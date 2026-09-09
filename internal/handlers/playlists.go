package handlers

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
	"github.com/drevilish/playlist-lab/internal/services/scheduler"
)

type PlaylistsHandler struct {
	DB            *sql.DB
	PlexAuth      *auth.PlexClient
	Tmpl          *Templates
	Notifications *notifications.Store
	Queue         *actionqueue.Queue
}

func RegisterPlaylists(r chi.Router, mw *auth.Middleware, h *PlaylistsHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/", h.index)
		r.Get("/playlists/{plexId}", h.editor)
		r.Put("/playlists/{plexId}/tracks/{trackId}/move", h.moveTrack)
		r.Delete("/playlists/{plexId}/tracks/{trackId}", h.removeTrack)
		r.Delete("/playlists/{plexId}", h.deletePlaylist)
		r.Post("/playlists/bulk-delete", h.bulkDelete)
		r.Post("/playlists/{plexId}/clone", h.clone)
		r.Post("/playlists/{plexId}/shuffle", h.shuffle)
		r.Post("/playlists/{plexId}/sort", h.sortTracks)
		r.Post("/playlists/{plexId}/dedupe", h.dedupe)
		r.Post("/playlists/{plexId}/split", h.split)
		r.Put("/playlists/{plexId}/rename", h.rename)
		r.Post("/playlists/{plexId}/cover", h.uploadCover)
		r.Get("/playlists/{plexId}/search-tracks", h.searchTracks)
		r.Post("/playlists/{plexId}/tracks", h.addTracks)
		r.Post("/playlists/merge", h.merge)
		r.Get("/playlists/{plexId}/share", h.shareForm)
		r.Post("/playlists/{plexId}/share", h.share)
		r.Get("/playlists/shared-with-me", h.sharedWithMe)
	})
}

// client builds a plex.Client for the current user's selected server,
// resolving whether to use their account token or the server-specific one
// (see plex.ResolveToken). Returns nil, nil if no server is selected yet.
func (h *PlaylistsHandler) client(user *db.User) (*plex.Client, *db.UserServer, error) {
	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil {
		return nil, userServer, err
	}
	token := plex.ResolveToken(user.PlexToken, userServer.AccessToken.String)
	return plex.NewClient(userServer.ServerURL, token, h.PlexAuth.ClientID, "Playlist Lab"), userServer, nil
}

type playlistRow struct {
	PlexID         string
	Name           string
	Source         string
	SourceURL      string
	CoverURL       string
	TrackCount     int
	Duration       int64
	DBID           int64 // 0 if this playlist was never imported/tracked through Playlist Lab
	CreatedAt      int64 // unix seconds; zero if DBID is 0
	MissingCount   int
	ScheduleID     int64  // 0 if no refresh schedule exists for this playlist
	Frequency      string // "daily"/"weekly"/"fortnightly"/"monthly"; empty if no schedule
	NextRun        string // display string ("in 3d", "due", ""), see scheduler.NextRun
	NextRunAt      int64  // unix seconds backing NextRun's display string, for sorting; 0 if none
	LastRun        int64  // unix seconds of the schedule's most recent run; 0 if never run
	LastRunStatus  string // "success"/"failed"/"running"; empty if never run
	ScheduleFailed bool
	NeedsAttention bool
}

func (h *PlaylistsHandler) index(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	client, userServer, err := h.client(user)
	if err != nil {
		slog.Error("failed to load user server", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if userServer == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	plexPlaylists, err := client.GetPlaylists()
	if err != nil {
		slog.Error("failed to fetch playlists from Plex", "error", err)
		h.Tmpl.RenderPage(w, r, "home", map[string]any{"User": user, "PlexError": true})
		return
	}

	tracked, err := db.GetUserPlaylists(h.DB, user.ID)
	if err != nil {
		slog.Error("failed to load tracked playlists", "error", err)
	}
	trackedByPlexID := make(map[string]db.Playlist, len(tracked))
	for _, p := range tracked {
		trackedByPlexID[p.PlexPlaylistID] = p
	}

	missingTracks, err := db.GetUserMissingTracks(h.DB, user.ID)
	if err != nil {
		slog.Error("failed to load missing tracks", "error", err)
	}
	missingCountByPlaylistID := make(map[int64]int, len(missingTracks))
	for _, t := range missingTracks {
		missingCountByPlaylistID[t.PlaylistID]++
	}

	schedules, err := db.GetUserSchedules(h.DB, user.ID)
	if err != nil {
		slog.Error("failed to load schedules", "error", err)
	}
	scheduleByPlaylistID := make(map[int64]db.Schedule, len(schedules))
	totalScheduled := 0
	for _, s := range schedules {
		if s.PlaylistID.Valid {
			scheduleByPlaylistID[s.PlaylistID.Int64] = s
			if s.ScheduleType == "playlist_refresh" {
				totalScheduled++
			}
		}
	}

	// latestStatus maps schedule ID -> its most recent execution's status,
	// so a schedule whose last run failed can flag its playlist as needing
	// attention (matches the React source's needsAttention()).
	latestStatus, err := db.GetLatestExecutionStatuses(h.DB, user.ID)
	if err != nil {
		slog.Error("failed to load schedule execution statuses", "error", err)
	}

	var rows []playlistRow
	totalMissing, totalAttention := len(missingTracks), 0
	for _, p := range plexPlaylists {
		if p.PlaylistType != "audio" {
			continue
		}
		row := playlistRow{
			PlexID: p.RatingKey, Name: cleanPlaylistName(p.Title),
			Source: "plex", TrackCount: p.LeafCount, Duration: p.Duration,
			CoverURL: ImageProxyURL(p.Composite),
		}
		if t, ok := trackedByPlexID[p.RatingKey]; ok {
			row.Source = t.Source
			row.SourceURL = t.SourceURL.String
			row.DBID = t.ID
			row.CreatedAt = t.CreatedAt
			row.MissingCount = missingCountByPlaylistID[t.ID]
			if s, ok := scheduleByPlaylistID[t.ID]; ok {
				row.ScheduleID = s.ID
				row.Frequency = s.Frequency
				row.NextRun, row.NextRunAt = nextRunRelative(s)
				last := latestStatus[s.ID]
				row.ScheduleFailed = last.Status == "failed"
				row.LastRun = last.StartedAt
				row.LastRunStatus = last.Status
			}
		}
		row.NeedsAttention = row.MissingCount > 0 || row.ScheduleFailed
		if row.NeedsAttention {
			totalAttention++
		}
		rows = append(rows, row)
	}

	// Stat tiles describe the whole library, so they're already tallied
	// above - filtering only narrows the table below them.
	q := r.URL.Query()
	f := rowFilter{
		Search:      strings.TrimSpace(q.Get("q")),
		Source:      q.Get("fSource"),
		Missing:     q.Get("fMissing"),
		Schedule:    q.Get("fSchedule"),
		TracksMin:   q.Get("fTracksMin"),
		TracksMax:   q.Get("fTracksMax"),
		DurationMin: q.Get("fDurationMin"),
		DurationMax: q.Get("fDurationMax"),
		NextRun:     q.Get("fNextRun"),
		LastRun:     q.Get("fLastRun"),
		AddedAfter:  q.Get("fAddedAfter"),
		AddedBefore: q.Get("fAddedBefore"),
	}
	// Built from the unfiltered set: filtering by a source must not collapse
	// the dropdown down to only the source already chosen.
	sources := distinctSources(rows)
	totalPlaylists := len(rows)
	rows = filterRows(rows, f)
	// Default (no explicit ?sort=) is newest-added first, not whatever
	// order Plex happened to return - a column header click still starts
	// its own asc/desc toggle from scratch via SortHref, unaffected by
	// this default.
	sortKey, dir := q.Get("sort"), q.Get("dir")
	if sortKey == "" {
		sortKey, dir = "dateAdded", "desc"
	}
	sortRows(rows, sortKey, dir)

	h.Tmpl.RenderPage(w, r, "home", map[string]any{
		"User": user, "Playlists": rows, "TotalPlaylists": totalPlaylists,
		"TotalMissing": totalMissing, "TotalScheduled": totalScheduled, "TotalAttention": totalAttention,
		"Sort": sortKey, "Dir": dir,
		"Filter": f, "Sources": sources,
		"Query": queryState(q),
	})
}

// queryState lets templates build a link that changes exactly one query
// param while preserving every other one already in the URL (sort, filters,
// search) - sortHref used to always reset to bare "?sort=X&dir=Y", silently
// dropping any active filter every time a column header was clicked.
type queryState url.Values

// With returns "/?..." with key set to val (or removed, if val is ""),
// every other current param untouched.
func (q queryState) With(key, val string) string {
	v := url.Values{}
	for k, vals := range q {
		v[k] = append([]string(nil), vals...)
	}
	if val == "" {
		v.Del(key)
	} else {
		v.Set(key, val)
	}
	return "/?" + v.Encode()
}

// SortHref is sortHref's old toggle-direction logic, now filter-preserving.
func (q queryState) SortHref(key string) string {
	dir := "asc"
	if url.Values(q).Get("sort") == key && url.Values(q).Get("dir") == "asc" {
		dir = "desc"
	}
	v := url.Values{}
	for k, vals := range q {
		v[k] = append([]string(nil), vals...)
	}
	v.Set("sort", key)
	v.Set("dir", dir)
	return "/?" + v.Encode()
}

// rowFilter mirrors the React page's per-column filter dropdowns. Sorting
// already round-trips to the server here (this page renders once per
// request rather than holding client-side table state), so the filters ride
// the same query-string mechanism instead of needing their own.
type rowFilter struct {
	Search      string // matches playlist name, case-insensitive
	Source      string // exact source id ("spotify", "plex", ...); "" = any
	Missing     string // "has" | "none"; "" = any
	Schedule    string // "on" | "off"; "" = any
	TracksMin   string // inclusive; "" = unbounded. Kept as the raw form string
	TracksMax   string // (not int) so an empty/invalid input redisplays as typed.
	DurationMin string // minutes, inclusive; "" = unbounded
	DurationMax string // minutes, inclusive; "" = unbounded
	NextRun     string // "due" | "upcoming" | "none"; "" = any
	LastRun     string // "success" | "failed" | "never"; "" = any
	AddedAfter  string // yyyy-mm-dd, inclusive; "" = unbounded
	AddedBefore string // yyyy-mm-dd, inclusive; "" = unbounded
}

func (f rowFilter) Active() bool {
	return f.Search != "" || f.Source != "" || f.Missing != "" || f.Schedule != "" ||
		f.TracksMin != "" || f.TracksMax != "" || f.DurationMin != "" || f.DurationMax != "" ||
		f.NextRun != "" || f.LastRun != "" || f.AddedAfter != "" || f.AddedBefore != ""
}

func filterRows(rows []playlistRow, f rowFilter) []playlistRow {
	if !f.Active() {
		return rows
	}
	search := strings.ToLower(f.Search)
	out := rows[:0:0]
	for _, row := range rows {
		if search != "" && !strings.Contains(strings.ToLower(row.Name), search) {
			continue
		}
		if f.Source != "" && row.Source != f.Source {
			continue
		}
		switch f.Missing {
		case "has":
			if row.MissingCount == 0 {
				continue
			}
		case "none":
			if row.MissingCount > 0 {
				continue
			}
		}
		switch f.Schedule {
		case "on":
			if row.ScheduleID == 0 {
				continue
			}
		case "off":
			if row.ScheduleID != 0 {
				continue
			}
		}
		if min, ok := parseIntFilter(f.TracksMin); ok && row.TrackCount < min {
			continue
		}
		if max, ok := parseIntFilter(f.TracksMax); ok && row.TrackCount > max {
			continue
		}
		durationMin := row.Duration / 60000
		if min, ok := parseIntFilter(f.DurationMin); ok && durationMin < int64(min) {
			continue
		}
		if max, ok := parseIntFilter(f.DurationMax); ok && durationMin > int64(max) {
			continue
		}
		switch f.NextRun {
		case "due":
			if row.NextRunAt == 0 || row.NextRunAt > time.Now().Unix() {
				continue
			}
		case "upcoming":
			if row.NextRunAt == 0 || row.NextRunAt <= time.Now().Unix() {
				continue
			}
		case "none":
			if row.NextRunAt != 0 {
				continue
			}
		}
		switch f.LastRun {
		case "success", "failed":
			if row.LastRunStatus != f.LastRun {
				continue
			}
		case "never":
			if row.LastRun != 0 {
				continue
			}
		}
		if t, ok := parseDateFilter(f.AddedAfter); ok && row.CreatedAt < t.Unix() {
			continue
		}
		if t, ok := parseDateFilter(f.AddedBefore); ok && row.CreatedAt >= t.AddDate(0, 0, 1).Unix() {
			continue
		}
		out = append(out, row)
	}
	return out
}

// parseIntFilter parses a rowFilter min/max form field, "" (unset) reporting
// ok=false so the caller leaves that bound unenforced rather than treating a
// blank input as zero.
func parseIntFilter(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	return n, err == nil
}

// parseDateFilter parses a rowFilter date form field (yyyy-mm-dd, the
// browser's native <input type=date> format).
func parseDateFilter(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", raw)
	return t, err == nil
}

// distinctSources lists the sources present so the Source filter only
// offers values that actually match something.
func distinctSources(rows []playlistRow) []string {
	seen := make(map[string]bool, len(rows))
	var out []string
	for _, row := range rows {
		if row.Source != "" && !seen[row.Source] {
			seen[row.Source] = true
			out = append(out, row.Source)
		}
	}
	sort.Strings(out)
	return out
}

// sortRows applies the Playlists page's column sort server-side (the Go
// port renders once per request rather than keeping client-side sort state
// like the React page does). Unrecognized/empty sort keys leave Plex's
// original ordering alone.
func sortRows(rows []playlistRow, sortKey, dir string) {
	if sortKey == "" {
		return
	}
	less := func(i, j int) bool {
		switch sortKey {
		case "name":
			return rows[i].Name < rows[j].Name
		case "source":
			return rows[i].Source < rows[j].Source
		case "tracks":
			return rows[i].TrackCount < rows[j].TrackCount
		case "duration":
			return rows[i].Duration < rows[j].Duration
		case "missing":
			return rows[i].MissingCount < rows[j].MissingCount
		// ScheduleID/NextRunAt/LastRun are 0 for an unscheduled/never-run
		// playlist, so - same as every other numeric column above - it
		// simply sorts first ascending, last descending rather than being
		// pinned to one end regardless of direction.
		case "schedule":
			return rows[i].ScheduleID < rows[j].ScheduleID
		case "nextRun":
			return rows[i].NextRunAt < rows[j].NextRunAt
		case "lastRun":
			return rows[i].LastRun < rows[j].LastRun
		case "dateAdded":
			return rows[i].CreatedAt < rows[j].CreatedAt
		default:
			return false
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if dir == "desc" {
			return less(j, i)
		}
		return less(i, j)
	})
}

// nextRunRelative ports scheduleTime.ts's getNextRunRelative: a compact
// "due"/"<1h"/"5h"/"3d" form of a schedule's next run, for the playlist
// table's Schedule column. Also returns the raw next-run time as unix
// seconds (0 if none) - the Next Run column sorts on that, since the
// display string doesn't compare chronologically ("5h" < "3d" as text).
func nextRunRelative(s db.Schedule) (string, int64) {
	next, ok := scheduler.NextRun(s, time.Now())
	if !ok {
		return "", 0
	}
	diff := time.Until(next)
	if diff <= 0 {
		return "due", next.Unix()
	}
	if diff < time.Hour {
		return "<1h", next.Unix()
	}
	if diff < 24*time.Hour {
		return fmt.Sprintf("%dh", int(diff.Round(time.Hour).Hours())), next.Unix()
	}
	return fmt.Sprintf("%dd", int(diff.Round(24*time.Hour).Hours()/24)), next.Unix()
}

// cleanPlaylistName strips a duplicated leading prefix Plex sometimes
// produces (e.g. "All out - All out 60s" -> "All out 60s"), matching the
// Node route's cleanup.
func cleanPlaylistName(name string) string {
	before, after, found := strings.Cut(name, " - ")
	// Node's split(' - ') only applies this cleanup when there is EXACTLY
	// one " - " (parts.length === 2) - strings.Cut alone only looks at the
	// first occurrence, so a second " - " later in the title (e.g.
	// "A - A - B") would otherwise get cleaned here but left untouched by
	// the original.
	if !found || strings.Contains(after, " - ") {
		return name
	}
	firstWord, _, _ := strings.Cut(after, " ")
	if before == firstWord {
		return after
	}
	return name
}

type trackRow struct {
	RatingKey      string
	PlaylistItemID string
	Title          string
	Artist         string
	Album          string
	Codec          string
}

func (h *PlaylistsHandler) editor(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	tracks, err := client.GetPlaylistTracks(plexID)
	if err != nil {
		slog.Error("failed to fetch playlist tracks", "error", err, "plexId", plexID)
		h.Tmpl.RenderPage(w, r, "editor", map[string]any{"PlexError": true})
		return
	}

	// Same lookup clone() uses: GetPlaylists() has no per-item Get, only a
	// full list, but the editor page is opened rarely enough (once per
	// playlist visited) that fetching it isn't worth adding a dedicated
	// client method for.
	name := plexID
	if playlists, err := client.GetPlaylists(); err == nil {
		for _, p := range playlists {
			if p.RatingKey == plexID {
				name = p.Title
				break
			}
		}
	}

	h.Tmpl.RenderPage(w, r, "editor", map[string]any{
		"PlexID": plexID,
		"Name":   name,
		"Tracks": toTrackRows(tracks),
	})
}

func toTrackRows(tracks []plex.Track) []trackRow {
	rows := make([]trackRow, len(tracks))
	for i, t := range tracks {
		itemID := ""
		if t.PlaylistItemID != 0 {
			itemID = strconv.Itoa(t.PlaylistItemID)
		}
		rows[i] = trackRow{
			RatingKey: t.RatingKey, PlaylistItemID: itemID,
			Title: t.Title, Artist: t.DisplayArtist(), Album: t.ParentTitle, Codec: t.Codec(),
		}
	}
	return rows
}

func (h *PlaylistsHandler) moveTrack(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	trackID := chi.URLParam(r, "trackId")
	afterID := r.FormValue("afterId")

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	if err := client.MovePlaylistItem(plexID, trackID, afterID); err != nil {
		slog.Error("failed to move track", "error", err)
		http.Error(w, "Failed to reorder track", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PlaylistsHandler) removeTrack(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	trackID := chi.URLParam(r, "trackId")

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	if err := client.RemoveFromPlaylist(plexID, trackID); err != nil {
		slog.Error("failed to remove track", "error", err)
		http.Error(w, "Failed to remove track", http.StatusBadGateway)
		return
	}
	if tracked, _ := db.GetPlaylistByPlexID(h.DB, user.ID, plexID); tracked != nil {
		_ = db.TouchPlaylist(h.DB, tracked.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// clone ports PlaylistsPage.tsx's handleClone(): duplicate a Plex playlist
// as a new one with the same tracks. Reuses the same
// BuildLibraryURI/BuildTrackURI/CreatePlaylist pieces adapters/plex/target.go
// already uses for cross-import - a straight copy, no new Plex API surface.
func (h *PlaylistsHandler) clone(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	tracks, err := client.GetPlaylistTracks(plexID)
	if err != nil {
		slog.Error("clone: failed to load source playlist tracks", "error", err)
		http.Error(w, "Failed to load playlist tracks", http.StatusBadGateway)
		return
	}
	machineID, err := client.GetMachineIdentifier()
	if err != nil {
		slog.Error("clone: failed to get machine identifier", "error", err)
		http.Error(w, "Failed to reach Plex server", http.StatusBadGateway)
		return
	}
	trackURIs := make([]string, len(tracks))
	for i, t := range tracks {
		trackURIs[i] = client.BuildTrackURI(t.RatingKey, machineID)
	}

	playlists, err := client.GetPlaylists()
	if err != nil {
		http.Error(w, "Failed to load playlists", http.StatusBadGateway)
		return
	}
	var sourceName string
	for _, p := range playlists {
		if p.RatingKey == plexID {
			sourceName = p.Title
			break
		}
	}
	name := sourceName + " (Copy)"
	if _, err := client.CreatePlaylist(name, client.BuildLibraryURI(userServer.LibraryID.String, machineID), trackURIs); err != nil {
		slog.Error("clone: failed to create cloned playlist", "error", err)
		http.Error(w, "Failed to create cloned playlist", http.StatusBadGateway)
		return
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (h *PlaylistsHandler) deletePlaylist(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	if err := deleteOnePlaylist(h.DB, client, user.ID, plexID); err != nil {
		slog.Error("failed to delete playlist in Plex", "error", err)
		http.Error(w, "Failed to delete playlist", http.StatusBadGateway)
		return
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

// deleteOnePlaylist is deletePlaylist's Plex-call-plus-DB-cleanup, factored
// out so bulkDelete (delete Selected on the Playlists page) can run it per
// id without duplicating the logic.
func deleteOnePlaylist(sqlDB *sql.DB, client *plex.Client, userID int64, plexID string) error {
	if err := client.DeletePlaylist(plexID); err != nil {
		return err
	}
	if tracked, _ := db.GetPlaylistByPlexID(sqlDB, userID, plexID); tracked != nil {
		_ = db.DeletePlaylistRow(sqlDB, tracked.ID)
	}
	return nil
}

// bulkDelete ports PlaylistsPage.tsx's handleBulkDelete(): delete every
// selected playlist from Plex, tolerating individual failures (matches the
// React version's Promise.allSettled - one bad id shouldn't block the rest).
func (h *PlaylistsHandler) bulkDelete(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	_ = r.ParseForm()
	ids := r.Form["id"]
	failed := 0
	for _, id := range ids {
		if err := deleteOnePlaylist(h.DB, client, user.ID, id); err != nil {
			slog.Error("bulk delete: failed to delete playlist", "plexId", id, "error", err)
			failed++
		}
	}
	if failed > 0 {
		slog.Warn("bulk delete finished with failures", "failed", failed, "total", len(ids))
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

// merge ports routes/playlists.ts's POST /merge: combine two or more Plex
// playlists' tracks (deduped by rating key) into a new playlist, or append
// them onto an existing one. Runs through the action queue like the Node
// version's enqueueAction, since fetching every source playlist's tracks can
// take a while.
func (h *PlaylistsHandler) merge(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	_ = r.ParseForm()
	sourceIDs := r.Form["id"]
	targetName := strings.TrimSpace(r.FormValue("targetName"))
	if len(sourceIDs) < 2 {
		http.Error(w, "select at least 2 playlists to merge", http.StatusBadRequest)
		return
	}
	if targetName == "" {
		http.Error(w, "targetName is required", http.StatusBadRequest)
		return
	}
	client, userServer, err := h.client(user)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "no server/library selected", http.StatusBadRequest)
		return
	}

	h.Queue.Enqueue(user.ID, "Merge playlists", notifications.TypeAction, func(notificationID string) error {
		seen := make(map[string]bool)
		var merged []string
		for _, id := range sourceIDs {
			tracks, err := client.GetPlaylistTracks(id)
			if err != nil {
				return fmt.Errorf("failed to load tracks for %s: %w", id, err)
			}
			for _, t := range tracks {
				if t.RatingKey != "" && !seen[t.RatingKey] {
					seen[t.RatingKey] = true
					merged = append(merged, t.RatingKey)
				}
			}
		}
		if len(merged) == 0 {
			return fmt.Errorf("the selected playlists have no tracks to merge")
		}
		machineID, err := client.GetMachineIdentifier()
		if err != nil {
			return err
		}
		trackURIs := make([]string, len(merged))
		for i, key := range merged {
			trackURIs[i] = client.BuildTrackURI(key, machineID)
		}
		libraryURI := client.BuildLibraryURI(userServer.LibraryID.String, machineID)
		if _, err := client.CreatePlaylist(targetName, libraryURI, trackURIs); err != nil {
			return err
		}
		h.Notifications.Update(user.ID, notificationID, notifications.Patch{
			Detail: strPtr(fmt.Sprintf("Merged %d tracks into %q", len(merged), targetName)),
		})
		return nil
	})
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func strPtr(s string) *string { return &s }

// applyTrackOrder rewrites a playlist's order to match ordered, by moving
// each track after the one before it - ports playlists.ts's applyTrackOrder,
// shared by shuffle and sort so there is one description of how a reorder
// is performed.
//
// ponytail: Plex's playlist API has no batch-reorder endpoint, so this is
// O(n) sequential MovePlaylistItem calls - fine for typical playlist sizes,
// but slow (and non-atomic - a mid-run failure leaves a partial reorder) for
// very large ones. Revisit if Plex ever adds a bulk-reorder endpoint.
func applyTrackOrder(client *plex.Client, plexID string, ordered []plex.Track) error {
	afterID := "0"
	for _, t := range ordered {
		itemID := strconv.Itoa(t.PlaylistItemID)
		if err := client.MovePlaylistItem(plexID, itemID, afterID); err != nil {
			return err
		}
		afterID = itemID
	}
	return nil
}

// touchTrackedPlaylist bumps a tracked playlist's updated_at after an
// in-place reorder/dedupe, matching playlists.ts's `db.updatePlaylist(tracked.id,
// { updated_at: Date.now() })` - a no-op if this playlist was never
// imported/tracked through Playlist Lab (an untracked Plex playlist has
// nothing to bump).
func touchTrackedPlaylist(sqlDB *sql.DB, userID int64, plexID string) {
	tracked, err := db.GetPlaylistByPlexID(sqlDB, userID, plexID)
	if err != nil || tracked == nil {
		return
	}
	if err := db.TouchPlaylist(sqlDB, tracked.ID); err != nil {
		slog.Warn("failed to touch playlist after reorder", "error", err, "playlistId", tracked.ID)
	}
}

// shuffle ports POST /api/playlists/:id/shuffle: randomize a playlist's
// track order in place. Runs through the action queue since a full
// sequential reorder (see applyTrackOrder) can take a while.
func (h *PlaylistsHandler) shuffle(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}

	h.Queue.Enqueue(user.ID, "Shuffle playlist", notifications.TypeAction, func(notificationID string) error {
		tracks, err := client.GetPlaylistTracks(plexID)
		if err != nil {
			return err
		}
		shuffled := onlyRealItems(tracks)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if err := applyTrackOrder(client, plexID, shuffled); err != nil {
			return err
		}
		touchTrackedPlaylist(h.DB, user.ID, plexID)
		status := notifications.StatusSuccess
		detail := fmt.Sprintf("Shuffled %d tracks", len(shuffled))
		h.Notifications.Update(user.ID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return nil
	})
	// 202 Accepted (matches playlists.ts's res.status(202).json(...) for
	// these same endpoints), with the notifications fragment rendered
	// immediately into the body - same as every other action-queue trigger
	// (missing.go's deemixAll, retry, etc.) - rather than an empty response.
	// This is what actually swaps into #notifications first, before the SSE
	// stream catches up with the same content a moment later; an empty body
	// would blank the bell out to nothing the instant the request completes,
	// since the button's hx-target="#notifications" hx-swap="innerHTML" has
	// nothing else to put there.
	w.WriteHeader(http.StatusAccepted)
	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{"Notifications": h.Notifications.List(user.ID)})
}

// onlyRealItems filters out entries with no PlaylistItemID: a smart
// (dynamically-generated) playlist's items have none and can't be
// reordered/removed individually, same filter playlists.ts applies before
// shuffling/sorting/deduping.
func onlyRealItems(tracks []plex.Track) []plex.Track {
	out := make([]plex.Track, 0, len(tracks))
	for _, t := range tracks {
		if t.PlaylistItemID != 0 {
			out = append(out, t)
		}
	}
	return out
}

// sortKeys are the fields a playlist can be sorted on, and how to read each
// one off a Track - ports playlists.ts's SORT_KEYS. Values are returned as
// `any` (string or int64) so sortTracks's comparator can dispatch on type
// the same way the original's readKey(a)/readKey(b) does.
var sortKeys = map[string]func(plex.Track) any{
	"title":    func(t plex.Track) any { return t.Title },
	"artist":   func(t plex.Track) any { return firstNonEmpty(t.GrandparentTitle, t.OriginalTitle) },
	"album":    func(t plex.Track) any { return t.ParentTitle },
	"year":     func(t plex.Track) any { return int64(t.Year) },
	"duration": func(t plex.Track) any { return t.Duration },
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// sortTracks ports POST /api/playlists/:id/sort: reorder a playlist by one
// of sortKeys. Form fields: by, direction ("asc"/"desc", default asc).
func (h *PlaylistsHandler) sortTracks(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	_ = r.ParseForm()
	by := r.FormValue("by")
	readKey, ok := sortKeys[by]
	if !ok {
		names := make([]string, 0, len(sortKeys))
		for k := range sortKeys {
			names = append(names, k)
		}
		sort.Strings(names)
		http.Error(w, "by must be one of: "+strings.Join(names, ", "), http.StatusBadRequest)
		return
	}
	descending := r.FormValue("direction") == "desc"

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}

	h.Queue.Enqueue(user.ID, "Sort playlist", notifications.TypeAction, func(notificationID string) error {
		tracks, err := client.GetPlaylistTracks(plexID)
		if err != nil {
			return err
		}
		sorted := onlyRealItems(tracks)
		sort.SliceStable(sorted, func(i, j int) bool {
			left, right := readKey(sorted[i]), readKey(sorted[j])
			var less bool
			if ls, ok := left.(string); ok {
				less = strings.ToLower(ls) < strings.ToLower(right.(string))
			} else {
				less = left.(int64) < right.(int64)
			}
			if descending {
				return !less
			}
			return less
		})
		if err := applyTrackOrder(client, plexID, sorted); err != nil {
			return err
		}
		touchTrackedPlaylist(h.DB, user.ID, plexID)
		status := notifications.StatusSuccess
		detail := fmt.Sprintf("Sorted %d tracks by %s", len(sorted), by)
		h.Notifications.Update(user.ID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return nil
	})
	// 202 Accepted (matches playlists.ts's res.status(202).json(...) for
	// these same endpoints), with the notifications fragment rendered
	// immediately into the body - same as every other action-queue trigger
	// (missing.go's deemixAll, retry, etc.) - rather than an empty response.
	// This is what actually swaps into #notifications first, before the SSE
	// stream catches up with the same content a moment later; an empty body
	// would blank the bell out to nothing the instant the request completes,
	// since the button's hx-target="#notifications" hx-swap="innerHTML" has
	// nothing else to put there.
	w.WriteHeader(http.StatusAccepted)
	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{"Notifications": h.Notifications.List(user.ID)})
}

// dedupe ports POST /api/playlists/:id/dedupe: remove repeated tracks,
// keeping the first occurrence of each so the playlist's order is otherwise
// untouched. Duplicates are judged by Plex ratingKey (the same track added
// twice), not by title, so two genuinely different recordings of a song are
// both kept.
func (h *PlaylistsHandler) dedupe(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}

	h.Queue.Enqueue(user.ID, "Remove duplicates", notifications.TypeAction, func(notificationID string) error {
		tracks, err := client.GetPlaylistTracks(plexID)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		var duplicateItemIDs []string
		for _, t := range tracks {
			if t.PlaylistItemID == 0 || t.RatingKey == "" {
				continue
			}
			if seen[t.RatingKey] {
				duplicateItemIDs = append(duplicateItemIDs, strconv.Itoa(t.PlaylistItemID))
				continue
			}
			seen[t.RatingKey] = true
		}
		if len(duplicateItemIDs) > 0 {
			if err := client.RemoveMultipleFromPlaylist(plexID, duplicateItemIDs); err != nil {
				return err
			}
		}
		touchTrackedPlaylist(h.DB, user.ID, plexID)
		status := notifications.StatusSuccess
		detail := fmt.Sprintf("Removed %d duplicate(s)", len(duplicateItemIDs))
		h.Notifications.Update(user.ID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return nil
	})
	// 202 Accepted (matches playlists.ts's res.status(202).json(...) for
	// these same endpoints), with the notifications fragment rendered
	// immediately into the body - same as every other action-queue trigger
	// (missing.go's deemixAll, retry, etc.) - rather than an empty response.
	// This is what actually swaps into #notifications first, before the SSE
	// stream catches up with the same content a moment later; an empty body
	// would blank the bell out to nothing the instant the request completes,
	// since the button's hx-target="#notifications" hx-swap="innerHTML" has
	// nothing else to put there.
	w.WriteHeader(http.StatusAccepted)
	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{"Notifications": h.Notifications.List(user.ID)})
}

// split ports POST /api/playlists/:id/split: copy a chosen set of tracks
// (by ratingKey, form field "trackId", repeatable) into a brand-new
// playlist named by the "name" form field. The original is untouched -
// "split" here means "peel a subset off into its own playlist", matching
// what PlaylistEditor.tsx's handleSplitSelected actually does (one new
// playlist from the current selection), not a multi-group operation - v2's
// route accepted an array of {name, trackIds} groups, but no caller in the
// real app ever sent more than one group, so that generality isn't ported.
func (h *PlaylistsHandler) split(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	trackIDs := r.Form["trackId"]
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if len(trackIDs) == 0 {
		http.Error(w, "select at least one track to split off", http.StatusBadRequest)
		return
	}

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "no server/library selected", http.StatusBadRequest)
		return
	}

	h.Queue.Enqueue(user.ID, "Split playlist", notifications.TypeAction, func(notificationID string) error {
		machineID, err := client.GetMachineIdentifier()
		if err != nil {
			return err
		}
		trackURIs := make([]string, len(trackIDs))
		for i, key := range trackIDs {
			trackURIs[i] = client.BuildTrackURI(key, machineID)
		}
		libraryURI := client.BuildLibraryURI(userServer.LibraryID.String, machineID)
		if _, err := client.CreatePlaylist(name, libraryURI, trackURIs); err != nil {
			return err
		}
		status := notifications.StatusSuccess
		detail := fmt.Sprintf("Split %d track(s) into %q", len(trackURIs), name)
		h.Notifications.Update(user.ID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return nil
	})
	// 202 Accepted (matches playlists.ts's res.status(202).json(...) for
	// these same endpoints), with the notifications fragment rendered
	// immediately into the body - same as every other action-queue trigger
	// (missing.go's deemixAll, retry, etc.) - rather than an empty response.
	// This is what actually swaps into #notifications first, before the SSE
	// stream catches up with the same content a moment later; an empty body
	// would blank the bell out to nothing the instant the request completes,
	// since the button's hx-target="#notifications" hx-swap="innerHTML" has
	// nothing else to put there.
	w.WriteHeader(http.StatusAccepted)
	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{"Notifications": h.Notifications.List(user.ID)})
}

// rename ports PUT /api/playlists/:id: renames a playlist in Plex itself
// (not just this app's own record of it) so the new name shows up in every
// real Plex client too, matching playlists.ts's PUT /:id. Synchronous, not
// queued - a single rename call is fast enough that the original route
// didn't queue it either.
func (h *PlaylistsHandler) rename(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name cannot be empty", http.StatusBadRequest)
		return
	}

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	if err := client.RenamePlaylist(plexID, name); err != nil {
		slog.Error("rename: failed to rename playlist in Plex", "error", err)
		http.Error(w, "Failed to rename playlist", http.StatusBadGateway)
		return
	}
	if tracked, err := db.GetPlaylistByPlexID(h.DB, user.ID, plexID); err == nil && tracked != nil {
		if err := db.RenamePlaylistRow(h.DB, tracked.ID, name); err != nil {
			slog.Warn("rename: failed to update tracked playlist row", "error", err)
		}
	}
	// Back to this same playlist's editor, not home - the user is mid-edit,
	// not done with it just because they renamed it.
	w.Header().Set("HX-Redirect", "/playlists/"+plexID)
	w.WriteHeader(http.StatusOK)
}

// maxCoverUploadSize mirrors multer's 5MB limit in routes/playlists.ts's
// upload middleware for POST /:id/cover.
const maxCoverUploadSize = 5 << 20

// uploadCover ports POST /api/playlists/:id/cover: upload a local image
// file as a playlist's poster. Synchronous, not queued - a single-file
// upload is fast enough that the original route didn't queue it either.
func (h *PlaylistsHandler) uploadCover(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")

	if err := r.ParseMultipartForm(maxCoverUploadSize); err != nil {
		http.Error(w, "cover image too large (max 5MB)", http.StatusRequestEntityTooLarge)
		return
	}
	file, header, err := r.FormFile("cover")
	if err != nil {
		http.Error(w, "No file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxCoverUploadSize+1))
	if err != nil {
		http.Error(w, "failed to read uploaded file", http.StatusInternalServerError)
		return
	}
	if len(body) > maxCoverUploadSize {
		http.Error(w, "cover image too large (max 5MB)", http.StatusRequestEntityTooLarge)
		return
	}
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "No server configured", http.StatusBadRequest)
		return
	}
	if err := client.UploadPlaylistPosterBytes(plexID, body, contentType); err != nil {
		slog.Error("failed to upload playlist cover", "error", err, "playlistId", plexID)
		http.Error(w, "Failed to upload cover", http.StatusBadGateway)
		return
	}
	// Back to this same playlist's editor, not home - same reasoning as
	// rename above.
	w.Header().Set("HX-Redirect", "/playlists/"+plexID)
	w.WriteHeader(http.StatusOK)
}

// searchTracks ports GET /api/search: the editor page's "search your
// library for a track to add" box, backed by the same
// plex.Client.SearchTrack every other search path in this app already
// uses. Renders a partial (checkboxes + an Add form) rather than JSON,
// since this whole app is server-rendered HTML.
func (h *PlaylistsHandler) searchTracks(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		h.Tmpl.RenderPartial(w, "partials/track_search_results.html", map[string]any{"PlexID": plexID})
		return
	}

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	tracks, err := client.SearchTrack(q, userServer.LibraryID.String, "", "")
	if err != nil {
		slog.Error("track search failed", "error", err, "query", q)
		http.Error(w, "search failed", http.StatusBadGateway)
		return
	}
	// Same 20-result cap as GET /api/search/tracks (the more targeted of
	// the two v2 search endpoints - this box searches by a single combined
	// query, matching what the editor's search field actually sends, not
	// GET /api/search's separate artist/track/album fields, which no
	// caller in this app used with more than one of those at once anyway).
	if len(tracks) > 20 {
		tracks = tracks[:20]
	}

	h.Tmpl.RenderPartial(w, "partials/track_search_results.html", map[string]any{
		"PlexID": plexID, "Query": q, "Results": toTrackRows(tracks),
	})
}

// addTracks ports POST /api/playlists/:id/tracks: add the tracks checked in
// the search results (by ratingKey) to this playlist.
func (h *PlaylistsHandler) addTracks(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	_ = r.ParseForm()
	trackIDs := r.Form["trackId"]
	if len(trackIDs) == 0 {
		http.Error(w, "select at least one track to add", http.StatusBadRequest)
		return
	}

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	machineID, err := client.GetMachineIdentifier()
	if err != nil {
		http.Error(w, "Failed to reach Plex server", http.StatusBadGateway)
		return
	}
	trackURIs := make([]string, len(trackIDs))
	for i, key := range trackIDs {
		trackURIs[i] = client.BuildTrackURI(key, machineID)
	}
	if err := client.AddToPlaylist(plexID, trackURIs); err != nil {
		slog.Error("failed to add tracks to playlist", "error", err, "playlistId", plexID)
		http.Error(w, "Failed to add tracks", http.StatusBadGateway)
		return
	}
	touchTrackedPlaylist(h.DB, user.ID, plexID)
	w.Header().Set("HX-Redirect", "/playlists/"+plexID)
	w.WriteHeader(http.StatusOK)
}

// shareForm renders the "share with another Playlist Lab user" picker
// (share-targets ported from routes/playlists.ts's GET /share-targets):
// every other user on this server who has a Plex server of their own
// configured, since sharing works by copying the playlist into their
// account.
func (h *PlaylistsHandler) shareForm(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	users, err := db.GetAllUsers(h.DB)
	if err != nil {
		http.Error(w, "failed to load users", http.StatusInternalServerError)
		return
	}
	var targets []db.AdminUserRow
	for _, u := range users {
		if u.ID != user.ID && u.HasServer {
			targets = append(targets, u)
		}
	}
	h.Tmpl.RenderPartial(w, "partials/share_form.html", map[string]any{
		"PlexID": plexID, "Targets": targets,
	})
}

// share ports POST /:id/share: copies the playlist's current tracks into the
// target user's own Plex account, then records the share so it shows up in
// their "Shared With Me" list.
func (h *PlaylistsHandler) share(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	targetUserID, err := strconv.ParseInt(r.FormValue("targetUserId"), 10, 64)
	if err != nil {
		http.Error(w, "invalid target user", http.StatusBadRequest)
		return
	}
	targetUser, err := db.GetUserByID(h.DB, targetUserID)
	if err != nil || targetUser == nil {
		http.Error(w, "target user not found", http.StatusBadRequest)
		return
	}
	targetServer, err := db.GetUserServer(h.DB, targetUserID)
	if err != nil || targetServer == nil || !targetServer.LibraryID.Valid {
		http.Error(w, "target user has no library selected", http.StatusBadRequest)
		return
	}

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	tracks, err := client.GetPlaylistTracks(plexID)
	if err != nil || len(tracks) == 0 {
		http.Error(w, "cannot share an empty or unreachable playlist", http.StatusBadGateway)
		return
	}
	playlists, err := client.GetPlaylists()
	if err != nil {
		http.Error(w, "failed to load playlists", http.StatusBadGateway)
		return
	}
	var name string
	for _, p := range playlists {
		if p.RatingKey == plexID {
			name = p.Title
			break
		}
	}
	if name == "" {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}

	targetToken := plex.ResolveToken(targetUser.PlexToken, targetServer.AccessToken.String)
	targetClient := plex.NewClient(targetServer.ServerURL, targetToken, h.PlexAuth.ClientID, "Playlist Lab")
	targetMachineID, err := targetClient.GetMachineIdentifier()
	if err != nil {
		http.Error(w, "failed to reach target server", http.StatusBadGateway)
		return
	}
	trackURIs := make([]string, len(tracks))
	for i, t := range tracks {
		trackURIs[i] = targetClient.BuildTrackURI(t.RatingKey, targetMachineID)
	}
	libraryURI := targetClient.BuildLibraryURI(targetServer.LibraryID.String, targetMachineID)
	newPlaylist, err := targetClient.CreatePlaylist(name, libraryURI, trackURIs)
	if err != nil {
		slog.Error("share: failed to create playlist for target user", "error", err)
		http.Error(w, "failed to create playlist for target user", http.StatusBadGateway)
		return
	}
	if _, err := db.CreatePlaylistRow(h.DB, targetUserID, newPlaylist.RatingKey, name, "shared", ""); err != nil {
		slog.Warn("share: failed to track new playlist row", "error", err)
	}

	sourceRow, err := db.GetPlaylistByPlexID(h.DB, user.ID, plexID)
	if err != nil || sourceRow == nil {
		sourceRow, err = db.CreatePlaylistRow(h.DB, user.ID, plexID, name, "plex", "")
		if err != nil {
			slog.Error("share: failed to record source playlist row", "error", err)
			http.Error(w, "shared, but failed to record the share", http.StatusInternalServerError)
			return
		}
	}
	if err := db.RecordPlaylistShare(h.DB, sourceRow.ID, user.ID, targetUserID, plexID, name); err != nil {
		slog.Error("share: failed to record share", "error", err)
	}
	h.Tmpl.RenderPartial(w, "partials/share_result.html", map[string]any{
		"PlaylistName": name, "TrackCount": len(tracks),
	})
}

// sharedWithMe ports GET /shared-with-me for the header's "Shared With Me"
// panel.
func (h *PlaylistsHandler) sharedWithMe(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	shared, err := db.GetPlaylistsSharedWithUser(h.DB, user.ID)
	if err != nil {
		http.Error(w, "failed to load shared playlists", http.StatusInternalServerError)
		return
	}
	h.Tmpl.RenderPartial(w, "partials/shared_with_me.html", map[string]any{"Shared": shared})
}

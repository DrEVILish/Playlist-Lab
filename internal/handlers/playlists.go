package handlers

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/plex"
	"github.com/drevilish/playlist-lab/internal/services/scheduler"
)

type PlaylistsHandler struct {
	DB       *sql.DB
	PlexAuth *auth.PlexClient
	Tmpl     *Templates
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
	NextRun        string // display string ("in 3d", "due", ""), see scheduler.NextRun
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
		h.Tmpl.RenderPage(w, "home", map[string]any{"User": user, "PlexError": true})
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
			CoverURL: client.MediaURL(p.Composite),
		}
		if t, ok := trackedByPlexID[p.RatingKey]; ok {
			row.Source = t.Source
			row.SourceURL = t.SourceURL.String
			row.DBID = t.ID
			row.CreatedAt = t.CreatedAt
			row.MissingCount = missingCountByPlaylistID[t.ID]
			if s, ok := scheduleByPlaylistID[t.ID]; ok {
				row.ScheduleID = s.ID
				row.NextRun = nextRunRelative(s)
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
		Search:   strings.TrimSpace(q.Get("q")),
		Source:   q.Get("fSource"),
		Missing:  q.Get("fMissing"),
		Schedule: q.Get("fSchedule"),
	}
	// Built from the unfiltered set: filtering by a source must not collapse
	// the dropdown down to only the source already chosen.
	sources := distinctSources(rows)
	totalPlaylists := len(rows)
	rows = filterRows(rows, f)
	sortRows(rows, q.Get("sort"), q.Get("dir"))

	h.Tmpl.RenderPage(w, "home", map[string]any{
		"User": user, "Playlists": rows, "TotalPlaylists": totalPlaylists,
		"TotalMissing": totalMissing, "TotalScheduled": totalScheduled, "TotalAttention": totalAttention,
		"Sort": q.Get("sort"), "Dir": q.Get("dir"),
		"Filter": f, "Sources": sources,
	})
}

// rowFilter mirrors the React page's per-column filter dropdowns. Sorting
// already round-trips to the server here (this page renders once per
// request rather than holding client-side table state), so the filters ride
// the same query-string mechanism instead of needing their own.
type rowFilter struct {
	Search   string // matches playlist name, case-insensitive
	Source   string // exact source id ("spotify", "plex", ...); "" = any
	Missing  string // "has" | "none"; "" = any
	Schedule string // "on" | "off"; "" = any
}

func (f rowFilter) Active() bool {
	return f.Search != "" || f.Source != "" || f.Missing != "" || f.Schedule != ""
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
		out = append(out, row)
	}
	return out
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
// table's Schedule column.
func nextRunRelative(s db.Schedule) string {
	next, ok := scheduler.NextRun(s, time.Now())
	if !ok {
		return ""
	}
	diff := time.Until(next)
	if diff <= 0 {
		return "due"
	}
	if diff < time.Hour {
		return "<1h"
	}
	if diff < 24*time.Hour {
		return fmt.Sprintf("%dh", int(diff.Round(time.Hour).Hours()))
	}
	return fmt.Sprintf("%dd", int(diff.Round(24*time.Hour).Hours()/24))
}

// cleanPlaylistName strips a duplicated leading prefix Plex sometimes
// produces (e.g. "All out - All out 60s" -> "All out 60s"), matching the
// Node route's cleanup.
func cleanPlaylistName(name string) string {
	before, after, found := strings.Cut(name, " - ")
	if !found {
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
		http.Error(w, "Failed to load playlist", http.StatusBadGateway)
		return
	}

	h.Tmpl.RenderPage(w, "editor", map[string]any{
		"PlexID": plexID,
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

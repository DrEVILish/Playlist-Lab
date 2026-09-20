// Package scheduler ports services/schedule-checker-job.ts: find due
// schedules and execute them, either a playlist refresh (via importsvc +
// direct Plex playlist writes) or a mix generation (via mixes.Service).
//
// Scope cut from the TS version: every schedule here is tied to an already-
// imported playlist row (schedules.playlist_id), including what TS called
// "chart import schedules" - those existed there because a schedule could
// predate any playlist row when triggered from a raw chart URL. In this Go
// server every import (including from a chart source) already creates a
// playlist row up front (see importsvc.FinalizeImportResult /
// handlers/import.go), so there's always a playlist to schedule against and
// no separate "resolve by source URL/name" fallback is needed. Mix
// generation schedules only support the single-mix-type shape (config.mixType
// + default settings for that type) - TS's template-based and legacy
// multi-mix-array schedule shapes have no equivalent creation path in this
// server's UI yet, so aren't ported.
package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/imdb"
	"github.com/drevilish/playlist-lab/internal/services/importsvc"
	"github.com/drevilish/playlist-lab/internal/services/letterboxd"
	"github.com/drevilish/playlist-lab/internal/services/matching"
	"github.com/drevilish/playlist-lab/internal/services/medialist"
	"github.com/drevilish/playlist-lab/internal/services/mixes"
	"github.com/drevilish/playlist-lab/internal/services/plex"
	"github.com/drevilish/playlist-lab/internal/services/tmdb"
	"github.com/drevilish/playlist-lab/internal/services/trakt"
	"github.com/drevilish/playlist-lab/internal/services/tvdb"
)

// tmdbAdminConfigKey/tvdbAdminConfigKey/traktAdminConfigKey mirror
// handlers/admin.go's configKeyTMDbAPIKey/configKeyTVDbAPIKey/
// configKeyTraktAPIKey - duplicated rather than imported to avoid a
// handlers<->scheduler import cycle (handlers already imports scheduler
// for RefreshCollection). IMDb/Letterboxd need no key (scraping only, same
// as this app's other unauthenticated playlist scrapers).
const (
	tmdbAdminConfigKey  = "tmdb_api_key"
	tvdbAdminConfigKey  = "tvdb_api_key"
	traktAdminConfigKey = "trakt_api_key"
)

// frequencySeconds ports getDueSchedules()'s switch on schedule.frequency.
var frequencySeconds = map[string]int64{
	"daily":       86400,
	"weekly":      604800,
	"fortnightly": 1209600,
	"monthly":     2592000,
}

// IsDue ports database.ts's getDueSchedules() per-schedule predicate as a
// pure function of (schedule, now) so it's unit-testable without a DB - see
// scheduler_test.go for the run_time/frequency edge cases.
func IsDue(s db.Schedule, now time.Time) bool {
	cfg := s.ParsedConfig()
	nowUnix := now.Unix()

	atOrPastRunTime := func() bool {
		if cfg.RunTime == "" {
			return true
		}
		var hour, minute int
		if _, err := fmt.Sscanf(cfg.RunTime, "%d:%d", &hour, &minute); err != nil {
			return true
		}
		scheduled := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		return !now.Before(scheduled)
	}

	if !s.LastRun.Valid {
		startDate, err := time.ParseInLocation("2006-01-02", s.StartDate, now.Location())
		if err != nil || now.Before(startDate) {
			return false
		}
		return atOrPastRunTime()
	}

	need, ok := frequencySeconds[s.Frequency]
	if !ok {
		return false
	}
	if nowUnix-s.LastRun.Int64 < need {
		return false
	}
	return atOrPastRunTime()
}

// NextRun ports getNextRunDate/getNextRunTimestamp (scheduleTime.ts) to Go:
// the next instant a schedule is expected to fire, for display only (IsDue
// above is what actually gates execution). Returns false if it can't be
// determined (bad start_date, unknown frequency).
func NextRun(s db.Schedule, now time.Time) (time.Time, bool) {
	if !s.LastRun.Valid {
		startDate, err := time.ParseInLocation("2006-01-02", s.StartDate, now.Location())
		if err != nil {
			return time.Time{}, false
		}
		return startDate, true
	}
	days, ok := frequencySeconds[s.Frequency]
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(s.LastRun.Int64, 0).Add(time.Duration(days) * time.Second), true
}

// Deps bundles what RunDue/RunSingle need to reach Plex and the rest of the
// app - one struct instead of a long parameter list, since both entry points
// need the same set.
type Deps struct {
	DB       *sql.DB
	Registry *adapters.Registry
	Mixes    *mixes.Service
	ClientID string // auth.PlexClient.ClientID, for building plex.Client instances
}

// RunDue is the cron-driven entry point (ported from runScheduleCheckerJob):
// finds every due schedule and executes it, logging per-schedule failures
// without aborting the batch.
func RunDue(d Deps) {
	all, err := db.GetDueSchedules(d.DB)
	if err != nil {
		slog.Error("schedule checker: failed to load schedules", "error", err)
		return
	}
	now := time.Now()
	var executed, failed int
	for _, s := range all {
		if !IsDue(s, now) {
			continue
		}
		if err := Run(d, s); err != nil {
			slog.Error("schedule checker: schedule failed", "scheduleId", s.ID, "error", err)
			failed++
			continue
		}
		executed++
	}
	if executed+failed > 0 {
		slog.Info("schedule checker job completed", "executed", executed, "failed", failed)
	} else {
		slog.Debug("schedule checker job completed", "executed", 0, "failed", 0)
	}
}

// Run executes one schedule regardless of whether it's currently due -
// shared by the cron batch (RunDue, after an IsDue check) and manual
// "Run Now"/"Run All" triggers (handlers/schedules.go), which skip the
// due-check entirely.
func Run(d Deps, s db.Schedule) error {
	switch s.ScheduleType {
	case "playlist_refresh":
		return executePlaylistRefresh(d, s)
	case "mix_generation":
		return executeMixGeneration(d, s)
	case "collection_refresh":
		return executeCollectionRefresh(d, s)
	default:
		return fmt.Errorf("unknown schedule type: %s", s.ScheduleType)
	}
}

func buildClient(d Deps, user *db.User, server *db.UserServer) *plex.Client {
	token := plex.ResolveToken(user.PlexToken, server.AccessToken.String)
	return plex.NewClient(server.ServerURL, token, d.ClientID, "Playlist Lab")
}

// executePlaylistRefresh ports executePlaylistRefreshSchedules' per-schedule
// body: re-scrape+match the linked playlist's source, then reconcile the
// result into the existing Plex playlist per updateMode (replace/accumulate)
// rather than creating a new one, so the playlist's ratingKey never changes
// across runs.
func executePlaylistRefresh(d Deps, s db.Schedule) error {
	if !s.PlaylistID.Valid {
		return fmt.Errorf("schedule %d has no linked playlist", s.ID)
	}
	user, err := db.GetUserByID(d.DB, s.UserID)
	if err != nil || user == nil {
		return fmt.Errorf("user not found for schedule %d", s.ID)
	}
	server, err := db.GetUserMusicServer(d.DB, s.UserID)
	if err != nil || server == nil {
		return fmt.Errorf("no Plex server configured for schedule %d", s.ID)
	}
	playlist, err := db.GetPlaylistByID(d.DB, s.PlaylistID.Int64)
	if err != nil || playlist == nil {
		return fmt.Errorf("playlist not found for schedule %d", s.ID)
	}

	executionID, _ := db.CreateScheduleExecution(d.DB, s.ID, s.UserID, playlist.Name)
	fail := func(err error) error {
		if executionID != 0 {
			_ = db.CompleteScheduleExecution(d.DB, executionID, "failed", 0, 0, err.Error())
		}
		return err
	}

	client := buildClient(d, user, server)
	sourceIdentifier := playlist.SourceURL.String
	if sourceIdentifier == "" {
		sourceIdentifier = playlist.PlexPlaylistID
	}
	result, err := importsvc.ImportPlaylist(context.Background(), d.Registry, d.DB, client, playlist.Source, sourceIdentifier,
		importsvc.Options{UserID: s.UserID, LibraryID: server.LibraryID.String}, nil, nil)
	if err != nil {
		return fail(err)
	}

	cfg := s.ParsedConfig()
	updateMode := cfg.UpdateMode
	if updateMode != "accumulate" {
		updateMode = "replace"
	}

	matched := matching.DedupeByPlexRatingKey(result.Matched)
	trackURIs := make([]string, 0, len(matched))
	for _, t := range matched {
		if t.Matched && t.PlexRatingKey != "" {
			trackURIs = append(trackURIs, plex.BuildTrackURI(server.ServerClientID, t.PlexRatingKey))
		}
	}

	targetPlaylistID, err := resolveTargetPlaylistID(client, playlist.PlexPlaylistID, playlist.Name)
	if err != nil {
		slog.Warn("schedule refresh: failed to resolve target playlist, will create new", "scheduleId", s.ID, "error", err)
	}

	var newRatingKey string
	if targetPlaylistID != "" && updateMode == "accumulate" {
		if err := accumulateIntoPlaylist(client, targetPlaylistID, matched, server.ServerClientID); err != nil {
			return fail(err)
		}
		newRatingKey = targetPlaylistID
	} else if targetPlaylistID != "" {
		if err := replacePlaylistTracks(client, targetPlaylistID, trackURIs); err != nil {
			return fail(err)
		}
		newRatingKey = targetPlaylistID
	} else {
		libraryURI := client.BuildLibraryURI(server.LibraryID.String, server.ServerClientID)
		created, err := client.CreatePlaylist(playlist.Name, libraryURI, trackURIs)
		if err != nil {
			return fail(err)
		}
		newRatingKey = created.RatingKey
	}

	if result.CoverURL != "" {
		if err := client.UploadPlaylistPoster(newRatingKey, result.CoverURL); err != nil {
			slog.Warn("schedule refresh: failed to upload cover art", "scheduleId", s.ID, "error", err)
		}
	}

	if newRatingKey != playlist.PlexPlaylistID {
		_ = db.UpdatePlaylistPlexID(d.DB, playlist.ID, newRatingKey)
	} else {
		_ = db.TouchPlaylist(d.DB, playlist.ID)
	}

	if len(result.Unmatched) > 0 {
		rows := make([]db.NewMissingTrack, len(result.Unmatched))
		for i, t := range result.Unmatched {
			rows[i] = db.NewMissingTrack{Title: t.Title, Artist: t.Artist, Album: t.Album, Position: i, Source: "Scheduled refresh"}
		}
		if err := db.AddMissingTracks(d.DB, s.UserID, playlist.ID, rows); err != nil {
			slog.Warn("schedule refresh: failed to save missing tracks", "scheduleId", s.ID, "error", err)
		}
	}

	_ = db.UpdateScheduleLastRun(d.DB, s.ID)
	if executionID != 0 {
		_ = db.CompleteScheduleExecution(d.DB, executionID, "success", result.MatchedCount, len(result.Unmatched), "")
	}
	return nil
}

// resolveTargetPlaylistID ports resolveTargetPlaylistId: a refresh/mix-
// generation run always writes into the playlist it already tracks, falling
// back to a name search in Plex only for a stale/missing ratingKey (e.g. a
// "pending-..." placeholder from an interrupted first import).
func resolveTargetPlaylistID(client *plex.Client, trackedPlexID, playlistName string) (string, error) {
	if trackedPlexID != "" && !isPendingID(trackedPlexID) {
		return trackedPlexID, nil
	}
	playlists, err := client.GetPlaylists()
	if err != nil {
		return "", err
	}
	for _, p := range playlists {
		if p.Title == playlistName {
			return p.RatingKey, nil
		}
	}
	return "", nil
}

func isPendingID(id string) bool {
	return len(id) >= 8 && id[:8] == "pending-"
}

// replacePlaylistTracks clears a Plex playlist's current items and refills
// it with trackURIs, in order - same ratingKey throughout.
func replacePlaylistTracks(client *plex.Client, playlistID string, trackURIs []string) error {
	existing, err := client.GetPlaylistTracks(playlistID)
	if err != nil {
		return err
	}
	itemIDs := make([]string, 0, len(existing))
	for _, t := range existing {
		if t.PlaylistItemID != 0 {
			itemIDs = append(itemIDs, fmt.Sprintf("%d", t.PlaylistItemID))
		}
	}
	if len(itemIDs) > 0 {
		if err := client.RemoveMultipleFromPlaylist(playlistID, itemIDs); err != nil {
			return err
		}
	}
	if len(trackURIs) > 0 {
		return client.AddToPlaylist(playlistID, trackURIs)
	}
	return nil
}

// accumulateIntoPlaylist adds only the tracks not already present, leaving
// everything else in the playlist untouched.
func accumulateIntoPlaylist(client *plex.Client, playlistID string, matched []matching.MatchedTrack, serverClientID string) error {
	existing, err := client.GetPlaylistTracks(playlistID)
	if err != nil {
		return err
	}
	present := make(map[string]bool, len(existing))
	for _, t := range existing {
		present[t.RatingKey] = true
	}
	var newURIs []string
	for _, t := range matched {
		if t.Matched && t.PlexRatingKey != "" && !present[t.PlexRatingKey] {
			newURIs = append(newURIs, plex.BuildTrackURI(serverClientID, t.PlexRatingKey))
		}
	}
	if len(newURIs) > 0 {
		return client.AddToPlaylist(playlistID, newURIs)
	}
	return nil
}

// executeMixGeneration ports executeMixGenerationSchedules' config.mixType
// branch - see the package doc for what's not ported (templates, legacy
// multi-mix array).
func executeMixGeneration(d Deps, s db.Schedule) error {
	user, err := db.GetUserByID(d.DB, s.UserID)
	if err != nil || user == nil {
		return fmt.Errorf("user not found for schedule %d", s.ID)
	}
	server, err := db.GetUserMusicServer(d.DB, s.UserID)
	if err != nil || server == nil || !server.LibraryID.Valid {
		return fmt.Errorf("no Plex library selected for schedule %d", s.ID)
	}

	cfg := s.ParsedConfig()
	if cfg.MixType == "" {
		return fmt.Errorf("schedule %d has no mixType configured", s.ID)
	}
	playlistName := cfg.PlaylistName
	if playlistName == "" {
		playlistName = cfg.MixType + " Mix"
	}

	executionID, _ := db.CreateScheduleExecution(d.DB, s.ID, s.UserID, playlistName)
	fail := func(err error) error {
		if executionID != 0 {
			_ = db.CompleteScheduleExecution(d.DB, executionID, "failed", 0, 0, err.Error())
		}
		return err
	}

	token := plex.ResolveToken(user.PlexToken, server.AccessToken.String)
	serverURL, libraryID := server.ServerURL, server.LibraryID.String

	result, err := generateMixByType(d.Mixes, cfg.MixType, serverURL, token, libraryID)
	if err != nil {
		return fail(err)
	}
	if result.TrackCount == 0 {
		return fail(fmt.Errorf("no tracks generated"))
	}

	client := buildClient(d, user, server)
	trackURIs := make([]string, len(result.TrackKeys))
	for i, key := range result.TrackKeys {
		trackURIs[i] = client.BuildTrackURI(key, server.ServerClientID)
	}

	var linkedPlaylist *db.Playlist
	if s.PlaylistID.Valid {
		linkedPlaylist, _ = db.GetPlaylistByID(d.DB, s.PlaylistID.Int64)
	}
	trackedPlexID := ""
	if linkedPlaylist != nil {
		trackedPlexID = linkedPlaylist.PlexPlaylistID
	}
	targetPlaylistID, err := resolveTargetPlaylistID(client, trackedPlexID, playlistName)
	if err != nil {
		slog.Warn("schedule mix: failed to resolve target playlist, will create new", "scheduleId", s.ID, "error", err)
	}

	var ratingKey string
	if targetPlaylistID != "" {
		if err := replacePlaylistTracks(client, targetPlaylistID, trackURIs); err != nil {
			return fail(err)
		}
		ratingKey = targetPlaylistID
	} else {
		libraryURI := client.BuildLibraryURI(libraryID, server.ServerClientID)
		created, err := client.CreatePlaylist(playlistName, libraryURI, trackURIs)
		if err != nil {
			return fail(err)
		}
		ratingKey = created.RatingKey
	}

	if linkedPlaylist != nil {
		if ratingKey != linkedPlaylist.PlexPlaylistID {
			_ = db.UpdatePlaylistPlexID(d.DB, linkedPlaylist.ID, ratingKey)
		}
	} else {
		row, err := db.CreatePlaylistRow(d.DB, s.UserID, ratingKey, playlistName, "mix", "")
		if err == nil {
			_ = db.LinkSchedulePlaylist(d.DB, s.ID, row.ID)
		}
	}

	_ = db.UpdateScheduleLastRun(d.DB, s.ID)
	if executionID != 0 {
		_ = db.CompleteScheduleExecution(d.DB, executionID, "success", result.TrackCount, 0, "")
	}
	return nil
}

// executeCollectionRefresh is the schedule-driven wrapper around
// RefreshCollection: resolves the schedule's linked collection/user/server,
// then adds schedule_executions bookkeeping around the shared refresh core.
func executeCollectionRefresh(d Deps, s db.Schedule) error {
	if !s.CollectionID.Valid {
		return fmt.Errorf("schedule %d has no linked collection", s.ID)
	}
	coll, err := db.GetCollectionByID(d.DB, s.CollectionID.Int64)
	if err != nil || coll == nil {
		return fmt.Errorf("collection not found for schedule %d", s.ID)
	}
	user, err := db.GetUserByID(d.DB, s.UserID)
	if err != nil || user == nil {
		return fmt.Errorf("user not found for schedule %d", s.ID)
	}
	// A collection carries its own server (coll.ServerID), independent of
	// the user's single default (db.GetUserServer) - it may target any of a
	// user's connected libraries, not just their default music one.
	server, err := db.GetUserServerByID(d.DB, coll.ServerID)
	if err != nil || server == nil {
		return fmt.Errorf("server not found for collection %d", coll.ID)
	}

	executionID, _ := db.CreateScheduleExecution(d.DB, s.ID, s.UserID, coll.Name)
	added, removed, err := RefreshCollection(d.DB, buildClient(d, user, server), coll, server)
	if err != nil {
		if executionID != 0 {
			_ = db.CompleteScheduleExecution(d.DB, executionID, "failed", 0, 0, err.Error())
		}
		return err
	}
	_ = db.UpdateScheduleLastRun(d.DB, s.ID)
	if executionID != 0 {
		_ = db.CompleteScheduleExecution(d.DB, executionID, "success", added, removed, "")
	}
	return nil
}

// RefreshCollection evaluates a collection's definition (DESIGN.md §11.11),
// resolves or creates its Plex collection, and reconciles membership to
// match. This is the shared core between the schedule-driven path above and
// the Collections page's manual "Refresh Now" button, which has no
// schedule row to attach schedule_executions bookkeeping to (same
// precedent as mix_templates.go's runTemplate, a one-off action that skips
// that bookkeeping entirely). Persists the collection's plex_collection_id/
// updated_at itself either way. Returns (added, removed) item counts.
func RefreshCollection(sqlDB *sql.DB, client *plex.Client, coll *db.Collection, server *db.UserServer) (added, removed int, err error) {
	targetKeys, err := resolveCollectionTargets(sqlDB, client, coll)
	if err != nil {
		return 0, 0, err
	}
	if len(targetKeys) == 0 {
		return 0, 0, fmt.Errorf("collection %d matched no items", coll.ID)
	}

	plexCollectionID, err := resolveOrCreateCollection(client, coll, targetKeys, server)
	if err != nil {
		return 0, 0, err
	}

	if coll.SortTitle.Valid && coll.SortTitle.String != "" {
		if err := client.SetCollectionSortTitle(plexCollectionID, coll.SortTitle.String); err != nil {
			slog.Warn("collection refresh: failed to set sort title", "collectionId", coll.ID, "error", err)
		}
	}

	added, removed, err = reconcileCollectionMembership(client, plexCollectionID, targetKeys, coll.SyncMode, server.ServerClientID)
	if err != nil {
		return 0, 0, err
	}

	// An external-list/chart source is very often itself a ranking (IMDb's
	// Top 250, a TMDb "popular"/"trending" chart, ...), not an arbitrary
	// set - default every such collection to Plex's "Custom" item order,
	// actually arranged to match targetKeys (see ReorderCollection: a bulk
	// uri= add, which is all reconcileCollectionMembership above just did,
	// does NOT preserve request order on its own - confirmed live). Run
	// after reconcile, once every targetKeys entry is actually a member;
	// applied every refresh but each is a cheap no-op unless the order
	// genuinely needs fixing (a first build, or the source ranking having
	// reshuffled), so this doesn't cost len(targetKeys) API calls on every
	// single refresh regardless of whether anything changed.
	if coll.BuilderType == "external_list" {
		if err := client.SetCollectionCustomOrder(plexCollectionID); err != nil {
			slog.Warn("collection refresh: failed to set custom order", "collectionId", coll.ID, "error", err)
		}
		if err := client.ReorderCollection(plexCollectionID, targetKeys); err != nil {
			slog.Warn("collection refresh: failed to reorder items", "collectionId", coll.ID, "error", err)
		}
	}

	if plexCollectionID != coll.PlexCollectionID.String {
		_ = db.UpdateCollectionPlexID(sqlDB, coll.ID, plexCollectionID)
	} else {
		_ = db.TouchCollection(sqlDB, coll.ID)
	}
	return added, removed, nil
}

// itemTypeForLibraryType maps a Plex library section type to the numeric
// metadata type its content uses - see plex.CreateCollection's doc.
func itemTypeForLibraryType(libraryType string) int {
	switch libraryType {
	case "movie":
		return 1
	case "show":
		return 2
	default:
		// "artist" (music) - collections operate at track level in v1,
		// matching this app's existing track-centric model (DESIGN.md §11.11).
		return 10
	}
}

// resolveCollectionTargets evaluates a collection's definition (DESIGN.md
// §11.11: a flat, AND-combined rule list for "smart", or a plain item list
// for "manual") into the set of Plex ratingKeys it should currently
// contain.
func resolveCollectionTargets(sqlDB *sql.DB, client *plex.Client, coll *db.Collection) ([]string, error) {
	if coll.BuilderType == "manual" {
		return coll.ParsedManualItems(), nil
	}
	if coll.BuilderType == "external_list" {
		return resolveExternalListTargets(sqlDB, client, coll)
	}
	var opts plex.LibraryFilterOptions
	for _, r := range coll.ParsedRules() {
		switch r.Field {
		case "genre":
			opts.Genres = append(opts.Genres, r.Value)
		case "mood":
			opts.Moods = append(opts.Moods, r.Value)
		case "style":
			opts.Styles = append(opts.Styles, r.Value)
		case "content_rating":
			opts.ContentRatings = append(opts.ContentRatings, r.Value)
		case "studio":
			opts.Studios = append(opts.Studios, r.Value)
		case "actor":
			opts.Actors = append(opts.Actors, r.Value)
		case "director":
			opts.Directors = append(opts.Directors, r.Value)
		case "writer":
			opts.Writers = append(opts.Writers, r.Value)
		case "unwatched":
			opts.Unwatched = true
		case "year":
			if y, err := strconv.Atoi(r.Value); err == nil {
				opts.YearFrom, opts.YearTo = y, y
			}
		case "decade":
			if y, err := strconv.Atoi(r.Value); err == nil {
				opts.YearFrom, opts.YearTo = y, y+9
			}
		}
	}
	items, err := client.SearchLibraryItems(coll.LibrarySectionID, itemTypeForLibraryType(coll.LibraryType), opts)
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(items))
	for i, item := range items {
		keys[i] = item.RatingKey
	}
	return keys, nil
}

// resolveExternalListTargets pulls one provider's list/chart entries
// (DESIGN.md §11.11 - tmdb/imdb/tvdb/letterboxd, see their packages under
// internal/services) and matches each to a Plex ratingKey via its external
// Guid (Plex's own guid= filter only matches its internal plex://... guid,
// confirmed live against a real server - not external agent ids - so the
// whole library is fetched once with includeGuids=1 and matched
// client-side instead, see plex.Client.GetLibraryItemsWithGuids). Every
// provider normalizes into medialist.Item, whose GuidKey is already the
// exact string Plex's own Guid array would carry (e.g. "tmdb://11974"), so
// matching is a single map lookup regardless of provider. Entries with no
// match are persisted as "missing" (DESIGN.md §11.11's Missing Films/TV
// Shows list) rather than silently dropped, mirroring missing_tracks.go's
// unmatched-import-track convention but scoped to this collection instead
// of a playlist. Movie/show level only - a partially-owned TV series is
// not detected (would need per-episode season data, out of scope, see
// DESIGN.md §11.11).
func resolveExternalListTargets(sqlDB *sql.DB, client *plex.Client, coll *db.Collection) ([]string, error) {
	src := coll.ParsedExternalListSource()

	wantType := "movie"
	if coll.LibraryType == "show" {
		wantType = "tv"
	}

	var items []medialist.Item
	var err error
	switch src.Provider {
	case "imdb":
		if src.Mode == "top250" {
			items, err = imdb.GetTop250(context.Background())
		} else {
			items, err = imdb.GetListItems(context.Background(), src.ListID)
		}
	case "tvdb":
		apiKey, _, _ := db.GetAdminConfig(sqlDB, tvdbAdminConfigKey)
		if apiKey == "" {
			return nil, fmt.Errorf("no TVDb API key configured (Settings > Administration > TVDb)")
		}
		items, err = tvdb.NewClient(apiKey).GetListItems(src.ListID, wantType)
	case "letterboxd":
		items, err = letterboxd.GetListItems(context.Background(), src.ListID, src.Limit)
	case "trakt":
		apiKey, _, _ := db.GetAdminConfig(sqlDB, traktAdminConfigKey)
		if apiKey == "" {
			return nil, fmt.Errorf("no Trakt Client ID configured (Settings > Administration > Trakt)")
		}
		items, err = trakt.NewClient(apiKey).GetListItems(src.ListID, wantType)
	default: // "tmdb"
		apiKey, _, _ := db.GetAdminConfig(sqlDB, tmdbAdminConfigKey)
		if apiKey == "" {
			return nil, fmt.Errorf("no TMDb API key configured (Settings > Administration > TMDb)")
		}
		tc := tmdb.NewClient(apiKey)
		switch src.Mode {
		case "popular":
			items, err = tc.GetChart(wantType, "popular", src.Limit)
		case "top_rated":
			items, err = tc.GetChart(wantType, "top_rated", src.Limit)
		case "trending_daily":
			items, err = tc.GetTrending(wantType, "day", src.Limit)
		case "trending_weekly":
			items, err = tc.GetTrending(wantType, "week", src.Limit)
		case "collection":
			items, err = tc.GetCollectionMovies(src.ListID)
		default:
			items, err = tc.GetListItems(src.ListID)
		}
	}
	if err != nil {
		return nil, err
	}

	libItems, err := client.GetLibraryItemsWithGuids(coll.LibrarySectionID, itemTypeForLibraryType(coll.LibraryType))
	if err != nil {
		return nil, err
	}
	byGuid := make(map[string]string, len(libItems))
	for _, it := range libItems {
		for _, g := range it.Guid {
			byGuid[g.ID] = it.RatingKey
		}
	}

	var keys []string
	var missing []db.NewMissingCollectionItem
	for _, item := range items {
		if item.MediaType != "" && item.MediaType != wantType {
			continue
		}
		if rk, ok := byGuid[item.GuidKey]; ok {
			keys = append(keys, rk)
			continue
		}
		missing = append(missing, db.NewMissingCollectionItem{
			GuidKey: item.GuidKey, MediaType: item.MediaType, Title: item.Title, Year: item.Year,
		})
	}
	if err := db.ReplaceMissingCollectionItems(sqlDB, coll.UserID, coll.ID, missing); err != nil {
		slog.Error("failed to persist missing collection items", "error", err, "collectionId", coll.ID)
	}
	return keys, nil
}

// resolveOrCreateCollection returns the Plex collection ratingKey to write
// into, creating it (seeded with targetKeys) the first time this
// definition ever runs.
func resolveOrCreateCollection(client *plex.Client, coll *db.Collection, targetKeys []string, server *db.UserServer) (string, error) {
	if coll.PlexCollectionID.Valid && coll.PlexCollectionID.String != "" {
		return coll.PlexCollectionID.String, nil
	}
	itemURIs := make([]string, len(targetKeys))
	for i, key := range targetKeys {
		itemURIs[i] = plex.BuildTrackURI(server.ServerClientID, key)
	}
	created, err := client.CreateCollection(coll.LibrarySectionID, itemTypeForLibraryType(coll.LibraryType), coll.Name, itemURIs)
	if err != nil {
		return "", err
	}
	return created.RatingKey, nil
}

// diffCollectionMembership is reconcileCollectionMembership's pure diff
// logic, split out so it's unit-testable without a live Plex server
// (scheduler_test.go): toAdd is targetKeys not already present in current;
// toRemove is current members not in targetKeys, left empty when syncMode
// is "add_only".
func diffCollectionMembership(current []plex.Track, targetKeys []string, syncMode string) (toAdd, toRemove []string) {
	currentSet := make(map[string]bool, len(current))
	for _, t := range current {
		currentSet[t.RatingKey] = true
	}
	targetSet := make(map[string]bool, len(targetKeys))
	for _, key := range targetKeys {
		targetSet[key] = true
		if !currentSet[key] {
			toAdd = append(toAdd, key)
		}
	}
	if syncMode != "add_only" {
		for _, t := range current {
			if !targetSet[t.RatingKey] {
				toRemove = append(toRemove, t.RatingKey)
			}
		}
	}
	return toAdd, toRemove
}

// reconcileCollectionMembership diffs targetKeys against the collection's
// current members and applies the delta. Returns (added, removed) counts
// for the schedule_executions record.
func reconcileCollectionMembership(client *plex.Client, plexCollectionID string, targetKeys []string, syncMode, serverClientID string) (added, removed int, err error) {
	current, err := client.GetCollectionItems(plexCollectionID)
	if err != nil {
		return 0, 0, err
	}
	toAdd, toRemove := diffCollectionMembership(current, targetKeys, syncMode)

	if len(toAdd) > 0 {
		addURIs := make([]string, len(toAdd))
		for i, key := range toAdd {
			addURIs[i] = plex.BuildTrackURI(serverClientID, key)
		}
		if err := client.AddToCollection(plexCollectionID, addURIs); err != nil {
			return 0, 0, err
		}
	}
	for _, key := range toRemove {
		if err := client.RemoveFromCollection(plexCollectionID, key); err != nil {
			return len(toAdd), 0, err
		}
	}
	return len(toAdd), len(toRemove), nil
}

// generateMixByType dispatches to the mixes.Service method for mixType,
// using each mix's own documented defaults (ScheduleModal.tsx and this
// server's quick-mix form don't expose per-mix settings for scheduling, only
// which mix and what to name it).
func generateMixByType(svc *mixes.Service, mixType, serverURL, token, libraryID string) (mixes.MixResult, error) {
	switch mixType {
	case "weekly":
		return svc.GenerateWeeklyMix(serverURL, token, libraryID, mixes.WeeklyMixSettings{TopArtists: 10, TracksPerArtist: 5})
	case "daily":
		return svc.GenerateDailyMix(serverURL, token, libraryID, mixes.DailyMixSettings{RecentTracks: 20, RelatedTracks: 15, RediscoveryTracks: 15, RediscoveryDays: 90})
	case "timecapsule":
		return svc.GenerateTimeCapsule(serverURL, token, libraryID, mixes.TimeCapsuleSettings{TrackCount: 50, DaysAgo: 365, MaxPerArtist: 3})
	case "newmusic":
		return svc.GenerateNewMusicMix(serverURL, token, libraryID, mixes.NewMusicSettings{AlbumCount: 10, TracksPerAlbum: 3})
	case "deep-cuts", "deepcuts":
		return svc.GenerateDeepCutsMix(serverURL, token, libraryID, mixes.DeepCutsSettings{TrackCount: 50, MaxPlayCount: 5})
	case "workout":
		return svc.GenerateWorkoutMix(serverURL, token, libraryID, mixes.WorkoutMixSettings{TrackCount: 50, WarmupTracks: 5, PeakTracks: 30, CooldownTracks: 5})
	case "forgotten-favorites", "forgottenfavorites":
		return svc.GenerateForgottenFavoritesMix(serverURL, token, libraryID, mixes.ForgottenFavoritesSettings{TrackCount: 50, MinPlayCount: 10, NotPlayedInDays: 180})
	default:
		return mixes.MixResult{}, fmt.Errorf("unknown mix type: %s", mixType)
	}
}

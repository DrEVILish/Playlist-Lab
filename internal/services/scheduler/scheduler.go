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
	"time"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/importsvc"
	"github.com/drevilish/playlist-lab/internal/services/matching"
	"github.com/drevilish/playlist-lab/internal/services/mixes"
	"github.com/drevilish/playlist-lab/internal/services/plex"
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
	server, err := db.GetUserServer(d.DB, s.UserID)
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
	server, err := db.GetUserServer(d.DB, s.UserID)
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

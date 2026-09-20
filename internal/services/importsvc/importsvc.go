// Package importsvc ports services/import.ts: import a playlist from an
// external source (Deezer/YouTube/ListenBrainz - the only ones with a Go
// SourceAdapter so far, see cmd/server/main.go's registry wiring), match its
// tracks against the user's Plex library, and create the resulting Plex
// playlist. Named importsvc (not "import") since that's a Go keyword.
//
// Unlike import.ts + import-queue.ts, this does not keep its own
// DB-backed single-flight queue: internal/services/actionqueue.Queue
// already gives every background action (mixes, cross-import, and now
// plain imports) bounded concurrency plus per-user position visibility
// through the notification bell, so a second, import-only queue table
// would just be the same feature built twice. See internal/handlers/import.go
// for the actionqueue wiring.
package importsvc

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/matching"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

// Result mirrors import.ts's ImportResult.
type Result struct {
	PlaylistID   string
	PlaylistName string
	Source       string
	Matched      []matching.MatchedTrack
	Unmatched    []matching.MatchedTrack
	MatchedCount int
	TotalCount   int
	UsedCache    bool
	CoverURL     string
}

// Options mirrors import.ts's ImportOptions (minus serverUrl/plexToken,
// which the caller already needs to build the *plex.Client it passes in).
type Options struct {
	UserID     int64
	LibraryID  string
	CustomName string
}

// ImportPlaylist scrapes sourceIdentifier via the registered source
// adapter, falling back to a stale cached_playlists row if the scrape
// fails, then matches the result against the user's Plex library. Ports
// import.ts's importPlaylist() (the SSE-progress plumbing collapses to a
// plain progress callback, same shape as adapters.TargetAdapter.MatchTracks).
func ImportPlaylist(
	ctx context.Context,
	registry *adapters.Registry,
	sqlDB *sql.DB,
	client *plex.Client,
	source, sourceIdentifier string,
	opts Options,
	progress func(phase string, current, total int),
	isCancelled func() bool,
) (*Result, error) {
	src, ok := registry.GetSource(source)
	if !ok {
		return nil, fmt.Errorf("unsupported import source: %s", source)
	}

	cached, err := db.GetCachedPlaylist(sqlDB, source, sourceIdentifier)
	if err != nil {
		slog.Warn("[Import] Failed to read cache", "source", source, "error", err)
	}

	if progress != nil {
		progress("scraping", 0, 0)
	}

	var (
		playlistName, coverURL string
		tracks                 []adapters.TrackInfo
		usedCache              bool
	)

	info, fetched, fetchErr := src.FetchTracks(ctx, sourceIdentifier, opts.UserID)
	if fetchErr != nil {
		if cached == nil {
			return nil, fetchErr
		}
		slog.Warn("[Import] Scrape failed, using stale cache", "source", source, "sourceIdentifier", sourceIdentifier, "error", fetchErr)
		playlistName, coverURL = cached.Name, cached.CoverURL.String
		usedCache = true
		tracks = make([]adapters.TrackInfo, len(cached.Tracks))
		for i, t := range cached.Tracks {
			tracks[i] = adapters.TrackInfo{Title: t.Title, Artist: t.Artist, Album: t.Album}
		}
	} else {
		playlistName, coverURL = info.Name, info.CoverURL
		tracks = fetched
		cacheTracks := make([]db.CachedTrack, len(tracks))
		for i, t := range tracks {
			cacheTracks[i] = db.CachedTrack{Title: t.Title, Artist: t.Artist, Album: t.Album}
		}
		if err := db.SaveCachedPlaylist(sqlDB, source, sourceIdentifier, playlistName, "", cacheTracks, coverURL); err != nil {
			slog.Warn("[Import] Failed to save cache", "source", source, "error", err)
		}
	}

	playlistID := sourceIdentifier
	if info.ID != "" {
		playlistID = info.ID
	}

	return matchAndBuildResult(sqlDB, client, source, playlistID, playlistName, coverURL, usedCache, tracks, opts, progress, isCancelled)
}

// ImportTracksFromFile matches an already-parsed track list (see
// internal/services/fileimport) against the user's Plex library and builds
// a Result, the same as ImportPlaylist minus the scrape/cache step - a file
// upload has no source adapter to fetch from or cache against. Ports the
// 'file' branch of import.ts's scrapePlaylist() feeding straight into the
// same importPlaylist() matching logic the other sources share.
func ImportTracksFromFile(
	sqlDB *sql.DB,
	client *plex.Client,
	playlistName string,
	tracks []adapters.TrackInfo,
	opts Options,
	progress func(phase string, current, total int),
	isCancelled func() bool,
) (*Result, error) {
	if progress != nil {
		progress("scraping", len(tracks), len(tracks))
	}
	return matchAndBuildResult(sqlDB, client, "file", "file-"+playlistName, playlistName, "", false, tracks, opts, progress, isCancelled)
}

// matchAndBuildResult is the shared tail of ImportPlaylist/
// ImportTracksFromFile: load matching settings + remembered manual matches,
// run matching.MatchPlaylist, and assemble the Result.
func matchAndBuildResult(
	sqlDB *sql.DB,
	client *plex.Client,
	source, playlistID, playlistName, coverURL string,
	usedCache bool,
	tracks []adapters.TrackInfo,
	opts Options,
	progress func(phase string, current, total int),
	isCancelled func() bool,
) (*Result, error) {
	if progress != nil {
		progress("matching", 0, len(tracks))
	}

	settingsJSON, err := db.GetMatchingSettingsJSON(sqlDB, opts.UserID)
	if err != nil {
		slog.Warn("[Import] Failed to load matching settings, using defaults", "userId", opts.UserID, "error", err)
	}
	settings := matching.SettingsFromJSON(settingsJSON)

	manualMatches, err := db.GetUserManualMatches(sqlDB, opts.UserID)
	if err != nil {
		slog.Warn("[Import] Failed to load manual matches", "userId", opts.UserID, "error", err)
	}
	remembered := matching.BuildRememberedMatchMap(toRememberedMatches(manualMatches))

	mtracks := make([]matching.Track, len(tracks))
	for i, t := range tracks {
		mtracks[i] = matching.Track{Title: t.Title, Artist: t.Artist, Album: t.Album}
	}

	matchProgress := func(current, total int) {
		if progress != nil {
			progress("matching", current, total)
		}
	}
	matchedTracks, err := matching.MatchPlaylist(mtracks, client, opts.LibraryID, settings, matchProgress, isCancelled, remembered)
	if err != nil {
		return nil, err
	}
	matching.RememberMatches(sqlDB, opts.UserID, matchedTracks)

	var matched, unmatched []matching.MatchedTrack
	for _, t := range matchedTracks {
		if t.Matched {
			matched = append(matched, t)
		} else {
			unmatched = append(unmatched, t)
		}
	}

	finalName := opts.CustomName
	if finalName == "" {
		finalName = playlistName
	}

	return &Result{
		PlaylistID:   playlistID,
		PlaylistName: finalName,
		Source:       source,
		Matched:      matched,
		Unmatched:    unmatched,
		MatchedCount: len(matched),
		TotalCount:   len(matchedTracks),
		UsedCache:    usedCache,
		CoverURL:     coverURL,
	}, nil
}

func toRememberedMatches(rows []db.ManualMatch) []matching.RememberedMatch {
	out := make([]matching.RememberedMatch, len(rows))
	for i, r := range rows {
		out[i] = matching.RememberedMatch{Title: r.Title, Artist: r.Artist, Album: r.Album.String, PlexRatingKey: r.PlexRatingKey}
	}
	return out
}

// FinalizeOpts mirrors import.ts's FinalizeImportOpts, minus
// overwriteExisting/keepExistingCover which the fire-and-forget import path
// (the only one ported so far - see the package doc) never sets; a manual
// "reimport this playlist" action wanting them can add those fields back
// when it's built.
type FinalizeOpts struct {
	PlaylistName string
}

// FinalizeImportResult creates the Plex playlist from a completed
// ImportPlaylist's matched tracks, saves the playlist row, and stores any
// unmatched tracks as missing tracks. Ports import.ts's
// finalizeImportResult (the overwrite-existing-playlist branch is not
// ported - see FinalizeOpts).
func FinalizeImportResult(
	sqlDB *sql.DB,
	client *plex.Client,
	source, sourceIdentifier string,
	userID int64,
	serverClientID, libraryID string,
	result *Result,
	opts FinalizeOpts,
) (dbPlaylistID int64, plexRatingKey string, err error) {
	playlistName := opts.PlaylistName
	if playlistName == "" {
		playlistName = result.PlaylistName
	}

	matched := matching.DedupeByPlexRatingKey(result.Matched)
	trackURIs := make([]string, 0, len(matched))
	for _, t := range matched {
		if t.Matched && t.PlexRatingKey != "" {
			trackURIs = append(trackURIs, plex.BuildTrackURI(serverClientID, t.PlexRatingKey))
		}
	}
	if len(trackURIs) == 0 {
		return 0, "", fmt.Errorf("no tracks matched in your Plex library - nothing to add to a playlist")
	}

	libraryURI := client.BuildLibraryURI(libraryID, serverClientID)
	newPlaylist, err := client.CreatePlaylist(playlistName, libraryURI, trackURIs)
	if err != nil {
		return 0, "", err
	}

	if result.CoverURL != "" {
		if err := client.UploadPlaylistPoster(newPlaylist.RatingKey, result.CoverURL); err != nil {
			slog.Warn("[Import] Failed to upload cover art", "playlistName", playlistName, "error", err)
		}
	}

	dbPlaylist, err := db.CreatePlaylistRow(sqlDB, userID, newPlaylist.RatingKey, playlistName, source, sourceIdentifier)
	if err != nil {
		return 0, "", err
	}

	if len(result.Unmatched) > 0 {
		rows := make([]db.NewMissingTrack, len(result.Unmatched))
		for i, t := range result.Unmatched {
			rows[i] = db.NewMissingTrack{Title: t.Title, Artist: t.Artist, Album: t.Album, Position: i, Source: source}
		}
		if err := db.AddMissingTracks(sqlDB, userID, dbPlaylist.ID, rows); err != nil {
			slog.Warn("[Import] Failed to store missing tracks", "playlistId", dbPlaylist.ID, "error", err)
		}
	}

	return dbPlaylist.ID, newPlaylist.RatingKey, nil
}

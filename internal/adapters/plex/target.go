// Package plex ports adapters/plex-target.ts: matching against your own
// Plex library, delegating the real work to internal/services/matching
// (the matching engine) and internal/services/plex (the Plex HTTP client) -
// this file is just the TargetAdapter-shaped glue between them plus the
// remember-a-match learning step.
package plex

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/matching"
	plexsvc "github.com/drevilish/playlist-lab/internal/services/plex"
)

type Target struct {
	DB *sql.DB
}

func NewTarget(sqlDB *sql.DB) *Target {
	return &Target{DB: sqlDB}
}

func (t *Target) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: "plex", Name: "Plex", Icon: "plex"}
}

func (t *Target) IsConfigured() bool { return true }

func toMatchResult(m matching.MatchedTrack) adapters.MatchResult {
	confidence := m.Score
	if confidence == 0 && m.Matched {
		confidence = 80
	}
	return adapters.MatchResult{
		SourceTrack:   adapters.TrackInfo{Title: m.Title, Artist: m.Artist, Album: m.Album},
		TargetTrackID: m.PlexRatingKey, TargetTitle: m.PlexTitle, TargetArtist: m.PlexArtist,
		TargetAlbum: m.PlexAlbum, Confidence: confidence, Matched: m.Matched,
	}
}

// SearchCatalog searches the user's own Plex library, the same
// artist-first search the matching engine uses, capped to the top 10
// results for a manual-search UI (unlike matchTracks, this returns
// unfiltered, ungated results - a person can judge a candidate a machine
// shouldn't auto-accept).
func (t *Target) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]adapters.MatchResult, error) {
	userServer, err := db.GetUserMusicServer(t.DB, userID)
	if err != nil {
		return nil, err
	}
	if userServer == nil {
		return nil, fmt.Errorf("no Plex server configured for this user")
	}
	user, err := db.GetUserByID(t.DB, userID)
	if err != nil {
		return nil, err
	}

	client := plexsvc.NewClient(userServer.ServerURL, user.PlexToken, "playlist-lab-server", "Playlist Lab")
	tracks, err := client.SearchTrack(query, "", "", "")
	if err != nil {
		return nil, err
	}
	if len(tracks) > 10 {
		tracks = tracks[:10]
	}

	results := make([]adapters.MatchResult, len(tracks))
	for i, track := range tracks {
		results[i] = adapters.MatchResult{
			SourceTrack:   adapters.TrackInfo{Title: query},
			TargetTrackID: track.RatingKey, TargetTitle: track.Title,
			TargetArtist: track.DisplayArtist(), TargetAlbum: track.ParentTitle,
			Confidence: 100, Matched: true,
		}
	}
	return results, nil
}

// MatchTracks delegates to matching.MatchPlaylist (the full tiered
// search-and-score engine), then records every resolved match so a later
// rerun of the same cross-import reuses the decision - the same learning
// step an ordinary import already performs, since a cross-import's matches
// are real playlist contents too.
func (t *Target) MatchTracks(ctx context.Context, tracks []adapters.TrackInfo, cfg adapters.TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]adapters.MatchResult, error) {
	if cfg.ServerURL == "" || cfg.PlexToken == "" {
		return nil, fmt.Errorf("Plex target requires serverUrl and plexToken in targetConfig")
	}

	settings := matching.DefaultSettings()
	client := plexsvc.NewClient(cfg.ServerURL, cfg.PlexToken, "playlist-lab-server", "Playlist Lab")

	matchTracks := make([]matching.Track, len(tracks))
	for i, tr := range tracks {
		matchTracks[i] = matching.Track{Title: tr.Title, Artist: tr.Artist, Album: tr.Album}
	}

	var remembered map[string]string
	if manual, err := db.GetUserManualMatches(t.DB, userID); err == nil {
		rm := make([]matching.RememberedMatch, len(manual))
		for i, m := range manual {
			rm[i] = matching.RememberedMatch{Title: m.Title, Artist: m.Artist, Album: m.Album.String, PlexRatingKey: m.PlexRatingKey}
		}
		remembered = matching.BuildRememberedMatchMap(rm)
	}

	matched, err := matching.MatchPlaylist(matchTracks, client, cfg.LibraryID, settings, progress, isCancelled, remembered)
	if err != nil {
		return nil, err
	}

	rememberMatches(t.DB, userID, matched)

	results := make([]adapters.MatchResult, len(matched))
	for i, m := range matched {
		results[i] = toMatchResult(m)
	}
	return results, nil
}

// rememberMatches ports matching.ts's rememberMatches(): every resolved
// match is written back to manual_matches so future runs reuse the
// decision. Errors are swallowed - learning is an optimization for next
// time, never worth failing an already-completed match over.
func rememberMatches(sqlDB *sql.DB, userID int64, matched []matching.MatchedTrack) {
	for _, m := range matched {
		if !m.Matched || m.PlexRatingKey == "" {
			continue
		}
		_ = db.RecordManualMatch(sqlDB, userID, m.Title, m.Artist, m.Album, m.PlexRatingKey)
	}
}

// CreatePlaylist creates the matched tracks as a new Plex playlist,
// appending " (Copy)" when the source and target are the same server and
// library (per TargetConfig's isSameSourceAndTarget convention - not yet a
// field on TargetConfig since no caller sets it before cross-import lands).
func (t *Target) CreatePlaylist(ctx context.Context, name string, matches []adapters.MatchResult, cfg adapters.TargetConfig, userID int64) (string, string, int, error) {
	if cfg.ServerURL == "" || cfg.PlexToken == "" {
		return "", "", 0, fmt.Errorf("Plex target requires serverUrl and plexToken in targetConfig")
	}

	matchedCount := 0
	for _, m := range matches {
		if m.Matched && m.TargetTrackID != "" {
			matchedCount++
		}
	}
	if matchedCount == 0 {
		return "", "", 0, fmt.Errorf("no matched tracks to create playlist with")
	}

	client := plexsvc.NewClient(cfg.ServerURL, cfg.PlexToken, "playlist-lab-server", "Playlist Lab")
	machineID, err := client.GetMachineIdentifier()
	if err != nil {
		return "", "", 0, err
	}
	libraryID := cfg.LibraryID
	if libraryID == "" {
		libraryID = "1"
	}
	libraryURI := client.BuildLibraryURI(libraryID, machineID)

	trackURIs := make([]string, 0, matchedCount)
	for _, m := range matches {
		if m.Matched && m.TargetTrackID != "" {
			trackURIs = append(trackURIs, client.BuildTrackURI(m.TargetTrackID, machineID))
		}
	}

	playlist, err := client.CreatePlaylist(name, libraryURI, trackURIs)
	if err != nil {
		return "", "", 0, err
	}
	return playlist.RatingKey, playlist.Title, len(trackURIs), nil
}

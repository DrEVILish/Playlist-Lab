// insert.go ports matching.ts's insertMatchedTrackIntoPlaylist and
// rememberMatches - shared by every caller that resolves a missing track to
// a real Plex track (the manual retry batch, deemix/Lidarr reconciliation,
// and manual rematch/replace-similar), so "add it to the playlist, moving
// it back to roughly where it was" behaves identically everywhere.
package matching

import (
	"database/sql"
	"log/slog"
	"strconv"
	"strings"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

// PlaylistTarget is the subset of a user's server config
// insertMatchedTrackIntoPlaylist needs to build Plex playlist/track URIs -
// mirrors matching.ts's inline { server_client_id, library_id } shape.
type PlaylistTarget struct {
	ServerClientID string
	LibraryID      string
}

// InsertMatchedTrackIntoPlaylist adds a resolved Plex track to the playlist
// a missing_tracks row belongs to (creating the Plex playlist first if it
// only exists as a Go pending-placeholder row - see missing.ts's /save),
// restoring its original position via after_track_key when known, then
// clears the missing_tracks row. Returns false (not an error) on any
// failure, matching the Node version's log-and-continue behaviour: the
// missing_tracks row is left alone so Retry/Rematch can still be used.
func InsertMatchedTrackIntoPlaylist(sqlDB *sql.DB, client *plex.Client, target PlaylistTarget, track db.MissingTrack, plexRatingKey string) bool {
	playlist, err := db.GetPlaylistByID(sqlDB, track.PlaylistID)
	if err != nil || playlist == nil {
		slog.Warn("[Matching] Playlist not found for missing track", "playlistId", track.PlaylistID)
		return false
	}

	trackURI := "server://" + target.ServerClientID + "/com.plexapp.plugins.library/library/metadata/" + plexRatingKey
	libraryURI := "server://" + target.ServerClientID + "/com.plexapp.plugins.library/library/sections/" + target.LibraryID

	plexPlaylistID := playlist.PlexPlaylistID
	if strings.HasPrefix(plexPlaylistID, "pending-") {
		newPlaylist, err := client.CreatePlaylist(playlist.Name, libraryURI, []string{trackURI})
		if err != nil {
			slog.Error("[Matching] Failed to create Plex playlist for missing track", "error", err, "trackId", track.ID)
			return false
		}
		if err := db.UpdatePlaylistPlexID(sqlDB, playlist.ID, newPlaylist.RatingKey); err != nil {
			slog.Error("[Matching] Failed to persist new Plex playlist id", "error", err, "trackId", track.ID)
			return false
		}
	} else {
		if err := client.AddToPlaylist(plexPlaylistID, []string{trackURI}); err != nil {
			if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "404") {
				slog.Error("[Matching] Failed to add matched track to playlist", "error", err, "trackId", track.ID, "playlistId", playlist.ID)
				return false
			}
			slog.Warn("[Matching] Plex playlist not found, creating new one", "playlistId", playlist.ID, "plexPlaylistId", plexPlaylistID)
			newPlaylist, err := client.CreatePlaylist(playlist.Name, libraryURI, []string{trackURI})
			if err != nil {
				slog.Error("[Matching] Failed to recreate Plex playlist for missing track", "error", err, "trackId", track.ID)
				return false
			}
			if err := db.UpdatePlaylistPlexID(sqlDB, playlist.ID, newPlaylist.RatingKey); err != nil {
				slog.Error("[Matching] Failed to persist recreated Plex playlist id", "error", err, "trackId", track.ID)
				return false
			}
		} else if track.AfterTrackKey.Valid && track.AfterTrackKey.String != "" {
			// Non-fatal: the track is still in the playlist, just not
			// necessarily at its original position.
			if playlistTracks, err := client.GetPlaylistTracks(plexPlaylistID); err == nil {
				for _, t := range playlistTracks {
					if t.RatingKey == plexRatingKey && t.PlaylistItemID != 0 {
						if err := client.MovePlaylistItem(plexPlaylistID, strconv.Itoa(t.PlaylistItemID), track.AfterTrackKey.String); err != nil {
							slog.Warn("[Matching] Failed to move track to original position", "error", err, "trackTitle", track.Title, "playlistId", plexPlaylistID)
						}
						break
					}
				}
			}
		}
	}

	if err := db.RemoveMissingTrack(sqlDB, track.ID); err != nil {
		slog.Error("[Matching] Failed to clear resolved missing track", "error", err, "trackId", track.ID)
		return false
	}
	return true
}

// RememberMatches records every matched track from a matching run as a
// manual match, so a repeated retry/import of the same source track reuses
// the resolved Plex track instead of re-running the fuzzy search. Ports
// matching.ts's rememberMatches.
func RememberMatches(sqlDB *sql.DB, userID int64, matches []MatchedTrack) {
	for _, m := range matches {
		if m.Matched && m.PlexRatingKey != "" {
			if err := db.RecordManualMatch(sqlDB, userID, m.Title, m.Artist, m.Album, m.PlexRatingKey); err != nil {
				slog.Warn("[Matching] Failed to remember match", "error", err, "title", m.Title, "artist", m.Artist)
			}
		}
	}
}

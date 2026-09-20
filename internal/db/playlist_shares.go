package db

import (
	"database/sql"
	"time"
)

// RecordPlaylistShare notes that ownerUserID shared one of their playlists
// with sharedWithUserID, so it shows up in the recipient's "Shared With Me"
// list. Ports database.ts's recordPlaylistShare() - INSERT OR REPLACE since
// (playlist_id, shared_with_user_id) is unique, so re-sharing just refreshes
// the name/timestamp rather than erroring.
func RecordPlaylistShare(sqlDB *sql.DB, playlistID, ownerUserID, sharedWithUserID int64, plexPlaylistID, playlistName string) error {
	_, err := sqlDB.Exec(
		`INSERT OR REPLACE INTO playlist_shares
		 (playlist_id, owner_user_id, shared_with_user_id, plex_playlist_id, playlist_name, shared_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		playlistID, ownerUserID, sharedWithUserID, plexPlaylistID, playlistName, time.Now().UnixMilli(),
	)
	return err
}

// SharedPlaylist is one row of a user's "Shared With Me" list.
type SharedPlaylist struct {
	ID               int64
	PlaylistName     string
	SharedByUsername string
	PlexPlaylistID   string
	SharedAt         int64
}

// GetPlaylistsSharedWithUser ports database.ts's getPlaylistsSharedWithUser().
func GetPlaylistsSharedWithUser(sqlDB *sql.DB, userID int64) ([]SharedPlaylist, error) {
	rows, err := sqlDB.Query(
		`SELECT ps.id, ps.playlist_name, ps.plex_playlist_id, u.plex_username, ps.shared_at
		 FROM playlist_shares ps JOIN users u ON ps.owner_user_id = u.id
		 WHERE ps.shared_with_user_id = ? ORDER BY ps.shared_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SharedPlaylist
	for rows.Next() {
		var s SharedPlaylist
		if err := rows.Scan(&s.ID, &s.PlaylistName, &s.PlexPlaylistID, &s.SharedByUsername, &s.SharedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

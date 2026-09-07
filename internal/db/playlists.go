package db

import (
	"database/sql"
	"time"
)

type Playlist struct {
	ID             int64
	UserID         int64
	PlexPlaylistID string
	Name           string
	Source         string
	SourceURL      sql.NullString
	CreatedAt      int64
	UpdatedAt      int64
}

func CreatePlaylistRow(sqlDB *sql.DB, userID int64, plexPlaylistID, name, source, sourceURL string) (*Playlist, error) {
	now := time.Now().Unix()
	res, err := sqlDB.Exec(
		`INSERT INTO playlists (user_id, plex_playlist_id, name, source, source_url, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, plexPlaylistID, name, source, nullIfEmpty(sourceURL), now, now,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetPlaylistByID(sqlDB, id)
}

func GetUserPlaylists(sqlDB *sql.DB, userID int64) ([]Playlist, error) {
	rows, err := sqlDB.Query("SELECT id, user_id, plex_playlist_id, name, source, source_url, created_at, updated_at FROM playlists WHERE user_id = ? ORDER BY created_at DESC", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Playlist
	for rows.Next() {
		var p Playlist
		if err := rows.Scan(&p.ID, &p.UserID, &p.PlexPlaylistID, &p.Name, &p.Source, &p.SourceURL, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func GetPlaylistByID(sqlDB *sql.DB, id int64) (*Playlist, error) {
	return scanPlaylist(sqlDB.QueryRow(
		"SELECT id, user_id, plex_playlist_id, name, source, source_url, created_at, updated_at FROM playlists WHERE id = ?", id,
	))
}

func GetPlaylistByPlexID(sqlDB *sql.DB, userID int64, plexPlaylistID string) (*Playlist, error) {
	return scanPlaylist(sqlDB.QueryRow(
		"SELECT id, user_id, plex_playlist_id, name, source, source_url, created_at, updated_at FROM playlists WHERE user_id = ? AND plex_playlist_id = ?", userID, plexPlaylistID,
	))
}

func scanPlaylist(row *sql.Row) (*Playlist, error) {
	var p Playlist
	err := row.Scan(&p.ID, &p.UserID, &p.PlexPlaylistID, &p.Name, &p.Source, &p.SourceURL, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdatePlaylistPlexID sets the real Plex playlist id once one has been
// created for a row that started out as a "pending-..." placeholder (see
// matching.InsertMatchedTrackIntoPlaylist).
func UpdatePlaylistPlexID(sqlDB *sql.DB, id int64, plexPlaylistID string) error {
	_, err := sqlDB.Exec("UPDATE playlists SET plex_playlist_id = ?, updated_at = ? WHERE id = ?", plexPlaylistID, time.Now().Unix(), id)
	return err
}

func TouchPlaylist(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("UPDATE playlists SET updated_at = ? WHERE id = ?", time.Now().Unix(), id)
	return err
}

// RenamePlaylistRow updates this app's own record of a tracked playlist's
// name after the caller has already renamed it in Plex itself - the two
// must be kept in sync or this app's name (shown on the home page) drifts
// from what every real Plex client displays.
func RenamePlaylistRow(sqlDB *sql.DB, id int64, name string) error {
	_, err := sqlDB.Exec("UPDATE playlists SET name = ?, updated_at = ? WHERE id = ?", name, time.Now().Unix(), id)
	return err
}

func DeletePlaylistRow(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("DELETE FROM playlists WHERE id = ?", id)
	return err
}

// GetPlaylistCount is the total-playlists-across-all-users figure for the
// admin Stats tab (mirrors admin.ts's db.getPlaylistCount()).
func GetPlaylistCount(sqlDB *sql.DB) (int, error) {
	var count int
	err := sqlDB.QueryRow("SELECT COUNT(*) FROM playlists").Scan(&count)
	return count, err
}

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

func TouchPlaylist(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("UPDATE playlists SET updated_at = ? WHERE id = ?", time.Now().Unix(), id)
	return err
}

func DeletePlaylistRow(sqlDB *sql.DB, id int64) error {
	_, err := sqlDB.Exec("DELETE FROM playlists WHERE id = ?", id)
	return err
}

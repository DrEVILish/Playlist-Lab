// saved_spotify_users.go: a plain per-user CRUD table for quick-access
// Spotify usernames on the Import page, same shape/conventions as
// mix_templates.go (no update method needed here - a saved username is
// add-or-remove only, never edited in place).
package db

import (
	"database/sql"
	"time"
)

type SavedSpotifyUser struct {
	ID            int64
	UserID        int64
	SpotifyUserID string
	DisplayName   string
	AddedAt       int64
}

const savedSpotifyUserColumns = "id, user_id, spotify_user_id, display_name, added_at"

func GetSavedSpotifyUsers(sqlDB *sql.DB, userID int64) ([]SavedSpotifyUser, error) {
	rows, err := sqlDB.Query(
		"SELECT "+savedSpotifyUserColumns+" FROM saved_spotify_users WHERE user_id = ? ORDER BY added_at DESC", userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SavedSpotifyUser
	for rows.Next() {
		var u SavedSpotifyUser
		if err := rows.Scan(&u.ID, &u.UserID, &u.SpotifyUserID, &u.DisplayName, &u.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func AddSavedSpotifyUser(sqlDB *sql.DB, userID int64, spotifyUserID, displayName string) (*SavedSpotifyUser, error) {
	now := time.Now().Unix()
	res, err := sqlDB.Exec(
		`INSERT INTO saved_spotify_users (user_id, spotify_user_id, display_name, added_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id, spotify_user_id) DO UPDATE SET display_name = excluded.display_name`,
		userID, spotifyUserID, displayName, now,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if id == 0 {
		// Conflict path (UPSERT) doesn't report the existing row's id via
		// LastInsertId - look it up instead.
		row := sqlDB.QueryRow("SELECT "+savedSpotifyUserColumns+" FROM saved_spotify_users WHERE user_id = ? AND spotify_user_id = ?", userID, spotifyUserID)
		var u SavedSpotifyUser
		if err := row.Scan(&u.ID, &u.UserID, &u.SpotifyUserID, &u.DisplayName, &u.AddedAt); err != nil {
			return nil, err
		}
		return &u, nil
	}
	return &SavedSpotifyUser{ID: id, UserID: userID, SpotifyUserID: spotifyUserID, DisplayName: displayName, AddedAt: now}, nil
}

func DeleteSavedSpotifyUser(sqlDB *sql.DB, userID, id int64) error {
	_, err := sqlDB.Exec("DELETE FROM saved_spotify_users WHERE id = ? AND user_id = ?", id, userID)
	return err
}

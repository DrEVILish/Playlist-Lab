package db

import (
	"database/sql"
	"errors"
	"time"
)

// User mirrors the subset of the users table Phase 0 auth needs. Later
// phases will grow this alongside a proper query package per table, but
// hand-rolling just what's used keeps this from becoming a premature ORM.
type User struct {
	ID           int64
	PlexUserID   string
	PlexUsername string
	PlexToken    string
	PlexThumb    sql.NullString
	IsEnabled    bool
}

var ErrUserNotFound = errors.New("user not found")

func GetUserByID(sqlDB *sql.DB, id int64) (*User, error) {
	return scanUser(sqlDB.QueryRow(
		"SELECT id, plex_user_id, plex_username, plex_token, plex_thumb, is_enabled FROM users WHERE id = ?", id,
	))
}

func GetUserByPlexID(sqlDB *sql.DB, plexUserID string) (*User, error) {
	return scanUser(sqlDB.QueryRow(
		"SELECT id, plex_user_id, plex_username, plex_token, plex_thumb, is_enabled FROM users WHERE plex_user_id = ?", plexUserID,
	))
}

func GetFirstUser(sqlDB *sql.DB) (*User, error) {
	return scanUser(sqlDB.QueryRow(
		"SELECT id, plex_user_id, plex_username, plex_token, plex_thumb, is_enabled FROM users ORDER BY id ASC LIMIT 1",
	))
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	var isEnabled int
	err := row.Scan(&u.ID, &u.PlexUserID, &u.PlexUsername, &u.PlexToken, &u.PlexThumb, &isEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	u.IsEnabled = isEnabled != 0
	return &u, nil
}

func CreateUser(sqlDB *sql.DB, plexUserID, username, token string, thumb string) (*User, error) {
	now := time.Now().UnixMilli()
	res, err := sqlDB.Exec(
		`INSERT INTO users (plex_user_id, plex_username, plex_token, plex_thumb, created_at, last_login, is_enabled)
		 VALUES (?, ?, ?, ?, ?, ?, 1)`,
		plexUserID, username, token, thumb, now, now,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetUserByID(sqlDB, id)
}

func UpdateUserLogin(sqlDB *sql.DB, userID int64) error {
	_, err := sqlDB.Exec("UPDATE users SET last_login = ? WHERE id = ?", time.Now().UnixMilli(), userID)
	return err
}

func UpdateUserToken(sqlDB *sql.DB, userID int64, token string) error {
	_, err := sqlDB.Exec("UPDATE users SET plex_token = ? WHERE id = ?", token, userID)
	return err
}

func UpdateUserProfile(sqlDB *sql.DB, userID int64, username, thumb string) error {
	_, err := sqlDB.Exec("UPDATE users SET plex_username = ?, plex_thumb = ? WHERE id = ?", username, thumb, userID)
	return err
}

func GetUserCount(sqlDB *sql.DB) (int, error) {
	var count int
	err := sqlDB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	return count, err
}

func IsAdmin(sqlDB *sql.DB, userID int64) (bool, error) {
	var exists int
	err := sqlDB.QueryRow("SELECT 1 FROM admin_users WHERE user_id = ?", userID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func AddAdmin(sqlDB *sql.DB, userID int64) error {
	_, err := sqlDB.Exec("INSERT OR IGNORE INTO admin_users (user_id) VALUES (?)", userID)
	return err
}

func EnableUser(sqlDB *sql.DB, userID int64) error {
	_, err := sqlDB.Exec("UPDATE users SET is_enabled = 1 WHERE id = ?", userID)
	return err
}

func DisableUser(sqlDB *sql.DB, userID int64) error {
	_, err := sqlDB.Exec("UPDATE users SET is_enabled = 0 WHERE id = ?", userID)
	return err
}

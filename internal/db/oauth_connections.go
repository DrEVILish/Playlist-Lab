package db

import (
	"database/sql"
	"time"
)

// OAuthConnection is one row of the shared oauth_connections table, used by
// every OAuth target adapter except Spotify (which predates this table and
// still stores its tokens directly on the users row for backward
// compatibility - see spotify.go).
type OAuthConnection struct {
	AccessToken    string // encrypted
	RefreshToken   sql.NullString
	TokenExpiresAt sql.NullInt64
	Scope          sql.NullString
}

func GetOAuthConnection(sqlDB *sql.DB, userID int64, service string) (*OAuthConnection, error) {
	var c OAuthConnection
	err := sqlDB.QueryRow(
		"SELECT access_token, refresh_token, token_expires_at, scope FROM oauth_connections WHERE user_id = ? AND service = ?",
		userID, service,
	).Scan(&c.AccessToken, &c.RefreshToken, &c.TokenExpiresAt, &c.Scope)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func SaveOAuthConnection(sqlDB *sql.DB, userID int64, service, encryptedAccessToken, encryptedRefreshToken string, expiresAt *time.Time) error {
	now := time.Now().UnixMilli()
	var expiresAtMillis any
	if expiresAt != nil {
		expiresAtMillis = expiresAt.UnixMilli()
	}
	_, err := sqlDB.Exec(
		`INSERT INTO oauth_connections (user_id, service, access_token, refresh_token, token_expires_at, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, service) DO UPDATE SET
		   access_token = excluded.access_token,
		   refresh_token = excluded.refresh_token,
		   token_expires_at = excluded.token_expires_at,
		   expires_at = excluded.expires_at,
		   updated_at = excluded.updated_at`,
		userID, service, encryptedAccessToken, nullIfEmpty(encryptedRefreshToken), expiresAtMillis, expiresAtMillis, now, now,
	)
	return err
}

// SaveOAuthConnectionWithScope additionally sets the scope column (repurposed
// by some adapters - e.g. Tidal stores its own numeric user ID there, since
// the shared oauth_connections table has no adapter-specific columns) and,
// on conflict, keeps the existing refresh token when the new one is empty
// (a refresh-token grant response doesn't always include a fresh one).
func SaveOAuthConnectionWithScope(sqlDB *sql.DB, userID int64, service, encryptedAccessToken, encryptedRefreshToken string, expiresAt *time.Time, scope string) error {
	now := time.Now().UnixMilli()
	var expiresAtMillis any
	if expiresAt != nil {
		expiresAtMillis = expiresAt.UnixMilli()
	}
	_, err := sqlDB.Exec(
		`INSERT INTO oauth_connections (user_id, service, access_token, refresh_token, token_expires_at, scope, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, service) DO UPDATE SET
		   access_token = excluded.access_token,
		   refresh_token = COALESCE(excluded.refresh_token, oauth_connections.refresh_token),
		   token_expires_at = excluded.token_expires_at,
		   scope = excluded.scope,
		   updated_at = excluded.updated_at`,
		userID, service, encryptedAccessToken, nullIfEmpty(encryptedRefreshToken), expiresAtMillis, scope, now, now,
	)
	return err
}

// UpdateOAuthAccessToken updates only the access token + expiry, for a
// refresh-token grant that doesn't touch the stored refresh token itself.
func UpdateOAuthAccessToken(sqlDB *sql.DB, userID int64, service, encryptedAccessToken string, expiresAt time.Time) error {
	_, err := sqlDB.Exec(
		"UPDATE oauth_connections SET access_token = ?, token_expires_at = ?, updated_at = ? WHERE user_id = ? AND service = ?",
		encryptedAccessToken, expiresAt.UnixMilli(), time.Now().UnixMilli(), userID, service,
	)
	return err
}

func DeleteOAuthConnection(sqlDB *sql.DB, userID int64, service string) error {
	_, err := sqlDB.Exec("DELETE FROM oauth_connections WHERE user_id = ? AND service = ?", userID, service)
	return err
}

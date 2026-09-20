package db

import (
	"database/sql"
	"time"
)

// SpotifyCredentials holds a user's own Spotify app registration (Client ID
// + Secret), which every Spotify OAuth flow in this app is keyed off - each
// user brings their own Spotify app rather than the server sharing one.
type SpotifyCredentials struct {
	ClientID     string // encrypted
	ClientSecret string // encrypted
}

func GetSpotifyCredentials(sqlDB *sql.DB, userID int64) (*SpotifyCredentials, error) {
	var clientID, clientSecret sql.NullString
	err := sqlDB.QueryRow("SELECT spotify_client_id, spotify_client_secret FROM users WHERE id = ?", userID).
		Scan(&clientID, &clientSecret)
	if err == sql.ErrNoRows || !clientID.Valid || !clientSecret.Valid {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &SpotifyCredentials{ClientID: clientID.String, ClientSecret: clientSecret.String}, nil
}

// SaveSpotifyCredentials stores a user's own Spotify app Client ID/Secret
// (both already encrypted by the caller). Used by the Settings save
// handler once it exists, and by main.go's one-time re-encryption of
// credentials left over from a SESSION_SECRET rotation.
func SaveSpotifyCredentials(sqlDB *sql.DB, userID int64, encClientID, encClientSecret string) error {
	_, err := sqlDB.Exec("UPDATE users SET spotify_client_id = ?, spotify_client_secret = ? WHERE id = ?",
		encClientID, encClientSecret, userID)
	return err
}

// SpotifyTokens holds a user's Spotify OAuth tokens (both encrypted) and
// the access token's expiry, all stored directly on the users table for
// backward compatibility with the original schema (unlike every other
// OAuth target, which uses the oauth_connections table).
type SpotifyTokens struct {
	AccessToken  string // encrypted
	RefreshToken sql.NullString
	ExpiresAt    int64 // unix millis
}

func GetSpotifyTokens(sqlDB *sql.DB, userID int64) (*SpotifyTokens, error) {
	var accessToken sql.NullString
	var refreshToken sql.NullString
	var expiresAt sql.NullInt64
	err := sqlDB.QueryRow(
		"SELECT spotify_access_token, spotify_refresh_token, spotify_token_expires_at FROM users WHERE id = ?", userID,
	).Scan(&accessToken, &refreshToken, &expiresAt)
	if err == sql.ErrNoRows || !accessToken.Valid {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &SpotifyTokens{AccessToken: accessToken.String, RefreshToken: refreshToken, ExpiresAt: expiresAt.Int64}, nil
}

func SaveSpotifyTokens(sqlDB *sql.DB, userID int64, encryptedAccessToken, encryptedRefreshToken string, expiresAt time.Time) error {
	_, err := sqlDB.Exec(
		"UPDATE users SET spotify_access_token = ?, spotify_refresh_token = ?, spotify_token_expires_at = ? WHERE id = ?",
		encryptedAccessToken, nullIfEmpty(encryptedRefreshToken), expiresAt.UnixMilli(), userID,
	)
	return err
}

func ClearSpotifyTokens(sqlDB *sql.DB, userID int64) error {
	_, err := sqlDB.Exec(
		"UPDATE users SET spotify_access_token = NULL, spotify_refresh_token = NULL, spotify_token_expires_at = NULL WHERE id = ?",
		userID,
	)
	return err
}

// ClearSpotifyCredentials additionally clears the user's own Client
// ID/Secret - used when decryption fails (SESSION_SECRET changed), since a
// credential that can't be decrypted is as unusable as a missing one.
func ClearSpotifyCredentials(sqlDB *sql.DB, userID int64) error {
	_, err := sqlDB.Exec(
		`UPDATE users SET spotify_client_id = NULL, spotify_client_secret = NULL,
		 spotify_access_token = NULL, spotify_refresh_token = NULL, spotify_token_expires_at = NULL WHERE id = ?`,
		userID,
	)
	return err
}

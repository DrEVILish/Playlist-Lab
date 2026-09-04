// Package spotify ports adapters/spotify-target.ts (the OAuth-backed
// Spotify Web API integration) to Go. The unauthenticated scraping/
// public-playlist half of spotify-source.ts is Puppeteer-based and belongs
// to Phase 5 of the rewrite plan, not here.
package spotify

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/drevilish/playlist-lab/internal/crypto"
	"github.com/drevilish/playlist-lab/internal/db"
)

var ErrNotConnected = errors.New("not connected to Spotify. Please authenticate first")

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// getCredentials decrypts the user's own Spotify app Client ID/Secret.
func getCredentials(sqlDB *sql.DB, secret string, userID int64) (clientID, clientSecret string, err error) {
	creds, err := db.GetSpotifyCredentials(sqlDB, userID)
	if err != nil {
		return "", "", err
	}
	if creds == nil {
		return "", "", errors.New("Spotify credentials not configured. Please provide your Client ID and Client Secret in Settings")
	}
	clientID, err = crypto.Decrypt(creds.ClientID, secret)
	if err != nil {
		return "", "", fmt.Errorf("failed to decrypt Spotify client ID: %w", err)
	}
	clientSecret, err = crypto.Decrypt(creds.ClientSecret, secret)
	if err != nil {
		return "", "", fmt.Errorf("failed to decrypt Spotify client secret: %w", err)
	}
	return clientID, clientSecret, nil
}

// GetToken returns a valid, decrypted Spotify access token for userID,
// refreshing it first if expired. Returns ("", nil) - not an error - if the
// user has never connected Spotify, matching getSpotifyToken()'s "null
// means not connected" contract in spotify-auth.ts.
func GetToken(sqlDB *sql.DB, secret string, userID int64) (string, error) {
	tokens, err := db.GetSpotifyTokens(sqlDB, userID)
	if err != nil {
		return "", err
	}
	if tokens == nil {
		return "", nil
	}

	if time.Now().UnixMilli() >= tokens.ExpiresAt {
		if !tokens.RefreshToken.Valid || tokens.RefreshToken.String == "" {
			return "", nil
		}
		return refreshToken(sqlDB, secret, userID, tokens.RefreshToken.String)
	}

	return crypto.Decrypt(tokens.AccessToken, secret)
}

// refreshToken matches refreshSpotifyToken() in spotify-auth.ts, including
// its self-healing behavior: a decryption failure means SESSION_SECRET
// changed since these were encrypted, so the now-unusable credentials are
// cleared rather than left to fail the same way on every future call.
func refreshToken(sqlDB *sql.DB, secret string, userID int64, encryptedRefreshToken string) (string, error) {
	clientID, clientSecret, err := getCredentials(sqlDB, secret, userID)
	if err != nil {
		_ = db.ClearSpotifyCredentials(sqlDB, userID)
		return "", err
	}

	refreshTok, err := crypto.Decrypt(encryptedRefreshToken, secret)
	if err != nil {
		_ = db.ClearSpotifyTokens(sqlDB, userID)
		return "", fmt.Errorf("failed to decrypt Spotify refresh token: %w", err)
	}

	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshTok}}
	tokens, err := requestToken(clientID, clientSecret, form)
	if err != nil {
		return "", err
	}

	newRefreshToken := encryptedRefreshToken
	if tokens.RefreshToken != "" {
		newRefreshToken, err = crypto.Encrypt(tokens.RefreshToken, secret)
		if err != nil {
			return "", err
		}
	}
	encryptedAccess, err := crypto.Encrypt(tokens.AccessToken, secret)
	if err != nil {
		return "", err
	}
	expiresAt := time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	if err := db.SaveSpotifyTokens(sqlDB, userID, encryptedAccess, newRefreshToken, expiresAt); err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

func requestToken(clientID, clientSecret string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequest(http.MethodPost, "https://accounts.spotify.com/api/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var errBody struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.ErrorDescription != "" {
			return nil, fmt.Errorf("%s", errBody.ErrorDescription)
		}
		return nil, fmt.Errorf("token request failed: status %d", resp.StatusCode)
	}
	var tokens tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return nil, err
	}
	return &tokens, nil
}

// Package tidal ports adapters/tidal-target.ts: Tidal login via
// username/password against Tidal's own public web/mobile app client
// credentials - no developer registration needed. The oauth_connections
// table's scope column is repurposed to hold Tidal's own numeric user ID,
// since that table has no adapter-specific columns and playlist creation
// needs it.
package tidal

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/adapters/shared"
	"github.com/drevilish/playlist-lab/internal/crypto"
	"github.com/drevilish/playlist-lab/internal/db"
)

const serviceName = "tidal"

// Tidal's own public client ID/secret (used by their own web app) - not
// secrets in the security sense, same defaults the Node adapter falls back
// to when TIDAL_CLIENT_ID/SECRET aren't set.
const (
	defaultClientID     = "zU4XHVVkc2tDPo4t"
	defaultClientSecret = "VJKhDFqJPqvsPVNBV6ukXTJmwlvbttP7wlMlrc72se4="
)

type Target struct {
	DB           *sql.DB
	Secret       string
	ClientID     string
	ClientSecret string
	httpClient   *http.Client
}

func NewTarget(sqlDB *sql.DB, secret, clientID, clientSecret string) *Target {
	if clientID == "" {
		clientID = defaultClientID
	}
	if clientSecret == "" {
		clientSecret = defaultClientSecret
	}
	return &Target{DB: sqlDB, Secret: secret, ClientID: clientID, ClientSecret: clientSecret, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

func (t *Target) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "Tidal", Icon: "tidal", RequiresOAuth: true}
}

func (t *Target) IsConfigured() bool { return true }

type tidalAuth struct {
	Token  string
	UserID string
}

// getAuth mirrors getToken() in tidal-target.ts: returns a live access
// token, refreshing via the stored refresh token if the current one
// expires within 60s.
func (t *Target) getAuth(userID int64) (*tidalAuth, error) {
	conn, err := db.GetOAuthConnection(t.DB, userID, serviceName)
	if err != nil || conn == nil {
		return nil, err
	}
	tidalUserID := ""
	if conn.Scope.Valid {
		tidalUserID = conn.Scope.String
	}

	if conn.TokenExpiresAt.Valid && conn.TokenExpiresAt.Int64 > time.Now().Add(60*time.Second).UnixMilli() {
		token, err := crypto.Decrypt(conn.AccessToken, t.Secret)
		if err != nil {
			return nil, nil
		}
		return &tidalAuth{Token: token, UserID: tidalUserID}, nil
	}

	if !conn.RefreshToken.Valid || conn.RefreshToken.String == "" {
		return nil, nil
	}
	refreshToken, err := crypto.Decrypt(conn.RefreshToken.String, t.Secret)
	if err != nil {
		return nil, nil
	}

	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}
	tokens, err := t.requestToken(form)
	if err != nil {
		return nil, nil
	}

	encryptedAccess, err := crypto.Encrypt(tokens.AccessToken, t.Secret)
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().Add(time.Duration(orDefault(tokens.ExpiresIn, 3600)) * time.Second)
	if err := db.UpdateOAuthAccessToken(t.DB, userID, serviceName, encryptedAccess, expiresAt); err != nil {
		return nil, err
	}
	return &tidalAuth{Token: tokens.AccessToken, UserID: tidalUserID}, nil
}

func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	User         struct {
		UserID int64 `json:"userId"`
	} `json:"user"`
}

func (t *Target) requestToken(form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequest(http.MethodPost, "https://auth.tidal.com/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(t.ClientID+":"+t.ClientSecret)))
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var errBody struct {
			ErrorDescription string `json:"error_description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.ErrorDescription != "" {
			return nil, fmt.Errorf("%s", errBody.ErrorDescription)
		}
		return nil, fmt.Errorf("Tidal login failed. Check your username and password")
	}
	var tokens tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return nil, err
	}
	return &tokens, nil
}

var ErrNotConnected = fmt.Errorf("not connected to Tidal. Please log in")

type tidalTrack struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Artist struct {
		Name string `json:"name"`
	} `json:"artist"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Album struct {
		Title string `json:"title"`
	} `json:"album"`
}

func (tt tidalTrack) artistName() string {
	if tt.Artist.Name != "" {
		return tt.Artist.Name
	}
	if len(tt.Artists) > 0 {
		return tt.Artists[0].Name
	}
	return ""
}

func (t *Target) search(token, query string, limit int) ([]tidalTrack, error) {
	u := "https://api.tidal.com/v1/search/tracks?query=" + url.QueryEscape(query) + "&limit=" + strconv.Itoa(limit) + "&countryCode=US"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Tidal-Token", t.ClientID)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Tidal search failed: status %d", resp.StatusCode)
	}
	var data struct {
		Items []tidalTrack `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Items, nil
}

func toMatchResult(source adapters.TrackInfo, item tidalTrack, score float64, matched bool) adapters.MatchResult {
	return adapters.MatchResult{
		SourceTrack: source, TargetTrackID: strconv.FormatInt(item.ID, 10), TargetTitle: item.Title,
		TargetArtist: item.artistName(), TargetAlbum: item.Album.Title, Confidence: score, Matched: matched,
	}
}

func (t *Target) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]adapters.MatchResult, error) {
	auth, err := t.getAuth(userID)
	if err != nil {
		return nil, err
	}
	if auth == nil {
		return nil, ErrNotConnected
	}
	items, err := t.search(auth.Token, query, 10)
	if err != nil {
		return nil, err
	}
	source := adapters.TrackInfo{Title: query}
	results := make([]adapters.MatchResult, len(items))
	for i, item := range items {
		results[i] = toMatchResult(source, item, shared.Similarity(query, item.Title), true)
	}
	return results, nil
}

func (t *Target) MatchTracks(ctx context.Context, tracks []adapters.TrackInfo, cfg adapters.TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]adapters.MatchResult, error) {
	auth, err := t.getAuth(userID)
	if err != nil {
		return nil, err
	}
	if auth == nil {
		return nil, ErrNotConnected
	}

	results := make([]adapters.MatchResult, 0, len(tracks))
	for i, track := range tracks {
		if isCancelled != nil && isCancelled() {
			break
		}
		query := strings.TrimSpace(track.Title + " " + track.Artist)
		result := adapters.MatchResult{SourceTrack: track}

		items, err := t.search(auth.Token, query, 5)
		if err != nil {
			slog.Warn("tidal search failed for track", "track", track, "error", err)
		} else if len(items) > 0 {
			sort.SliceStable(items, func(a, b int) bool {
				return shared.ScoreResult(track.Title, track.Artist, items[a].Title, items[a].artistName()) >
					shared.ScoreResult(track.Title, track.Artist, items[b].Title, items[b].artistName())
			})
			best := items[0]
			score := shared.ScoreResult(track.Title, track.Artist, best.Title, best.artistName())
			result = toMatchResult(track, best, score, score >= 50)
		}

		results = append(results, result)
		if progress != nil {
			progress(i+1, len(tracks))
		}
	}
	return results, nil
}

func (t *Target) CreatePlaylist(ctx context.Context, name string, matches []adapters.MatchResult, cfg adapters.TargetConfig, userID int64) (string, string, int, error) {
	auth, err := t.getAuth(userID)
	if err != nil {
		return "", "", 0, err
	}
	if auth == nil {
		return "", "", 0, ErrNotConnected
	}

	body, _ := json.Marshal(map[string]string{"title": name, "description": ""})
	req, _ := http.NewRequest(http.MethodPost, "https://api.tidal.com/v1/users/"+url.PathEscape(auth.UserID)+"/playlists", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	req.Header.Set("X-Tidal-Token", t.ClientID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", 0, fmt.Errorf("failed to create Tidal playlist: status %d", resp.StatusCode)
	}
	var created struct {
		UUID string `json:"uuid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", "", 0, err
	}

	var trackIDs []int64
	for _, m := range matches {
		if m.Matched && !m.Skipped && m.TargetTrackID != "" {
			if id, err := strconv.ParseInt(m.TargetTrackID, 10, 64); err == nil {
				trackIDs = append(trackIDs, id)
			}
		}
	}
	if len(trackIDs) > 0 {
		addBody, _ := json.Marshal(map[string]any{"trackIds": trackIDs, "toIndex": 0})
		addReq, _ := http.NewRequest(http.MethodPost, "https://api.tidal.com/v1/playlists/"+created.UUID+"/items", strings.NewReader(string(addBody)))
		addReq.Header.Set("Authorization", "Bearer "+auth.Token)
		addReq.Header.Set("X-Tidal-Token", t.ClientID)
		addReq.Header.Set("Content-Type", "application/json")
		addReq.Header.Set("If-None-Match", "*")
		if addResp, err := t.httpClient.Do(addReq); err == nil {
			addResp.Body.Close()
		}
	}

	return created.UUID, name, len(trackIDs), nil
}

// GetOAuthURL: Tidal uses username/password (the "password" OAuth2 grant),
// not a redirect-based flow - this returns a form-handoff URL matching
// tidal-target.ts.
func (t *Target) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	formURL := strings.Replace(redirectURI, "/callback", "/form", 1)
	return fmt.Sprintf("%s?service=tidal&state=%d&type=credentials", formURL, userID), nil
}

// HandleOAuthCallback: code is base64("username:password").
func (t *Target) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	decoded, err := base64.StdEncoding.DecodeString(code)
	if err != nil {
		return fmt.Errorf("invalid credentials format")
	}
	username, password, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return fmt.Errorf("invalid credentials format")
	}

	form := url.Values{"grant_type": {"password"}, "username": {username}, "password": {password}, "scope": {"r_usr w_usr w_sub"}}
	tokens, err := t.requestToken(form)
	if err != nil {
		return err
	}

	encryptedAccess, err := crypto.Encrypt(tokens.AccessToken, t.Secret)
	if err != nil {
		return err
	}
	var encryptedRefresh string
	if tokens.RefreshToken != "" {
		encryptedRefresh, err = crypto.Encrypt(tokens.RefreshToken, t.Secret)
		if err != nil {
			return err
		}
	}
	expiresAt := time.Now().Add(time.Duration(orDefault(tokens.ExpiresIn, 3600)) * time.Second)
	tidalUserID := strconv.FormatInt(tokens.User.UserID, 10)
	return db.SaveOAuthConnectionWithScope(t.DB, userID, serviceName, encryptedAccess, encryptedRefresh, &expiresAt, tidalUserID)
}

func (t *Target) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	auth, err := t.getAuth(userID)
	return auth != nil, err
}

func (t *Target) RevokeConnection(ctx context.Context, userID int64) error {
	return db.DeleteOAuthConnection(t.DB, userID, serviceName)
}

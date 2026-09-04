// Package qobuz ports adapters/qobuz-target.ts: Qobuz login via
// username/password against Qobuz's public app_id (embedded in their own
// web app - no developer registration needed), yielding a user_auth_token
// that's sent on every subsequent request.
package qobuz

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

const serviceName = "qobuz"

// defaultAppID is Qobuz's own public web-app app_id - not a secret, no
// registration required, same default the Node adapter falls back to.
const defaultAppID = "285473059"

type Target struct {
	DB         *sql.DB
	Secret     string
	AppID      string
	httpClient *http.Client
}

func NewTarget(sqlDB *sql.DB, secret, appID string) *Target {
	if appID == "" {
		appID = defaultAppID
	}
	return &Target{DB: sqlDB, Secret: secret, AppID: appID, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

func (t *Target) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "Qobuz", Icon: "qobuz", RequiresOAuth: true}
}

func (t *Target) IsConfigured() bool { return true }

func (t *Target) getToken(userID int64) (string, error) {
	conn, err := db.GetOAuthConnection(t.DB, userID, serviceName)
	if err != nil || conn == nil {
		return "", err
	}
	token, err := crypto.Decrypt(conn.AccessToken, t.Secret)
	if err != nil {
		return "", nil
	}
	return token, nil
}

var ErrNotConnected = fmt.Errorf("not connected to Qobuz. Please log in first")

type qobuzTrack struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Performer struct {
		Name string `json:"name"`
	} `json:"performer"`
	Album struct {
		Title string `json:"title"`
	} `json:"album"`
}

type qobuzSearchResponse struct {
	Tracks struct {
		Items []qobuzTrack `json:"items"`
	} `json:"tracks"`
}

func (t *Target) search(token, query string, limit int) ([]qobuzTrack, error) {
	u := "https://www.qobuz.com/api.json/0.2/track/search?query=" + url.QueryEscape(query) +
		"&limit=" + strconv.Itoa(limit) + "&app_id=" + t.AppID
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-User-Auth-Token", token)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Qobuz search failed: status %d", resp.StatusCode)
	}
	var data qobuzSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Tracks.Items, nil
}

func toMatchResult(source adapters.TrackInfo, item qobuzTrack, score float64, matched bool) adapters.MatchResult {
	return adapters.MatchResult{
		SourceTrack: source, TargetTrackID: strconv.FormatInt(item.ID, 10), TargetTitle: item.Title,
		TargetArtist: item.Performer.Name, TargetAlbum: item.Album.Title, Confidence: score, Matched: matched,
	}
}

func (t *Target) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]adapters.MatchResult, error) {
	token, err := t.getToken(userID)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, ErrNotConnected
	}
	items, err := t.search(token, query, 10)
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
	token, err := t.getToken(userID)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, ErrNotConnected
	}

	results := make([]adapters.MatchResult, 0, len(tracks))
	for i, track := range tracks {
		if isCancelled != nil && isCancelled() {
			break
		}
		query := strings.TrimSpace(track.Title + " " + track.Artist)
		result := adapters.MatchResult{SourceTrack: track}

		items, err := t.search(token, query, 5)
		if err != nil {
			slog.Warn("qobuz search failed for track", "track", track, "error", err)
		} else if len(items) > 0 {
			sort.SliceStable(items, func(a, b int) bool {
				return shared.ScoreResult(track.Title, track.Artist, items[a].Title, items[a].Performer.Name) >
					shared.ScoreResult(track.Title, track.Artist, items[b].Title, items[b].Performer.Name)
			})
			best := items[0]
			score := shared.ScoreResult(track.Title, track.Artist, best.Title, best.Performer.Name)
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
	token, err := t.getToken(userID)
	if err != nil {
		return "", "", 0, err
	}
	if token == "" {
		return "", "", 0, ErrNotConnected
	}

	createURL := "https://www.qobuz.com/api.json/0.2/playlist/create?name=" + url.QueryEscape(name) + "&is_public=false&app_id=" + t.AppID
	req, _ := http.NewRequest(http.MethodPost, createURL, nil)
	req.Header.Set("X-User-Auth-Token", token)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", 0, fmt.Errorf("failed to create Qobuz playlist: status %d", resp.StatusCode)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", "", 0, err
	}
	playlistID := strconv.FormatInt(created.ID, 10)

	var trackIDs []string
	for _, m := range matches {
		if m.Matched && !m.Skipped && m.TargetTrackID != "" {
			trackIDs = append(trackIDs, m.TargetTrackID)
		}
	}
	if len(trackIDs) > 0 {
		addURL := "https://www.qobuz.com/api.json/0.2/playlist/addTracks?playlist_id=" + playlistID +
			"&track_ids=" + strings.Join(trackIDs, ",") + "&app_id=" + t.AppID
		addReq, _ := http.NewRequest(http.MethodPost, addURL, nil)
		addReq.Header.Set("X-User-Auth-Token", token)
		if addResp, err := t.httpClient.Do(addReq); err == nil {
			addResp.Body.Close()
		}
	}

	return playlistID, name, len(trackIDs), nil
}

// GetOAuthURL: Qobuz uses username/password, not a redirect-based OAuth
// flow - this returns a form-handoff URL matching qobuz-target.ts.
func (t *Target) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	formURL := strings.Replace(redirectURI, "/callback", "/form", 1)
	return fmt.Sprintf("%s?service=qobuz&state=%d&type=credentials", formURL, userID), nil
}

// HandleOAuthCallback: code is base64("username:password"), matching
// qobuz-target.ts's handoff format from the credentials form.
func (t *Target) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	decoded, err := base64.StdEncoding.DecodeString(code)
	if err != nil {
		return fmt.Errorf("invalid credentials format")
	}
	username, password, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return fmt.Errorf("invalid credentials format")
	}

	loginURL := "https://www.qobuz.com/api.json/0.2/user/login?username=" + url.QueryEscape(username) +
		"&password=" + url.QueryEscape(password) + "&app_id=" + t.AppID
	resp, err := t.httpClient.Get(loginURL)
	if err != nil {
		return fmt.Errorf("Qobuz login failed. Check your username and password")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Qobuz login failed. Check your username and password")
	}
	var data struct {
		UserAuthToken string `json:"user_auth_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}
	if data.UserAuthToken == "" {
		return fmt.Errorf("no auth token returned from Qobuz")
	}

	encrypted, err := crypto.Encrypt(data.UserAuthToken, t.Secret)
	if err != nil {
		return err
	}
	return db.SaveOAuthConnection(t.DB, userID, serviceName, encrypted, "", nil)
}

func (t *Target) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	token, err := t.getToken(userID)
	return token != "", err
}

func (t *Target) RevokeConnection(ctx context.Context, userID int64) error {
	return db.DeleteOAuthConnection(t.DB, userID, serviceName)
}

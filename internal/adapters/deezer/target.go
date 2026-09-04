// Package deezer ports adapters/deezer-target.ts to Go: Deezer has no
// developer-app OAuth, just a long-lived "ARL" session cookie the user
// copies out of their browser after logging in to deezer.com, sent as a
// Cookie header on every Deezer API request.
package deezer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"context"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/adapters/shared"
	"github.com/drevilish/playlist-lab/internal/crypto"
	"github.com/drevilish/playlist-lab/internal/db"
)

const serviceName = "deezer"

type Target struct {
	DB         *sql.DB
	Secret     string
	httpClient *http.Client
}

func NewTarget(sqlDB *sql.DB, secret string) *Target {
	return &Target{DB: sqlDB, Secret: secret, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

func (t *Target) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "Deezer", Icon: "deezer", RequiresOAuth: true}
}

func (t *Target) IsConfigured() bool { return true }

func (t *Target) getARL(userID int64) (string, error) {
	conn, err := db.GetOAuthConnection(t.DB, userID, serviceName)
	if err != nil || conn == nil {
		return "", err
	}
	arl, err := crypto.Decrypt(conn.AccessToken, t.Secret)
	if err != nil {
		return "", nil // matches the Node adapter's catch-and-return-null on a stale/undecryptable ARL
	}
	return arl, nil
}

func (t *Target) deezerRequest(method, path, arl string, body io.Reader) (map[string]any, error) {
	req, err := http.NewRequest(method, "https://api.deezer.com"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", "arl="+arl)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Deezer API error: %d", resp.StatusCode)
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

type deezerTrack struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Artist struct {
		Name string `json:"name"`
	} `json:"artist"`
	Album struct {
		Title string `json:"title"`
	} `json:"album"`
}

func (t *Target) searchTracks(arl, query string, limit int) ([]deezerTrack, error) {
	data, err := t.deezerRequest(http.MethodGet, "/search/track?q="+url.QueryEscape(query)+"&limit="+strconv.Itoa(limit), arl, nil)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(data["data"])
	if err != nil {
		return nil, err
	}
	var tracks []deezerTrack
	if err := json.Unmarshal(raw, &tracks); err != nil {
		return nil, err
	}
	return tracks, nil
}

func toMatchResult(source adapters.TrackInfo, item deezerTrack, score float64, matched bool) adapters.MatchResult {
	return adapters.MatchResult{
		SourceTrack: source, TargetTrackID: strconv.FormatInt(item.ID, 10), TargetTitle: item.Title,
		TargetArtist: item.Artist.Name, TargetAlbum: item.Album.Title, Confidence: score, Matched: matched,
	}
}

var ErrNotConnected = fmt.Errorf("not connected to Deezer. Please add your ARL token")

func (t *Target) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]adapters.MatchResult, error) {
	arl, err := t.getARL(userID)
	if err != nil {
		return nil, err
	}
	if arl == "" {
		return nil, ErrNotConnected
	}
	items, err := t.searchTracks(arl, query, 10)
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
	arl, err := t.getARL(userID)
	if err != nil {
		return nil, err
	}
	if arl == "" {
		return nil, ErrNotConnected
	}

	results := make([]adapters.MatchResult, 0, len(tracks))
	for i, track := range tracks {
		if isCancelled != nil && isCancelled() {
			break
		}
		query := strings.TrimSpace(track.Title + " " + track.Artist)
		result := adapters.MatchResult{SourceTrack: track}

		items, err := t.searchTracks(arl, query, 5)
		if err != nil {
			slog.Warn("deezer search failed for track", "track", track, "error", err)
		} else if len(items) > 0 {
			sort.SliceStable(items, func(a, b int) bool {
				return shared.ScoreResult(track.Title, track.Artist, items[a].Title, items[a].Artist.Name) >
					shared.ScoreResult(track.Title, track.Artist, items[b].Title, items[b].Artist.Name)
			})
			best := items[0]
			score := shared.ScoreResult(track.Title, track.Artist, best.Title, best.Artist.Name)
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
	arl, err := t.getARL(userID)
	if err != nil {
		return "", "", 0, err
	}
	if arl == "" {
		return "", "", 0, ErrNotConnected
	}

	me, err := t.deezerRequest(http.MethodGet, "/user/me", arl, nil)
	if err != nil {
		return "", "", 0, err
	}
	meID, _ := me["id"].(float64)

	created, err := t.deezerRequest(http.MethodPost, "/user/"+strconv.FormatInt(int64(meID), 10)+"/playlists", arl,
		strings.NewReader(url.Values{"title": {name}}.Encode()))
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to create Deezer playlist: %w", err)
	}
	playlistIDFloat, _ := created["id"].(float64)
	playlistID := strconv.FormatInt(int64(playlistIDFloat), 10)

	var trackIDs []string
	for _, m := range matches {
		if m.Matched && !m.Skipped && m.TargetTrackID != "" {
			trackIDs = append(trackIDs, m.TargetTrackID)
		}
	}
	if len(trackIDs) > 0 {
		if _, err := t.deezerRequest(http.MethodPost, "/playlist/"+playlistID+"/tracks", arl,
			strings.NewReader(url.Values{"songs": {strings.Join(trackIDs, ",")}}.Encode())); err != nil {
			return "", "", 0, fmt.Errorf("failed to add tracks to Deezer playlist: %w", err)
		}
	}

	return playlistID, name, len(trackIDs), nil
}

// GetOAuthURL: Deezer has no real OAuth redirect - this returns a
// form-input URL the frontend renders as an ARL text box instead of
// redirecting to a third-party sign-in page, matching
// deezer-target.ts's getOAuthUrl().
func (t *Target) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	formURL := strings.Replace(redirectURI, "/callback", "/form", 1)
	return fmt.Sprintf("%s?service=deezer&state=%d&type=arl", formURL, userID), nil
}

// HandleOAuthCallback: code is actually the pasted ARL token, not an OAuth
// authorization code - validated by fetching /user/me before it's stored.
func (t *Target) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	arl := strings.TrimSpace(code)
	if arl == "" {
		return fmt.Errorf("no ARL token provided")
	}
	data, err := t.deezerRequest(http.MethodGet, "/user/me", arl, nil)
	if err != nil {
		return fmt.Errorf("invalid Deezer ARL token. Please check and try again")
	}
	if _, hasError := data["error"]; hasError {
		return fmt.Errorf("invalid Deezer ARL token. Please check and try again")
	}

	encrypted, err := crypto.Encrypt(arl, t.Secret)
	if err != nil {
		return err
	}
	return db.SaveOAuthConnection(t.DB, userID, serviceName, encrypted, "", nil)
}

func (t *Target) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	arl, err := t.getARL(userID)
	return arl != "", err
}

func (t *Target) RevokeConnection(ctx context.Context, userID int64) error {
	return db.DeleteOAuthConnection(t.DB, userID, serviceName)
}

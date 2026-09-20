// Package apple ports adapters/apple-target.ts to Go: Apple Music via
// MusicKit. The developer JWT (signed with the app's own ES256 private key)
// is generated server-side; the per-user "Music User Token" is obtained by
// MusicKit JS running in the browser and handed to this adapter as the
// OAuth "code" during the callback flow, same as the Node version.
package apple

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/adapters/shared"
	"github.com/drevilish/playlist-lab/internal/crypto"
	"github.com/drevilish/playlist-lab/internal/db"
)

const serviceName = "apple"

type Target struct {
	DB            *sql.DB
	Secret        string
	TeamID        string
	KeyID         string
	PrivateKeyPEM string
	httpClient    *http.Client
}

func NewTarget(sqlDB *sql.DB, secret, teamID, keyID, privateKeyPEM string) *Target {
	return &Target{
		DB: sqlDB, Secret: secret, TeamID: teamID, KeyID: keyID,
		PrivateKeyPEM: strings.ReplaceAll(privateKeyPEM, `\n`, "\n"),
		httpClient:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (t *Target) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "Apple Music", Icon: "apple", RequiresOAuth: true}
}

func (t *Target) IsConfigured() bool {
	return t.TeamID != "" && t.KeyID != "" && t.PrivateKeyPEM != ""
}

func (t *Target) getUserToken(userID int64) (string, error) {
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

var ErrNotConnected = fmt.Errorf("not connected to Apple Music. Please authenticate first")

type appleSong struct {
	ID         string `json:"id"`
	Attributes struct {
		Name       string `json:"name"`
		ArtistName string `json:"artistName"`
		AlbumName  string `json:"albumName"`
	} `json:"attributes"`
}

type searchResponse struct {
	Results struct {
		Songs struct {
			Data []appleSong `json:"data"`
		} `json:"songs"`
	} `json:"results"`
}

func (t *Target) search(ctx context.Context, devToken, userToken, query string, limit int) ([]appleSong, error) {
	u := "https://api.music.apple.com/v1/catalog/us/search?types=songs&term=" + url.QueryEscape(query) + "&limit=" + fmt.Sprint(limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+devToken)
	req.Header.Set("Music-User-Token", userToken)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("Apple Music search failed: status %d", resp.StatusCode)
	}
	var data searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Results.Songs.Data, nil
}

func toMatchResult(source adapters.TrackInfo, item appleSong, score float64, matched bool) adapters.MatchResult {
	return adapters.MatchResult{
		SourceTrack: source, TargetTrackID: item.ID, TargetTitle: item.Attributes.Name,
		TargetArtist: item.Attributes.ArtistName, TargetAlbum: item.Attributes.AlbumName, Confidence: score, Matched: matched,
	}
}

func (t *Target) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]adapters.MatchResult, error) {
	userToken, err := t.getUserToken(userID)
	if err != nil {
		return nil, err
	}
	if userToken == "" {
		return nil, ErrNotConnected
	}
	devToken, err := generateDeveloperToken(t.TeamID, t.KeyID, t.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	items, err := t.search(ctx, devToken, userToken, query, 10)
	if err != nil {
		return nil, err
	}
	source := adapters.TrackInfo{Title: query}
	results := make([]adapters.MatchResult, len(items))
	for i, item := range items {
		results[i] = toMatchResult(source, item, shared.Similarity(query, item.Attributes.Name), true)
	}
	return results, nil
}

func (t *Target) MatchTracks(ctx context.Context, tracks []adapters.TrackInfo, cfg adapters.TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]adapters.MatchResult, error) {
	userToken, err := t.getUserToken(userID)
	if err != nil {
		return nil, err
	}
	if userToken == "" {
		return nil, ErrNotConnected
	}
	devToken, err := generateDeveloperToken(t.TeamID, t.KeyID, t.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}

	results := make([]adapters.MatchResult, 0, len(tracks))
	for i, track := range tracks {
		if isCancelled != nil && isCancelled() {
			break
		}
		query := strings.TrimSpace(track.Title + " " + track.Artist)
		result := adapters.MatchResult{SourceTrack: track}

		items, err := t.search(ctx, devToken, userToken, query, 5)
		if err != nil {
			slog.Warn("apple music search failed for track", "track", track, "error", err)
		} else if len(items) > 0 {
			sort.SliceStable(items, func(a, b int) bool {
				return shared.ScoreResult(track.Title, track.Artist, items[a].Attributes.Name, items[a].Attributes.ArtistName) >
					shared.ScoreResult(track.Title, track.Artist, items[b].Attributes.Name, items[b].Attributes.ArtistName)
			})
			best := items[0]
			score := shared.ScoreResult(track.Title, track.Artist, best.Attributes.Name, best.Attributes.ArtistName)
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
	userToken, err := t.getUserToken(userID)
	if err != nil {
		return "", "", 0, err
	}
	if userToken == "" {
		return "", "", 0, ErrNotConnected
	}
	devToken, err := generateDeveloperToken(t.TeamID, t.KeyID, t.PrivateKeyPEM)
	if err != nil {
		return "", "", 0, err
	}

	type trackRef struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	var trackRefs []trackRef
	for _, m := range matches {
		if m.Matched && !m.Skipped && m.TargetTrackID != "" {
			trackRefs = append(trackRefs, trackRef{ID: m.TargetTrackID, Type: "songs"})
		}
	}

	body, _ := json.Marshal(map[string]any{
		"attributes":    map[string]string{"name": name},
		"relationships": map[string]any{"tracks": map[string]any{"data": trackRefs}},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.music.apple.com/v1/me/library/playlists", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+devToken)
	req.Header.Set("Music-User-Token", userToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", 0, fmt.Errorf("failed to create Apple Music playlist: status %d", resp.StatusCode)
	}
	var created struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", "", 0, err
	}
	playlistID := "unknown"
	if len(created.Data) > 0 {
		playlistID = created.Data[0].ID
	}
	return playlistID, name, len(trackRefs), nil
}

// GetOAuthURL: Apple Music auth needs MusicKit JS running in the browser -
// this returns a form-handoff URL (with a dev token embedded for MusicKit
// JS to use) instead of a redirect to a third-party sign-in page.
func (t *Target) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	devToken, err := generateDeveloperToken(t.TeamID, t.KeyID, t.PrivateKeyPEM)
	if err != nil {
		return "", err
	}
	formURL := strings.Replace(redirectURI, "/callback", "/form", 1)
	return fmt.Sprintf("%s?service=apple&state=%d&devToken=%s", formURL, userID, url.QueryEscape(devToken)), nil
}

// HandleOAuthCallback: code is the MusicKit user token obtained by the
// frontend, not an OAuth authorization code.
func (t *Target) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	userToken := strings.TrimSpace(code)
	if userToken == "" {
		return fmt.Errorf("no Apple Music user token provided")
	}
	encrypted, err := crypto.Encrypt(userToken, t.Secret)
	if err != nil {
		return err
	}
	return db.SaveOAuthConnection(t.DB, userID, serviceName, encrypted, "", nil)
}

func (t *Target) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	token, err := t.getUserToken(userID)
	return token != "", err
}

func (t *Target) RevokeConnection(ctx context.Context, userID int64) error {
	return db.DeleteOAuthConnection(t.DB, userID, serviceName)
}

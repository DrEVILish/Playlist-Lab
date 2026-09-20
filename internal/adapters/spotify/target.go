package spotify

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

// Target implements adapters.TargetAdapter and adapters.OAuthCapable for
// Spotify, porting adapters/spotify-target.ts. Every user brings their own
// Spotify app registration (Client ID/Secret, stored encrypted on the users
// row) rather than the server sharing one, so tokens live directly on
// users.spotify_* for backward compatibility with the original schema
// (every other OAuth target uses the shared oauth_connections table).
type Target struct {
	DB          *sql.DB
	Secret      string // encryption secret, see internal/config's SessionSecret
	RedirectURI string // resolved once at construction, see getSpotifyRedirectUri() in spotify-target.ts
	httpClient  *http.Client
}

func NewTarget(sqlDB *sql.DB, secret, redirectURI string) *Target {
	return &Target{DB: sqlDB, Secret: secret, RedirectURI: redirectURI, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

func (t *Target) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: "spotify", Name: "Spotify", Icon: "spotify", RequiresOAuth: true}
}

func (t *Target) IsConfigured() bool { return true } // per-user credentials, not a server-wide env var

type spotifyTrack struct {
	URI     string `json:"uri"`
	Name    string `json:"name"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Album struct {
		Name string `json:"name"`
	} `json:"album"`
}

type searchResponse struct {
	Tracks struct {
		Items []spotifyTrack `json:"items"`
	} `json:"tracks"`
}

func scoreResult(sourceTitle, sourceArtist string, item spotifyTrack) float64 {
	artistName := ""
	if len(item.Artists) > 0 {
		artistName = item.Artists[0].Name
	}
	return shared.ScoreResult(sourceTitle, sourceArtist, item.Name, artistName)
}

func toMatchResult(source adapters.TrackInfo, item spotifyTrack, score float64, matched bool) adapters.MatchResult {
	artistName := ""
	if len(item.Artists) > 0 {
		artistName = item.Artists[0].Name
	}
	return adapters.MatchResult{
		SourceTrack: source, TargetTrackID: item.URI, TargetTitle: item.Name,
		TargetArtist: artistName, TargetAlbum: item.Album.Name, Confidence: score, Matched: matched,
	}
}

func (t *Target) searchTracks(ctx context.Context, token, query string, limit int) ([]spotifyTrack, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.spotify.com/v1/search?type=track&limit="+fmt.Sprint(limit)+"&q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var errBody struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.Error.Message != "" {
			return nil, fmt.Errorf("%s", errBody.Error.Message)
		}
		return nil, fmt.Errorf("Spotify search failed: status %d", resp.StatusCode)
	}
	var data searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Tracks.Items, nil
}

func (t *Target) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]adapters.MatchResult, error) {
	token, err := GetToken(ctx, t.DB, t.Secret, userID)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, ErrNotConnected
	}
	items, err := t.searchTracks(ctx, token, query, 10)
	if err != nil {
		return nil, err
	}
	source := adapters.TrackInfo{Title: query}
	results := make([]adapters.MatchResult, len(items))
	for i, item := range items {
		results[i] = toMatchResult(source, item, scoreResult(query, "", item), true)
	}
	return results, nil
}

func (t *Target) MatchTracks(ctx context.Context, tracks []adapters.TrackInfo, cfg adapters.TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]adapters.MatchResult, error) {
	token, err := GetToken(ctx, t.DB, t.Secret, userID)
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

		items, err := t.searchTracks(ctx, token, query, 5)
		if err != nil {
			slog.Warn("spotify search failed for track", "track", track, "error", err)
		} else if len(items) > 0 {
			sort.SliceStable(items, func(a, b int) bool {
				return scoreResult(track.Title, track.Artist, items[a]) > scoreResult(track.Title, track.Artist, items[b])
			})
			best := items[0]
			score := scoreResult(track.Title, track.Artist, best)
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
	token, err := GetToken(ctx, t.DB, t.Secret, userID)
	if err != nil {
		return "", "", 0, err
	}
	if token == "" {
		return "", "", 0, ErrNotConnected
	}

	me, err := t.getMe(ctx, token)
	if err != nil {
		return "", "", 0, err
	}

	playlistID, createdName, err := t.createEmptyPlaylist(ctx, token, me, name)
	if err != nil {
		return "", "", 0, err
	}

	var uris []string
	for _, m := range matches {
		if m.Matched && !m.Skipped && m.TargetTrackID != "" {
			uris = append(uris, m.TargetTrackID)
		}
	}
	if len(uris) == 0 {
		return playlistID, createdName, 0, nil
	}

	const batchSize = 100
	for i := 0; i < len(uris); i += batchSize {
		end := min(i+batchSize, len(uris))
		if err := t.addTracks(ctx, token, playlistID, uris[i:end]); err != nil {
			t.deletePlaylist(ctx, token, playlistID) // best-effort cleanup of the now-broken empty playlist
			return "", "", 0, err
		}
	}

	return playlistID, createdName, len(uris), nil
}

func (t *Target) getMe(ctx context.Context, token string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.spotify.com/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("failed to get Spotify user info: status %d", resp.StatusCode)
	}
	var me struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return "", err
	}
	return me.ID, nil
}

func (t *Target) createEmptyPlaylist(ctx context.Context, token, spotifyUserID, name string) (id, createdName string, err error) {
	body, _ := json.Marshal(map[string]any{"name": name, "public": false})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.spotify.com/v1/users/"+url.PathEscape(spotifyUserID)+"/playlists", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("failed to create Spotify playlist: status %d", resp.StatusCode)
	}
	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", "", err
	}
	return created.ID, created.Name, nil
}

func (t *Target) addTracks(ctx context.Context, token, playlistID string, uris []string) error {
	body, _ := json.Marshal(map[string]any{"uris": uris})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.spotify.com/v1/playlists/"+url.PathEscape(playlistID)+"/tracks", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("failed to add tracks to Spotify playlist: status %d", resp.StatusCode)
	}
	return nil
}

func (t *Target) deletePlaylist(ctx context.Context, token, playlistID string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, "https://api.spotify.com/v1/playlists/"+url.PathEscape(playlistID)+"/followers", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := t.httpClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// GetOAuthURL ports getOAuthUrl(): note the redirectURI parameter is
// ignored, matching spotify-target.ts's own behavior - Spotify's redirect
// URI must exactly match what's registered in the Spotify app dashboard, so
// it always uses the server-resolved RedirectURI rather than whatever a
// caller passes in.
func (t *Target) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	clientID, _, err := getCredentials(t.DB, t.Secret, userID)
	if err != nil {
		return "", err
	}
	scopes := strings.Join([]string{
		"playlist-read-private", "playlist-read-collaborative",
		"playlist-modify-public", "playlist-modify-private", "user-library-read",
	}, " ")
	params := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "scope": {scopes},
		"redirect_uri": {t.RedirectURI}, "state": {fmt.Sprint(userID)}, "show_dialog": {"true"},
	}
	return "https://accounts.spotify.com/authorize?" + params.Encode(), nil
}

func (t *Target) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	clientID, clientSecret, err := getCredentials(t.DB, t.Secret, userID)
	if err != nil {
		return err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {t.RedirectURI}}
	tokens, err := requestToken(ctx, clientID, clientSecret, form)
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
	expiresAt := time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	return db.SaveSpotifyTokens(t.DB, userID, encryptedAccess, encryptedRefresh, expiresAt)
}

func (t *Target) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	token, err := GetToken(ctx, t.DB, t.Secret, userID)
	if err != nil {
		return false, err
	}
	return token != "", nil
}

func (t *Target) RevokeConnection(ctx context.Context, userID int64) error {
	return db.ClearSpotifyTokens(t.DB, userID)
}

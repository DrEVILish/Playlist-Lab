// Package listenbrainz ports adapters/listenbrainz-target.ts: ListenBrainz
// writes (playlist creation) use a simple user token, but ListenBrainz
// itself has no track search API, so catalog search goes through
// MusicBrainz's public recording search instead - a separate, unrelated,
// unauthenticated service.
package listenbrainz

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

const (
	serviceName = "listenbrainz"
	apiBase     = "https://api.listenbrainz.org"
)

type Target struct {
	DB         *sql.DB
	Secret     string
	httpClient *http.Client
}

func NewTarget(sqlDB *sql.DB, secret string) *Target {
	return &Target{DB: sqlDB, Secret: secret, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

func (t *Target) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "ListenBrainz", Icon: "listenbrainz", RequiresOAuth: true}
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

var ErrNotConnected = fmt.Errorf("not connected to ListenBrainz. Please add your user token first")

type recording struct {
	ID           string `json:"id"` // MBID
	Title        string `json:"title"`
	ArtistCredit []struct {
		Name string `json:"name"`
	} `json:"artist-credit"`
	Releases []struct {
		Title string `json:"title"`
	} `json:"releases"`
}

func (r recording) artistName() string {
	if len(r.ArtistCredit) > 0 {
		return r.ArtistCredit[0].Name
	}
	return ""
}

func (r recording) albumTitle() string {
	if len(r.Releases) > 0 {
		return r.Releases[0].Title
	}
	return ""
}

func (t *Target) searchMusicBrainz(ctx context.Context, query string, limit int) ([]recording, error) {
	u := "https://musicbrainz.org/ws/2/recording?query=" + url.QueryEscape(query) + "&limit=" + fmt.Sprint(limit) + "&fmt=json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PlaylistLab/1.0 (playlist-lab)")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("MusicBrainz search failed: status %d", resp.StatusCode)
	}
	var data struct {
		Recordings []recording `json:"recordings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Recordings, nil
}

func toMatchResult(source adapters.TrackInfo, item recording, score float64, matched bool) adapters.MatchResult {
	return adapters.MatchResult{
		SourceTrack: source, TargetTrackID: item.ID, TargetTitle: item.Title,
		TargetArtist: item.artistName(), TargetAlbum: item.albumTitle(), Confidence: score, Matched: matched,
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
	items, err := t.searchMusicBrainz(ctx, query, 10)
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
		query := fmt.Sprintf(`recording:"%s" AND artist:"%s"`, track.Title, track.Artist)
		result := adapters.MatchResult{SourceTrack: track}

		items, err := t.searchMusicBrainz(ctx, query, 5)
		if err != nil {
			slog.Warn("musicbrainz search failed for track", "track", track, "error", err)
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

		// MusicBrainz allows 1 request/sec - skip the wait after the last
		// track so a playlist's total match time isn't padded needlessly.
		if i < len(tracks)-1 {
			time.Sleep(1100 * time.Millisecond)
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

	type identifierTrack struct {
		Identifier []string `json:"identifier"`
	}
	var mbids []string
	var tracks []identifierTrack
	for _, m := range matches {
		if m.Matched && !m.Skipped && m.TargetTrackID != "" {
			mbids = append(mbids, m.TargetTrackID)
			tracks = append(tracks, identifierTrack{Identifier: []string{"https://musicbrainz.org/recording/" + m.TargetTrackID}})
		}
	}

	body, _ := json.Marshal(map[string]any{
		"playlist": map[string]any{"title": name, "track": tracks},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/1/playlist/create", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Token "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", 0, fmt.Errorf("failed to create ListenBrainz playlist: status %d", resp.StatusCode)
	}
	var created struct {
		PlaylistMBID string `json:"playlist_mbid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", "", 0, err
	}
	return created.PlaylistMBID, name, len(mbids), nil
}

// GetOAuthURL: ListenBrainz uses a plain user token, not OAuth - this
// returns a form-handoff URL matching listenbrainz-target.ts.
func (t *Target) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	formURL := strings.Replace(redirectURI, "/callback", "/form", 1)
	return fmt.Sprintf("%s?service=listenbrainz&state=%d", formURL, userID), nil
}

func (t *Target) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	token := strings.TrimSpace(code)
	if token == "" {
		return fmt.Errorf("no ListenBrainz token provided")
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/1/validate-token", nil)
	req.Header.Set("Authorization", "Token "+token)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("invalid ListenBrainz token")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("invalid ListenBrainz token")
	}
	var data struct {
		Valid bool `json:"valid"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}
	if !data.Valid {
		return fmt.Errorf("ListenBrainz token is not valid")
	}

	encrypted, err := crypto.Encrypt(token, t.Secret)
	if err != nil {
		return err
	}
	// The Node adapter also best-effort writes a listenbrainz_username
	// column that doesn't actually exist in schema.sql (defensively
	// swallowed there as "column may not exist yet") - there's nothing to
	// port, since that write would never have succeeded.
	return db.SaveOAuthConnection(t.DB, userID, serviceName, encrypted, "", nil)
}

func (t *Target) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	token, err := t.getToken(userID)
	return token != "", err
}

func (t *Target) RevokeConnection(ctx context.Context, userID int64) error {
	return db.DeleteOAuthConnection(t.DB, userID, serviceName)
}

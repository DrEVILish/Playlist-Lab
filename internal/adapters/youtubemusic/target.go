// Package youtubemusic ports adapters/youtube-target.ts: a direct REST
// integration against YouTube Music's own internal "innertube" web API
// (the same API music.youtube.com's own frontend calls), authenticated with
// a browser session cookie the user pastes in rather than OAuth. This is
// plain HTTP + a SHA1-based auth header - no youtubei.js/InnerTube-library
// dependency at all, unlike youtube-innertube-target.ts, so it ports
// directly with no R1-style gap.
package youtubemusic

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/adapters/shared"
	"github.com/drevilish/playlist-lab/internal/crypto"
	"github.com/drevilish/playlist-lab/internal/db"
)

const (
	serviceName = "youtube-music"
	ytmBase     = "https://music.youtube.com"
	ytmAPI      = ytmBase + "/youtubei/v1"
	// The search "params" blob restricts results to songs - an opaque,
	// version-pinned protobuf blob YouTube's own web client sends, copied
	// verbatim from the Node adapter rather than decoded/rebuilt.
	songsOnlySearchParams = "EgWKAQIIAWoKEAkQBRAKEAMQBA%3D%3D"
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
	return adapters.ServiceMeta{ID: serviceName, Name: "YouTube Music", Icon: "youtube-music", RequiresOAuth: true}
}

func (t *Target) IsConfigured() bool { return true }

func (t *Target) getCookie(userID int64) (string, error) {
	conn, err := db.GetOAuthConnection(t.DB, userID, serviceName)
	if err != nil || conn == nil {
		return "", err
	}
	cookie, err := crypto.Decrypt(conn.AccessToken, t.Secret)
	if err != nil {
		return "", nil
	}
	return cookie, nil
}

var ErrNotConnected = fmt.Errorf("not connected to YouTube Music. Please add your browser cookie")

var sapisidPattern = regexp.MustCompile(`(?:__Secure-3PAPISID|SAPISID)=([^;]+)`)

// generateSapisidHash builds the SAPISIDHASH Authorization header Google's
// own web clients use to prove possession of a session cookie without a
// full OAuth handshake - a SHA1 of "<unix-seconds> <SAPISID> <origin>".
func generateSapisidHash(sapisid string) string {
	timestamp := time.Now().Unix()
	sum := sha1.Sum([]byte(fmt.Sprintf("%d %s %s", timestamp, sapisid, ytmBase)))
	return fmt.Sprintf("SAPISIDHASH %d_%x", timestamp, sum)
}

func (t *Target) ytmAPI(endpoint string, body map[string]any, cookie string) (map[string]any, error) {
	sapisid := ""
	if m := sapisidPattern.FindStringSubmatch(cookie); m != nil {
		sapisid = m[1]
	}

	fullBody := map[string]any{"context": map[string]any{"client": map[string]string{
		"clientName": "WEB_REMIX", "clientVersion": "1.20240101.01.00",
	}}}
	for k, v := range body {
		fullBody[k] = v
	}
	payload, err := json.Marshal(fullBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, ytmAPI+"/"+endpoint+"?prettyPrint=false", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	if sapisid != "" {
		req.Header.Set("Authorization", generateSapisidHash(sapisid))
	}
	req.Header.Set("X-Origin", ytmBase)
	req.Header.Set("Origin", ytmBase)
	req.Header.Set("Referer", ytmBase+"/")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("YouTube Music API error: status %d", resp.StatusCode)
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

type ytmTrack struct {
	VideoID string
	Title   string
	Artist  string
}

// extractTracks walks the deeply-nested, undocumented YouTube Music search
// response shape - ported field-path-for-field-path from
// youtube-target.ts's extractTrack(), tolerating any missing field the way
// its try/catch-and-return-null did.
func extractTracks(data map[string]any) []ytmTrack {
	items := digArray(data,
		"contents", "tabbedSearchResultsRenderer", "tabs", 0, "tabRenderer", "content",
		"sectionListRenderer", "contents", 0, "musicShelfRenderer", "contents")

	var tracks []ytmTrack
	for _, item := range items {
		renderer, _ := digMap(item, "musicResponsiveListItemRenderer")
		if renderer == nil {
			continue
		}
		videoID, _ := digString(renderer, "playlistItemData", "videoId")
		if videoID == "" {
			videoID, _ = digString(renderer, "overlay", "musicItemThumbnailOverlayRenderer", "content",
				"musicPlayButtonRenderer", "playNavigationEndpoint", "watchEndpoint", "videoId")
		}
		if videoID == "" {
			continue
		}
		cols := digArray(renderer, "flexColumns")
		title, _ := digStringFrom(cols, 0, "musicResponsiveListItemFlexColumnRenderer", "text", "runs", 0, "text")
		artist, _ := digStringFrom(cols, 1, "musicResponsiveListItemFlexColumnRenderer", "text", "runs", 0, "text")
		tracks = append(tracks, ytmTrack{VideoID: videoID, Title: title, Artist: artist})
	}
	return tracks
}

func (t *Target) search(cookie, query string) ([]ytmTrack, error) {
	data, err := t.ytmAPI("search", map[string]any{"query": query, "params": songsOnlySearchParams}, cookie)
	if err != nil {
		return nil, err
	}
	return extractTracks(data), nil
}

func toMatchResult(source adapters.TrackInfo, track ytmTrack, score float64, matched bool) adapters.MatchResult {
	return adapters.MatchResult{
		SourceTrack: source, TargetTrackID: track.VideoID, TargetTitle: track.Title,
		TargetArtist: track.Artist, Confidence: score, Matched: matched,
	}
}

func (t *Target) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]adapters.MatchResult, error) {
	cookie, err := t.getCookie(userID)
	if err != nil {
		return nil, err
	}
	if cookie == "" {
		return nil, ErrNotConnected
	}
	tracks, err := t.search(cookie, query)
	if err != nil {
		return nil, err
	}
	source := adapters.TrackInfo{Title: query}
	results := make([]adapters.MatchResult, len(tracks))
	for i, tr := range tracks {
		results[i] = toMatchResult(source, tr, shared.Similarity(query, tr.Title), true)
	}
	return results, nil
}

func (t *Target) MatchTracks(ctx context.Context, tracks []adapters.TrackInfo, cfg adapters.TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]adapters.MatchResult, error) {
	cookie, err := t.getCookie(userID)
	if err != nil {
		return nil, err
	}
	if cookie == "" {
		return nil, ErrNotConnected
	}

	results := make([]adapters.MatchResult, 0, len(tracks))
	for i, track := range tracks {
		if isCancelled != nil && isCancelled() {
			break
		}
		query := strings.TrimSpace(track.Title + " " + track.Artist)
		result := adapters.MatchResult{SourceTrack: track}

		candidates, err := t.search(cookie, query)
		if err != nil {
			slog.Warn("youtube music search failed for track", "track", track, "error", err)
		} else if len(candidates) > 0 {
			sort.SliceStable(candidates, func(a, b int) bool {
				return shared.ScoreResult(track.Title, track.Artist, candidates[a].Title, candidates[a].Artist) >
					shared.ScoreResult(track.Title, track.Artist, candidates[b].Title, candidates[b].Artist)
			})
			best := candidates[0]
			score := shared.ScoreResult(track.Title, track.Artist, best.Title, best.Artist)
			result = toMatchResult(track, best, score, score >= 40)
		}

		results = append(results, result)
		if progress != nil {
			progress(i+1, len(tracks))
		}
	}
	return results, nil
}

func (t *Target) CreatePlaylist(ctx context.Context, name string, matches []adapters.MatchResult, cfg adapters.TargetConfig, userID int64) (string, string, int, error) {
	cookie, err := t.getCookie(userID)
	if err != nil {
		return "", "", 0, err
	}
	if cookie == "" {
		return "", "", 0, ErrNotConnected
	}

	var videoIDs []string
	for _, m := range matches {
		if m.Matched && !m.Skipped && m.TargetTrackID != "" {
			videoIDs = append(videoIDs, m.TargetTrackID)
		}
	}

	data, err := t.ytmAPI("playlist/create", map[string]any{
		"title": name, "description": "", "privacyStatus": "PRIVATE", "videoIds": videoIDs,
	}, cookie)
	if err != nil {
		return "", "", 0, err
	}
	playlistID, _ := data["playlistId"].(string)
	if playlistID == "" {
		playlistID = "unknown"
	}
	return playlistID, name, len(videoIDs), nil
}

func (t *Target) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	formURL := strings.Replace(redirectURI, "/callback", "/form", 1)
	return fmt.Sprintf("%s?service=youtube-music&state=%d&type=cookie", formURL, userID), nil
}

func (t *Target) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	cookie := strings.TrimSpace(code)
	if cookie == "" {
		return fmt.Errorf("no cookie provided")
	}
	if _, err := t.search(cookie, "test"); err != nil {
		return fmt.Errorf("invalid YouTube Music cookie. Please check and try again")
	}

	encrypted, err := crypto.Encrypt(cookie, t.Secret)
	if err != nil {
		return err
	}
	return db.SaveOAuthConnection(t.DB, userID, serviceName, encrypted, "", nil)
}

func (t *Target) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	cookie, err := t.getCookie(userID)
	return cookie != "", err
}

func (t *Target) RevokeConnection(ctx context.Context, userID int64) error {
	return db.DeleteOAuthConnection(t.DB, userID, serviceName)
}

// --- tiny untyped-JSON navigation helpers, since YouTube's internal API
// response has no stable schema worth defining structs for ---

func digMap(v any, path ...any) (map[string]any, bool) {
	cur := v
	for _, p := range path {
		cur = index(cur, p)
		if cur == nil {
			return nil, false
		}
	}
	m, ok := cur.(map[string]any)
	return m, ok
}

func digArray(v any, path ...any) []any {
	cur := v
	for _, p := range path {
		cur = index(cur, p)
		if cur == nil {
			return nil
		}
	}
	arr, _ := cur.([]any)
	return arr
}

func digString(v any, path ...any) (string, bool) {
	cur := v
	for _, p := range path {
		cur = index(cur, p)
		if cur == nil {
			return "", false
		}
	}
	s, ok := cur.(string)
	return s, ok
}

func digStringFrom(arr []any, idx int, path ...any) (string, bool) {
	if idx < 0 || idx >= len(arr) {
		return "", false
	}
	return digString(arr[idx], path...)
}

func index(v any, key any) any {
	if v == nil {
		return nil
	}
	switch k := key.(type) {
	case string:
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		return m[k]
	case int:
		arr, ok := v.([]any)
		if !ok || k < 0 || k >= len(arr) {
			return nil
		}
		return arr[k]
	default:
		return nil
	}
}

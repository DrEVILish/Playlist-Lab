// Package youtubeplain ports adapters/youtube-plain-source.ts (registry id
// "youtube", registered and active - not to be confused with
// youtube-source.ts's "youtube-music" id, which is browser-scraping based
// and belongs to Phase 5). FetchTracks parses the public playlist page's
// embedded ytInitialData JSON blob - plain HTTP + regex + encoding/json, no
// browser needed. ListPlaylists/SearchPlaylists call YouTube's internal
// (undocumented) API directly, same as youtubemusic's target adapter.
package youtubeplain

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/crypto"
	"github.com/drevilish/playlist-lab/internal/db"
)

const serviceName = "youtube"

type Source struct {
	DB         *sql.DB
	Secret     string
	httpClient *http.Client
}

func NewSource(sqlDB *sql.DB, secret string) *Source {
	return &Source{DB: sqlDB, Secret: secret, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

func (s *Source) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "YouTube", Icon: "youtube"}
}

var playlistIDPattern = regexp.MustCompile(`[?&]list=([a-zA-Z0-9_-]+)`)

// FetchTracks parses the public playlist page's embedded ytInitialData JSON
// - no auth, no browser, matching scrapeYouTubePlaylist()'s approach
// exactly.
func (s *Source) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	m := playlistIDPattern.FindStringSubmatch(playlistURLOrID)
	if m == nil {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("invalid YouTube playlist URL. Please provide a URL containing ?list=...")
	}
	playlistID := m[1]

	req, err := http.NewRequest(http.MethodGet, "https://www.youtube.com/playlist?list="+playlistID, nil)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Without a consent cookie, YouTube 302s this request to a
	// consent.youtube.com interstitial for any request originating from an
	// EU/UK-geolocated IP - confirmed live during verification (the original
	// TS scraper this was ported from has the same gap and would fail
	// identically if deployed in such a region). CONSENT/SOCS are the
	// standard values a browser gets after clicking through that page once.
	req.Header.Set("Cookie", "CONSENT=YES+cb; SOCS=CAI")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	data, err := extractYtInitialData(body)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("could not parse YouTube playlist page")
	}

	itemSection := digArray(data, "contents", "twoColumnBrowseResultsRenderer", "tabs", 0,
		"tabRenderer", "content", "sectionListRenderer", "contents", 0, "itemSectionRenderer", "contents")

	// Classic pages wrap the actual video items one level deeper inside a
	// playlistVideoListRenderer; live verification found current YouTube
	// serving playlist pages where itemSection's entries (lockupViewModel
	// items, see extractVideoTrack) sit directly at this level with no such
	// wrapper - try the classic wrapper first, fall back to the flat shape.
	contents := digArray(itemSection, 0, "playlistVideoListRenderer", "contents")
	if len(contents) == 0 {
		contents = itemSection
	}

	// microformatDataRenderer.title is a bare string; playlistHeaderRenderer's
	// is the usual {simpleText}/{runs} shape - current YouTube pages were
	// found live to use neither of the two header renderers this was
	// originally ported against (a "pageHeaderRenderer" instead, not handled
	// here), so the microformat fallback carries the actual name in
	// practice.
	name, _ := digString(data, "microformat", "microformatDataRenderer", "title")
	if name == "" {
		header, _ := digMap(data, "header", "playlistHeaderRenderer")
		name, _ = digString(header, "title", "simpleText")
		if name == "" {
			name, _ = digStringFrom(digArray(header, "title", "runs"), 0, "text")
		}
	}
	if name == "" {
		name = "YouTube Playlist"
	}

	var tracks []adapters.TrackInfo
	for _, item := range contents {
		if track, ok := extractVideoTrack(item); ok {
			tracks = append(tracks, track)
		}
	}

	playlist := adapters.PlaylistInfo{ID: "youtube-" + playlistID, Name: name, TrackCount: len(tracks)}
	return playlist, tracks, nil
}

// extractVideoTrack reads a single playlist-page item, checked against the
// real current page format live (not just the TS source it was ported
// from): YouTube has migrated at least some playlists to a newer
// "lockupViewModel" item shape that the original scrapeYouTubePlaylist()
// never handled (it only knew the classic playlistVideoRenderer) - fetching
// a real playlist during verification hit exactly this and returned zero
// tracks, so this adds the new shape rather than reproducing that
// staleness. The nearby extractPlaylist() in the real TS file already
// handles lockupViewModel for playlist *listings*, just never for a
// playlist's own video items.
func extractVideoTrack(item any) (adapters.TrackInfo, bool) {
	if video, ok := digMap(item, "playlistVideoRenderer"); ok {
		title, _ := digStringFrom(digArray(video, "title", "runs"), 0, "text")
		if title == "" {
			title, _ = digString(video, "title", "simpleText")
		}
		artist, _ := digStringFrom(digArray(video, "shortBylineText", "runs"), 0, "text")
		return adapters.TrackInfo{Title: orUnknown(title), Artist: orUnknown(artist)}, true
	}

	if lvm, ok := digMap(item, "lockupViewModel"); ok {
		title, _ := digString(lvm, "metadata", "lockupMetadataViewModel", "title", "content")
		artist, _ := digStringFrom(
			digArray(lvm, "metadata", "lockupMetadataViewModel", "metadata", "contentMetadataViewModel", "metadataRows", 0, "metadataParts"),
			0, "text", "content")
		if title == "" {
			return adapters.TrackInfo{}, false
		}
		return adapters.TrackInfo{Title: orUnknown(title), Artist: orUnknown(artist)}, true
	}

	return adapters.TrackInfo{}, false
}

func orUnknown(s string) string {
	if s == "" {
		return "Unknown"
	}
	return s
}

// extractYtInitialData pulls the `var ytInitialData = {...};` JSON blob
// YouTube embeds directly in the playlist page's HTML.
var ytInitialDataPattern = regexp.MustCompile(`(?s)var ytInitialData\s*=\s*(\{.+?\});\s*</script>`)

func extractYtInitialData(html []byte) (map[string]any, error) {
	m := ytInitialDataPattern.FindSubmatch(html)
	if m == nil {
		return nil, fmt.Errorf("ytInitialData not found")
	}
	var data map[string]any
	if err := json.Unmarshal(m[1], &data); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *Source) getStoredCookie(userID int64) (string, error) {
	conn, err := db.GetOAuthConnection(s.DB, userID, serviceName)
	if err != nil || conn == nil {
		return "", err
	}
	cookie, err := crypto.Decrypt(conn.AccessToken, s.Secret)
	if err != nil {
		return "", nil
	}
	return cookie, nil
}

// SearchPlaylists calls YouTube's internal search endpoint, authenticated
// if the user has a stored cookie (from the youtubemusic/youtube target's
// cookie flow, which shares the "youtube" oauth_connections row) or
// unauthenticated otherwise - matching searchPlaylists()'s fallback.
func (s *Source) SearchPlaylists(ctx context.Context, query string, userID int64) ([]adapters.PlaylistInfo, error) {
	cookie, err := s.getStoredCookie(userID)
	if err != nil {
		return nil, err
	}

	data, err := s.ytAPI("search", map[string]any{"query": query, "params": "EgIQAw%3D%3D"}, cookie)
	if err != nil {
		return nil, err
	}

	items := digArray(data, "contents", "twoColumnSearchResultsRenderer", "primaryContents",
		"sectionListRenderer", "contents", 0, "itemSectionRenderer", "contents")

	var playlists []adapters.PlaylistInfo
	for _, item := range items {
		r, ok := digMap(item, "playlistRenderer")
		if !ok {
			continue
		}
		id, _ := digString(r, "playlistId")
		title, _ := digString(r, "title", "simpleText")
		if title == "" {
			title, _ = digStringFrom(digArray(r, "title", "runs"), 0, "text")
		}
		if id == "" || title == "" {
			continue
		}
		countText, _ := digStringFrom(digArray(r, "videoCountText", "runs"), 0, "text")
		trackCount := parseDigits(countText)
		playlists = append(playlists, adapters.PlaylistInfo{ID: id, Name: title, TrackCount: trackCount})
	}
	return playlists, nil
}

var nonDigitPattern = regexp.MustCompile(`\D`)

func parseDigits(s string) int {
	n, _ := strconv.Atoi(nonDigitPattern.ReplaceAllString(s, ""))
	return n
}

func (s *Source) ytAPI(endpoint string, body map[string]any, cookie string) (map[string]any, error) {
	fullBody := map[string]any{"context": map[string]any{"client": map[string]string{
		"clientName": "WEB", "clientVersion": "2.20250101.00.00",
	}}}
	for k, v := range body {
		fullBody[k] = v
	}
	payload, err := json.Marshal(fullBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, "https://www.youtube.com/youtubei/v1/"+endpoint+"?prettyPrint=false", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("X-YouTube-Client-Name", "1")
	req.Header.Set("X-YouTube-Client-Version", "2.20250101.00.00")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
		req.Header.Set("X-Origin", "https://www.youtube.com")
		req.Header.Set("Origin", "https://www.youtube.com")
		req.Header.Set("Referer", "https://www.youtube.com/")
		req.Header.Set("X-Goog-AuthUser", "0")
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("YouTube API error: %d", resp.StatusCode)
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("YouTube API: invalid JSON")
	}
	if errObj, ok := digMap(data, "error"); ok {
		if code, ok := errObj["code"]; ok {
			return nil, fmt.Errorf("YouTube API error: %v", code)
		}
	}
	return data, nil
}

// --- tiny untyped-JSON navigation helpers (same pattern as youtubemusic's) ---

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

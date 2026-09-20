package youtube

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/crypto"
	"github.com/drevilish/playlist-lab/internal/db"
)

const ServiceName = "youtube"

type Target struct {
	DB         *sql.DB
	Secret     string
	httpClient *http.Client

	mu    sync.Mutex
	oauth OAuthConfig
}

func NewTarget(sqlDB *sql.DB, secret, clientID, clientSecret, redirectURI string) *Target {
	return &Target{
		DB: sqlDB, Secret: secret,
		oauth:      OAuthConfig{ClientID: clientID, ClientSecret: clientSecret, RedirectURI: redirectURI},
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (t *Target) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: ServiceName, Name: "YouTube", Icon: "youtube", RequiresOAuth: true}
}

// OAuthConfig returns a copy of the currently configured OAuth credentials -
// the only safe way to read them, since SetOAuth can replace them from a
// concurrent admin-panel save at any time.
func (t *Target) OAuthConfig() OAuthConfig {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.oauth
}

// SetOAuth replaces the configured OAuth credentials (admin panel save).
func (t *Target) SetOAuth(cfg OAuthConfig) {
	t.mu.Lock()
	t.oauth = cfg
	t.mu.Unlock()
}

func (t *Target) IsConfigured() bool { return t.OAuthConfig().IsConfigured() }

// getValidAccessToken mirrors youtube-oauth.ts's getValidAccessToken():
// refreshes if the stored token expires within 5 minutes, deleting the
// connection entirely if the refresh itself fails (a stale refresh token
// isn't recoverable without the user reconnecting). Exported as
// GetValidAccessToken so other callers can share the same token storage.
func (t *Target) getValidAccessToken(userID int64) (string, error) {
	return GetValidAccessToken(t.DB, t.Secret, t.OAuthConfig(), userID)
}

func GetValidAccessToken(sqlDB *sql.DB, secret string, oauth OAuthConfig, userID int64) (string, error) {
	conn, err := db.GetOAuthConnection(sqlDB, userID, ServiceName)
	if err != nil {
		return "", err
	}
	if conn == nil {
		return "", fmt.Errorf("not connected to YouTube. Please authenticate first")
	}

	const expiryBuffer = 5 * time.Minute
	isExpired := !conn.TokenExpiresAt.Valid || time.UnixMilli(conn.TokenExpiresAt.Int64).Before(time.Now().Add(expiryBuffer))

	if !isExpired {
		return crypto.Decrypt(conn.AccessToken, secret)
	}
	if !conn.RefreshToken.Valid || conn.RefreshToken.String == "" {
		return crypto.Decrypt(conn.AccessToken, secret)
	}

	refreshTok, err := crypto.Decrypt(conn.RefreshToken.String, secret)
	if err != nil {
		_ = db.DeleteOAuthConnection(sqlDB, userID, ServiceName)
		return "", fmt.Errorf("YouTube session expired. Please reconnect your YouTube account")
	}
	tokens, err := oauth.RefreshToken(refreshTok)
	if err != nil {
		_ = db.DeleteOAuthConnection(sqlDB, userID, ServiceName)
		return "", fmt.Errorf("YouTube session expired. Please reconnect your YouTube account")
	}
	if err := StoreTokens(sqlDB, secret, userID, tokens); err != nil {
		return "", err
	}
	return tokens.AccessToken, nil
}

func (t *Target) storeTokens(userID int64, tokens *GoogleTokens) error {
	return StoreTokens(t.DB, t.Secret, userID, tokens)
}

func StoreTokens(sqlDB *sql.DB, secret string, userID int64, tokens *GoogleTokens) error {
	encryptedAccess, err := crypto.Encrypt(tokens.AccessToken, secret)
	if err != nil {
		return err
	}
	var encryptedRefresh string
	if tokens.RefreshToken != "" {
		encryptedRefresh, err = crypto.Encrypt(tokens.RefreshToken, secret)
		if err != nil {
			return err
		}
	}
	expiresAt := ExpiresAtFrom(tokens)
	return db.SaveOAuthConnection(sqlDB, userID, ServiceName, encryptedAccess, encryptedRefresh, &expiresAt)
}

// cleanTrackTitle strips parentheticals and "remastered", matching
// youtube-oauth-target.ts's cleanTrackTitle().
var (
	parensPattern     = regexp.MustCompile(`\([^)]*\)`)
	remasteredPattern = regexp.MustCompile(`(?i)\bremastered?\b`)
	whitespacePattern = regexp.MustCompile(`\s+`)
)

func cleanTrackTitle(title string) string {
	cleaned := parensPattern.ReplaceAllString(title, "")
	cleaned = remasteredPattern.ReplaceAllString(cleaned, "")
	return strings.TrimSpace(whitespacePattern.ReplaceAllString(cleaned, " "))
}

// similarity ports youtube-oauth-target.ts's own similarity(): word-based
// matching first (>=80% of the shorter side's words individually contained
// in the other), falling back to LCS character similarity - a different,
// stricter algorithm than shared.Similarity's plain LCS, kept separate to
// match this adapter's real behavior (word matching rewards multi-word
// title/channel overlaps LCS alone underweights).
func similarity(a, b string) float64 {
	normalize := func(s string) string {
		var sb strings.Builder
		for _, r := range strings.ToLower(s) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
				sb.WriteRune(r)
			}
		}
		return strings.TrimSpace(sb.String())
	}
	na, nb := normalize(a), normalize(b)
	if na == nb {
		return 100
	}
	if na == "" || nb == "" {
		return 0
	}

	wordsA, wordsB := strings.Fields(na), strings.Fields(nb)
	shorterWords, longerWords := wordsA, wordsB
	if len(wordsB) < len(wordsA) {
		shorterWords, longerWords = wordsB, wordsA
	}
	matchedWords := 0
	for _, w := range shorterWords {
		for _, lw := range longerWords {
			if strings.Contains(lw, w) || strings.Contains(w, lw) {
				matchedWords++
				break
			}
		}
	}
	wordMatchRatio := float64(matchedWords) / float64(len(shorterWords))
	if wordMatchRatio >= 0.8 {
		return round(wordMatchRatio * 100)
	}

	longer, shorter := na, nb
	if len(nb) > len(na) {
		longer, shorter = nb, na
	}
	matches, pos := 0, 0
	for _, ch := range shorter {
		idx := strings.IndexRune(longer[pos:], ch)
		if idx != -1 {
			matches++
			pos += idx + len(string(ch))
		}
	}
	return round(float64(matches) / float64(len([]rune(longer))) * 100)
}

func round(f float64) float64 { return float64(int(f + 0.5)) }

var officialPattern = regexp.MustCompile(`(?i)official`)
var lyricsPattern = regexp.MustCompile(`(?i)\blyrics?\b`)

// applyBoostsAndPenalties mirrors the +15 "official" boost and -20
// live/lyrics/acoustic penalties both searchCatalog and matchTracks apply
// on top of the base title/channel similarity.
func applyBoostsAndPenalties(score float64, title string) float64 {
	if officialPattern.MatchString(title) {
		score = min(100, score+15)
	}
	lower := strings.ToLower(title)
	if strings.Contains(lower, "live") {
		score = max(0, score-20)
	}
	if lyricsPattern.MatchString(title) {
		score = max(0, score-20)
	}
	if strings.Contains(lower, "acoustic") {
		score = max(0, score-20)
	}
	return score
}

type ytVideo struct {
	ID      string
	Title   string
	Channel string
}

func (t *Target) search(ctx context.Context, accessToken, query string, maxResults int) ([]ytVideo, error) {
	params := url.Values{
		"part": {"snippet"}, "q": {query}, "type": {"video"},
		"maxResults": {fmt.Sprint(maxResults)}, "videoCategoryId": {"10"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/search?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("YouTube search failed: status %d", resp.StatusCode)
	}
	var data struct {
		Items []struct {
			ID struct {
				VideoID string `json:"videoId"`
			} `json:"id"`
			Snippet struct {
				Title        string `json:"title"`
				ChannelTitle string `json:"channelTitle"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	videos := make([]ytVideo, 0, len(data.Items))
	for _, item := range data.Items {
		if item.ID.VideoID == "" {
			continue
		}
		videos = append(videos, ytVideo{ID: item.ID.VideoID, Title: item.Snippet.Title, Channel: item.Snippet.ChannelTitle})
	}
	return videos, nil
}

func (t *Target) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]adapters.MatchResult, error) {
	accessToken, err := t.getValidAccessToken(userID)
	if err != nil {
		return nil, err
	}
	videos, err := t.search(ctx, accessToken, query, 10)
	if err != nil {
		return nil, fmt.Errorf("YouTube search failed: %w", err)
	}

	source := adapters.TrackInfo{Title: query}
	results := make([]adapters.MatchResult, len(videos))
	for i, v := range videos {
		score := applyBoostsAndPenalties(similarity(query, v.Title), v.Title)
		results[i] = adapters.MatchResult{
			SourceTrack: source, TargetTrackID: v.ID, TargetTitle: v.Title,
			TargetArtist: v.Channel, Confidence: score, Matched: true,
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].Confidence > results[j].Confidence })
	return results, nil
}

func (t *Target) MatchTracks(ctx context.Context, tracks []adapters.TrackInfo, cfg adapters.TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]adapters.MatchResult, error) {
	accessToken, err := t.getValidAccessToken(userID)
	if err != nil {
		return nil, err
	}

	results := make([]adapters.MatchResult, 0, len(tracks))
	var matchedVideoIDs []string

	for i, track := range tracks {
		if isCancelled != nil && isCancelled() {
			break
		}
		cleanedTitle := cleanTrackTitle(track.Title)
		query := strings.TrimSpace(cleanedTitle + " " + track.Artist)
		result := adapters.MatchResult{SourceTrack: track}

		if candidates, err := t.search(ctx, accessToken, query, 5); err == nil && len(candidates) > 0 {
			bestIdx, bestScore := 0, -1.0
			for ci, c := range candidates {
				score := applyBoostsAndPenalties(similarity(cleanedTitle, c.Title)*0.6+similarity(track.Artist, c.Channel)*0.4, c.Title)
				if score > bestScore {
					bestIdx, bestScore = ci, score
				}
			}
			best := candidates[bestIdx]
			result = adapters.MatchResult{
				SourceTrack: track, TargetTrackID: best.ID, TargetTitle: best.Title,
				TargetArtist: best.Channel, Confidence: bestScore, Matched: bestScore >= 40,
			}
			if result.Matched {
				matchedVideoIDs = append(matchedVideoIDs, best.ID)
			}
		}

		results = append(results, result)
		if progress != nil {
			progress(i+1, len(tracks))
		}
	}

	if len(matchedVideoIDs) > 0 && (isCancelled == nil || !isCancelled()) {
		resolutions := t.fetchResolutions(ctx, accessToken, matchedVideoIDs)
		for i, r := range results {
			if res, ok := resolutions[r.TargetTrackID]; ok {
				results[i].TargetResolution = res
			}
		}
	}

	return results, nil
}

// fetchResolutions batch-fetches contentDetails.definition (hd/sd) for
// every matched video, mapping to a display resolution - best-effort, a
// failure here doesn't affect matching.
func (t *Target) fetchResolutions(ctx context.Context, accessToken string, videoIDs []string) map[string]string {
	out := map[string]string{}
	const batchSize = 50
	for i := 0; i < len(videoIDs); i += batchSize {
		end := min(i+batchSize, len(videoIDs))
		batch := videoIDs[i:end]
		params := url.Values{"part": {"contentDetails"}, "id": {strings.Join(batch, ",")}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/videos?"+params.Encode(), nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		resp, err := t.httpClient.Do(req)
		if err != nil {
			continue
		}
		var data struct {
			Items []struct {
				ID             string `json:"id"`
				ContentDetails struct {
					Definition string `json:"definition"`
				} `json:"contentDetails"`
			} `json:"items"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&data)
		resp.Body.Close()
		for _, item := range data.Items {
			switch item.ContentDetails.Definition {
			case "hd":
				out[item.ID] = "720p+"
			case "sd":
				out[item.ID] = "480p"
			}
		}
	}
	return out
}

func (t *Target) CreatePlaylist(ctx context.Context, name string, matches []adapters.MatchResult, cfg adapters.TargetConfig, userID int64) (string, string, int, error) {
	accessToken, err := t.getValidAccessToken(userID)
	if err != nil {
		return "", "", 0, err
	}

	body, _ := json.Marshal(map[string]any{
		"snippet": map[string]string{"title": name, "description": "Created by Playlist Lab"},
		"status":  map[string]string{"privacyStatus": "private"},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/playlists?part=snippet,status", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("failed to create YouTube playlist: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", 0, fmt.Errorf("failed to create YouTube playlist: status %d", resp.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", "", 0, err
	}
	if created.ID == "" {
		return "", "", 0, fmt.Errorf("failed to create playlist - no playlist ID returned")
	}

	addPlaylistItem := func(itemBody []byte) (status int, retryAfter time.Duration, err error) {
		itemReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/playlistItems?part=snippet", strings.NewReader(string(itemBody)))
		if err != nil {
			return 0, 0, err
		}
		itemReq.Header.Set("Authorization", "Bearer "+accessToken)
		itemReq.Header.Set("Content-Type", "application/json")
		itemResp, err := t.httpClient.Do(itemReq)
		if err != nil {
			return 0, 0, err
		}
		defer itemResp.Body.Close()
		if itemResp.StatusCode == http.StatusTooManyRequests {
			if secs, parseErr := strconv.Atoi(itemResp.Header.Get("Retry-After")); parseErr == nil {
				retryAfter = time.Duration(secs) * time.Second
			}
		}
		return itemResp.StatusCode, retryAfter, nil
	}

	addedCount := 0
	for _, m := range matches {
		if !m.Matched || m.Skipped || m.TargetTrackID == "" {
			continue
		}
		itemBody, _ := json.Marshal(map[string]any{
			"snippet": map[string]any{
				"playlistId": created.ID,
				"resourceId": map[string]string{"kind": "youtube#video", "videoId": m.TargetTrackID},
			},
		})

		status, retryAfter, err := addPlaylistItem(itemBody)
		if err == nil && status == http.StatusTooManyRequests {
			if retryAfter <= 0 {
				retryAfter = 5 * time.Second
			}
			if retryAfter > 30*time.Second {
				retryAfter = 30 * time.Second
			}
			slog.Warn("youtube playlist add rate-limited, retrying once", "videoId", m.TargetTrackID, "playlistId", created.ID, "retryAfter", retryAfter)
			time.Sleep(retryAfter)
			status, _, err = addPlaylistItem(itemBody)
		}

		switch {
		case err != nil:
			slog.Warn("failed to add track to YouTube playlist", "videoId", m.TargetTrackID, "playlistId", created.ID, "error", err)
		case status >= 400:
			slog.Warn("failed to add track to YouTube playlist", "videoId", m.TargetTrackID, "playlistId", created.ID, "status", status)
		default:
			addedCount++
		}
	}

	return created.ID, name, addedCount, nil
}

func (t *Target) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	oauth := t.OAuthConfig()
	if !oauth.IsConfigured() {
		return "", fmt.Errorf("YouTube OAuth not configured. Set YOUTUBE_CLIENT_ID, YOUTUBE_CLIENT_SECRET, and YOUTUBE_REDIRECT_URI")
	}
	return oauth.AuthorizeURL(fmt.Sprint(userID)), nil
}

func (t *Target) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	oauth := t.OAuthConfig()
	if !oauth.IsConfigured() {
		return fmt.Errorf("YouTube OAuth not configured")
	}
	tokens, err := oauth.ExchangeCode(code)
	if err != nil {
		return fmt.Errorf("failed to exchange authorization code: %w", err)
	}
	return t.storeTokens(userID, tokens)
}

func (t *Target) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	conn, err := db.GetOAuthConnection(t.DB, userID, ServiceName)
	if err != nil || conn == nil {
		return false, err
	}
	return conn.AccessToken != "", nil
}

func (t *Target) RevokeConnection(ctx context.Context, userID int64) error {
	conn, err := db.GetOAuthConnection(t.DB, userID, ServiceName)
	oauth := t.OAuthConfig()
	if err == nil && conn != nil && oauth.IsConfigured() {
		if token, decErr := crypto.Decrypt(conn.AccessToken, t.Secret); decErr == nil {
			oauth.Revoke(token)
		}
	}
	return db.DeleteOAuthConnection(t.DB, userID, ServiceName)
}

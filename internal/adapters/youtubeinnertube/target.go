package youtubeinnertube

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	itb "github.com/drevilish/innertube-go"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/adapters/youtube"
)

var errNotConfigured = fmt.Errorf("YouTube OAuth not configured. Set YOUTUBE_CLIENT_ID, YOUTUBE_CLIENT_SECRET, and YOUTUBE_REDIRECT_URI")

type Target struct {
	DB     *sql.DB
	Secret string
	OAuth  youtube.OAuthConfig
	client *itb.Client
}

func NewTarget(sqlDB *sql.DB, secret, clientID, clientSecret, redirectURI string) *Target {
	return &Target{
		DB: sqlDB, Secret: secret,
		OAuth:  youtube.OAuthConfig{ClientID: clientID, ClientSecret: clientSecret, RedirectURI: redirectURI},
		client: itb.NewClient(),
	}
}

func (t *Target) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: youtube.ServiceName, Name: "YouTube", Icon: "youtube", RequiresOAuth: true}
}

func (t *Target) IsConfigured() bool { return t.OAuth.IsConfigured() }

// candidate mirrors the Node adapter's Candidate: a scored video, first
// without resolution data (cheap), then with it for the top few (one extra
// InnerTube call each).
type candidate struct {
	video         itb.Video
	confidence    float64 // uncapped - used for ranking
	title, author string
	qualityLabel  string
	isStaticImage bool
}

func (t *Target) scoreCandidates(sourceTitle, sourceArtist string, videos []itb.Video, allowLive, allowStatic bool, resolveTop int) []candidate {
	candidates := make([]candidate, 0, len(videos))
	for _, v := range videos {
		conf := calculateConfidence(sourceTitle, sourceArtist, v.Title, v.Channel, "", allowLive, false, allowStatic)
		candidates = append(candidates, candidate{video: v, confidence: conf, title: v.Title, author: v.Channel})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].confidence > candidates[j].confidence })

	top := resolveTop
	if top > len(candidates) {
		top = len(candidates)
	}
	for i := 0; i < top; i++ {
		maxHeight, maxFPS, err := itb.MaxHeightAndFPS(context.Background(), candidates[i].video.ID)
		if err != nil {
			continue // matches the Node adapter: no resolution info, keep the no-resolution score
		}
		candidates[i].qualityLabel = qualityLabel(maxHeight)
		candidates[i].isStaticImage = isStaticImage(maxFPS)
		candidates[i].confidence = calculateConfidence(sourceTitle, sourceArtist, candidates[i].title, candidates[i].author,
			candidates[i].qualityLabel, allowLive, candidates[i].isStaticImage, allowStatic)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].confidence > candidates[j].confidence })
	return candidates
}

// extractArtistFromQuery mirrors searchCatalog()'s ad hoc artist-extraction
// heuristic, used only because SearchCatalog (unlike MatchTracks) has no
// separate artist field - just a combined query string.
func extractArtistFromQuery(query, author string) (artist, titleForComparison string) {
	if author == "" {
		return "", query
	}
	queryLower, authorLower := strings.ToLower(query), strings.ToLower(author)
	if strings.Contains(queryLower, authorLower) {
		artist = author
	} else {
		normalizedAuthor := normalizeArtistName(author)
		normalizedQuery := normalizeArtistName(query)
		if strings.Contains(normalizedQuery, normalizedAuthor) ||
			strings.Contains(normalizedAuthor, strings.ReplaceAll(normalizedQuery, " ", "")) {
			artist = author
		}
	}
	titleForComparison = query
	if artist != "" {
		titleForComparison = strings.TrimSpace(replaceAllCaseInsensitive(query, artist, ""))
	}
	return artist, titleForComparison
}

func replaceAllCaseInsensitive(s, old, new string) string {
	if old == "" {
		return s
	}
	lowerS, lowerOld := strings.ToLower(s), strings.ToLower(old)
	var b strings.Builder
	for {
		idx := strings.Index(lowerS, lowerOld)
		if idx == -1 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:idx])
		b.WriteString(new)
		s = s[idx+len(old):]
		lowerS = lowerS[idx+len(old):]
	}
	return b.String()
}

func (t *Target) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]adapters.MatchResult, error) {
	videos, err := t.client.Search(query)
	if err != nil {
		return nil, err
	}
	if len(videos) > 20 {
		videos = videos[:20]
	}

	source := adapters.TrackInfo{Title: query}
	results := make([]adapters.MatchResult, 0, len(videos))
	for _, v := range videos {
		artist, titleForComparison := extractArtistFromQuery(query, v.Channel)
		maxHeight, maxFPS, _ := itb.MaxHeightAndFPS(ctx, v.ID)
		ql := qualityLabel(maxHeight)
		static := isStaticImage(maxFPS)

		conf := calculateConfidence(titleForComparison, artist, v.Title, v.Channel, ql, allowLive, static, allowStatic)
		confPercent := round(conf * 100)
		results = append(results, adapters.MatchResult{
			SourceTrack: source, Matched: true, Confidence: min(confPercent, 100),
			TargetTrackID: v.ID, TargetTitle: v.Title, TargetArtist: v.Channel,
			TargetResolution: ql, IsStaticImage: static,
		})
	}

	sort.SliceStable(results, func(i, j int) bool { return results[i].Confidence > results[j].Confidence })
	return results, nil
}

func (t *Target) MatchTracks(ctx context.Context, tracks []adapters.TrackInfo, cfg adapters.TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]adapters.MatchResult, error) {
	results := make([]adapters.MatchResult, 0, len(tracks))

	for i, track := range tracks {
		if isCancelled != nil && isCancelled() {
			break
		}
		query := strings.TrimSpace(cleanTrackTitle(track.Title) + " " + track.Artist)

		videos, err := t.client.Search(query)
		if err != nil || len(videos) == 0 {
			results = append(results, adapters.MatchResult{SourceTrack: track})
		} else {
			if len(videos) > 20 {
				videos = videos[:20]
			}
			candidates := t.scoreCandidates(track.Title, track.Artist, videos, false, false, 5)
			best := candidates[0]
			confPercent := round(best.confidence * 100)
			results = append(results, adapters.MatchResult{
				SourceTrack: track, Matched: best.confidence > 0.30, Confidence: min(confPercent, 100),
				TargetTrackID: best.video.ID, TargetTitle: best.title, TargetArtist: best.author,
				TargetResolution: best.qualityLabel, IsStaticImage: best.isStaticImage,
			})
		}

		if progress != nil {
			progress(i+1, len(tracks))
		}
		time.Sleep(50 * time.Millisecond) // matches the Node adapter's inter-track delay
	}

	return results, nil
}

func (t *Target) CreatePlaylist(ctx context.Context, name string, matches []adapters.MatchResult, cfg adapters.TargetConfig, userID int64) (string, string, int, error) {
	accessToken, err := youtube.GetValidAccessToken(t.DB, t.Secret, t.OAuth, userID)
	if err != nil {
		return "", "", 0, err
	}

	var videoIDs []string
	for _, m := range matches {
		if m.Matched && !m.Skipped && m.TargetTrackID != "" {
			videoIDs = append(videoIDs, m.TargetTrackID)
		}
	}

	playlistID, err := t.client.CreatePlaylist(accessToken, name, nil)
	if err != nil {
		return "", "", 0, err
	}

	addedCount := 0
	for _, videoID := range videoIDs {
		if err := t.client.AddVideo(accessToken, playlistID, videoID); err == nil {
			addedCount++
		}
		time.Sleep(200 * time.Millisecond) // matches the Node adapter's inter-add delay
	}

	return playlistID, name, addedCount, nil
}

func (t *Target) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	if !t.OAuth.IsConfigured() {
		return "", errNotConfigured
	}
	return t.OAuth.AuthorizeURL(strconv.FormatInt(userID, 10)), nil
}

func (t *Target) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	if !t.OAuth.IsConfigured() {
		return errNotConfigured
	}
	tokens, err := t.OAuth.ExchangeCode(code)
	if err != nil {
		return err
	}
	return youtube.StoreTokens(t.DB, t.Secret, userID, tokens)
}

func (t *Target) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	token, err := youtube.GetValidAccessToken(t.DB, t.Secret, t.OAuth, userID)
	return token != "", err
}

func (t *Target) RevokeConnection(ctx context.Context, userID int64) error {
	return (&youtube.Target{DB: t.DB, Secret: t.Secret, OAuth: t.OAuth}).RevokeConnection(ctx, userID)
}

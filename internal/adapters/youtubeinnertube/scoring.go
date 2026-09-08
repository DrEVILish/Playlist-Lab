// Package youtubeinnertube ports adapters/youtube-innertube-target.ts - the
// only YouTube target actually registered in the live app (the cookie-based
// and Data-API-v3 targets this rewrite also ported are both commented out
// there as unused/deprecated, kept only as fallbacks). Search and playlist
// management go through github.com/drevilish/innertube-go, a small,
// separate Go module built for this rewrite since no existing Go InnerTube
// library supports authenticated writes; OAuth token handling is shared
// with internal/adapters/youtube (same Google OAuth connection, same
// "youtube" oauth_connections row).
package youtubeinnertube

import (
	"regexp"
	"strings"
)

func normalizeArtistName(name string) string {
	s := strings.ToLower(name)
	s = theePrefixPattern.ReplaceAllString(s, "")
	s = hyphenSpacePattern.ReplaceAllString(s, "")
	s = musicSuffixPattern.ReplaceAllString(s, "")
	s = officialSuffixPattern.ReplaceAllString(s, "")
	s = vevoSuffixPattern.ReplaceAllString(s, "")
	return s
}

var (
	theePrefixPattern     = regexp.MustCompile(`(?i)^the\s+`)
	hyphenSpacePattern    = regexp.MustCompile(`[-_\s]`)
	musicSuffixPattern    = regexp.MustCompile(`(?i)music$`)
	officialSuffixPattern = regexp.MustCompile(`(?i)official$`)
	vevoSuffixPattern     = regexp.MustCompile(`(?i)vevo$`)

	parensPattern     = regexp.MustCompile(`\s*\([^)]*\)`)
	bracketsPattern   = regexp.MustCompile(`\s*\[[^\]]*\]`)
	dashSuffixPattern = regexp.MustCompile(`(?i)\s*-\s*(?:remaster|remastered|version|edit|mix|demo|bonus)(?:\s|$)`)
	hasParensPattern  = regexp.MustCompile(`\([^)]+\)`)
)

// cleanTrackTitle removes ALL parenthetical/bracketed content and common
// edition suffixes - deliberately more aggressive than the matching
// engine's own cleanTrackTitle (internal/services/matching), since this
// adapter is scoring YouTube video titles, which carry far more
// incidental decoration ("(Official Video)", "[4K Remaster]") than a
// Plex library tag ever does.
func cleanTrackTitle(title string) string {
	cleaned := parensPattern.ReplaceAllString(title, "")
	cleaned = bracketsPattern.ReplaceAllString(cleaned, "")
	cleaned = dashSuffixPattern.ReplaceAllString(cleaned, "")
	return strings.TrimSpace(cleaned)
}

// similarity is Levenshtein-distance-based (0-1), matching
// youtube-innertube-target.ts's own similarity() - a different algorithm
// again from both the matching engine's and the other YouTube adapters'
// LCS-based similarity, kept faithful to what this specific adapter
// actually does.
func similarity(a, b string) float64 {
	longer, shorter := a, b
	if len(b) > len(a) {
		longer, shorter = b, a
	}
	if len(longer) == 0 {
		return 1.0
	}
	dist := levenshtein(strings.ToLower(longer), strings.ToLower(shorter))
	return float64(len(longer)-dist) / float64(len(longer))
}

func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(ar)+1)
	cur := make([]int, len(ar)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(br); i++ {
		cur[0] = i
		for j := 1; j <= len(ar); j++ {
			if br[i-1] == ar[j-1] {
				cur[j] = prev[j-1]
			} else {
				cur[j] = min3(prev[j-1]+1, cur[j-1]+1, prev[j]+1)
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(ar)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

var (
	guitarLessonPattern = regexp.MustCompile(`(?i)\b(guitar\s+lesson|how\s+to\s+play|tutorial|tab|chord|fingerstyle)\b`)
	lyricsPattern       = regexp.MustCompile(`(?i)\b(lyric|lyrics)\b`)
	livePattern         = regexp.MustCompile(`(?i)\b(live|concert|performance|awards|festival)\b`)
	coverPattern        = regexp.MustCompile(`(?i)\b(cover|acoustic|karaoke|instrumental)\b`)
	audioOnlyPattern    = regexp.MustCompile(`(?i)\b(audio)\b`)
	commentaryPattern   = regexp.MustCompile(`(?i)\b(commentary|reaction|review|analysis|breakdown)\b`)
	locationPattern     = regexp.MustCompile(`(?i)\([^)]*(?:arena|stadium|theatre|theater|festival|awards|city|country|state|19\d{2}|20\d{2}|[A-Z][a-z]+,\s*[A-Z])`)
)

// calculateConfidence ports calculateYouTubeConfidence() line-for-line -
// see the Node source for the rationale behind each individual
// bonus/penalty; this is intentionally a direct port rather than a
// simplification, since the specific weights were tuned against real
// mismatches (wrong-artist videos, tutorials, static-image uploads, ...).
func calculateConfidence(sourceTitle, sourceArtist, videoTitle, channelName, qualityLabel string, allowLive, isStaticImage, allowStatic bool) float64 {
	cleanSource := strings.ToLower(cleanTrackTitle(sourceTitle))
	cleanVideo := strings.ToLower(cleanTrackTitle(videoTitle))

	sourceHasParens := hasParensPattern.MatchString(sourceTitle)
	videoHasParens := hasParensPattern.MatchString(videoTitle)
	unnecessaryParens := !sourceHasParens && videoHasParens

	titleSim := similarity(cleanSource, cleanVideo)
	artistSim := similarity(strings.ToLower(sourceArtist), strings.ToLower(channelName))
	normalizedArtist := normalizeArtistName(sourceArtist)
	normalizedChannel := normalizeArtistName(channelName)
	normalizedArtistSim := similarity(normalizedArtist, normalizedChannel)
	bestArtistSim := max(artistSim, normalizedArtistSim)

	channelHasOfficial := strings.Contains(strings.ToLower(channelName), "official")
	artistInTitle := sourceArtist != "" && strings.Contains(strings.ToLower(videoTitle), strings.ToLower(sourceArtist))
	sourceTitleInVideo := strings.Contains(strings.ToLower(videoTitle), strings.ToLower(sourceTitle))
	cleanedMatch := strings.Contains(cleanVideo, cleanSource)

	var sourceWords []string
	for _, w := range strings.Fields(cleanSource) {
		if len(w) > 2 {
			sourceWords = append(sourceWords, w)
		}
	}
	videoWords := strings.Fields(cleanVideo)
	allWordsPresent := true
	for _, w := range sourceWords {
		found := false
		for _, vw := range videoWords {
			if strings.Contains(vw, w) || strings.Contains(w, vw) {
				found = true
				break
			}
		}
		if !found {
			allWordsPresent = false
			break
		}
	}
	if len(sourceWords) == 0 {
		allWordsPresent = false
	}

	isArtistChannel := bestArtistSim > 0.85 || (channelHasOfficial && bestArtistSim > 0.60)

	videoLower := strings.ToLower(videoTitle)
	isGuitarLesson := guitarLessonPattern.MatchString(videoLower)
	isLyrics := lyricsPattern.MatchString(videoLower)
	isLive := livePattern.MatchString(videoLower)
	isCover := coverPattern.MatchString(videoLower)
	isUnplugged := strings.Contains(videoLower, "unplugged")
	isAudioOnly := audioOnlyPattern.MatchString(videoLower)
	isCommentary := commentaryPattern.MatchString(videoLower)
	hasLocation := locationPattern.MatchString(videoTitle)

	hasArtistMatch := sourceArtist == "" || isArtistChannel || artistInTitle || bestArtistSim > 0.5
	isWrongArtist := sourceArtist != "" && !isArtistChannel && !artistInTitle && bestArtistSim < 0.3

	var confidence float64
	switch {
	case isArtistChannel && titleSim > 0.95:
		confidence = 0.95
	case isArtistChannel && (titleSim > 0.85 || allWordsPresent):
		confidence = 0.90
	case isArtistChannel && titleSim > 0.75:
		confidence = 0.85
	case sourceTitleInVideo && hasArtistMatch:
		confidence = 0.90
	case (cleanedMatch || allWordsPresent) && hasArtistMatch:
		confidence = 0.85
	case titleSim > 0.8 && hasArtistMatch:
		confidence = 0.75
	case hasArtistMatch:
		confidence = titleSim * 0.70
	default:
		confidence = titleSim * 0.30
	}

	switch {
	case isArtistChannel:
		confidence += 0.20
	case bestArtistSim > 0.7:
		confidence += 0.05
	case artistInTitle:
		confidence += 0.05
	}

	if isWrongArtist {
		confidence -= 0.70
	}
	if strings.Contains(videoLower, "official") {
		confidence += 0.10
	}
	if channelHasOfficial {
		confidence += 0.15
	}
	if isGuitarLesson {
		confidence -= 0.60
	}
	if isLyrics {
		confidence -= 0.40
	}
	if isCommentary {
		confidence -= 0.50
	}
	if isAudioOnly {
		confidence -= 0.30
	}
	if isStaticImage && !allowStatic {
		confidence -= 0.35
	}
	if isLive && !allowLive {
		confidence -= 0.25
	}
	if hasLocation && !allowLive {
		confidence -= 0.30
	}
	if isCover {
		confidence -= 0.35
	}
	if isUnplugged {
		confidence -= 0.20
	}
	if strings.Contains(videoLower, "remaster") {
		confidence -= 0.02
	}
	if unnecessaryParens {
		confidence -= 0.03
	}

	switch {
	case strings.Contains(qualityLabel, "4K"):
		confidence += 0.25
	case strings.Contains(qualityLabel, "1440p"):
		confidence += 0.20
	case strings.Contains(qualityLabel, "1080p"):
		confidence += 0.15
	case strings.Contains(qualityLabel, "720p"):
		confidence += 0.05
	case strings.Contains(qualityLabel, "480p"):
		confidence -= 0.10
	case strings.Contains(qualityLabel, "360p"):
		confidence -= 0.15
	case strings.Contains(qualityLabel, "240p"):
		confidence -= 0.20
	}

	return confidence // uncapped - callers cap only when displaying, so ties/ranking can use the uncapped value
}

// qualityLabel maps a max pixel height to the same display strings
// calculateYouTubeConfidence() switches on.
func qualityLabel(maxHeight int) string {
	switch {
	case maxHeight >= 2160:
		return "4K"
	case maxHeight >= 1440:
		return "1440p"
	case maxHeight >= 1080:
		return "1080p"
	case maxHeight >= 720:
		return "720p"
	case maxHeight >= 480:
		return "480p"
	case maxHeight >= 360:
		return "360p"
	case maxHeight >= 240:
		return "240p"
	default:
		return ""
	}
}

// isStaticImage flags a static-image "video" (real music videos run
// 24-60fps; a static-image upload is typically 1-5fps).
func isStaticImage(maxFPS int) bool {
	return maxFPS > 0 && maxFPS <= 5
}

func round(f float64) float64 { return float64(int(f + 0.5)) }

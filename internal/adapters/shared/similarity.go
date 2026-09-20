// Package shared holds the small helpers duplicated verbatim across
// several of the TS target adapters (Spotify, Deezer, Apple, Tidal, Qobuz,
// ...), each of which uses its own catalog search as a fuzzy-match engine
// and only needs a rough confidence score - unlike the real matching engine
// (internal/services/matching), which is Plex-specific and considerably
// more involved (title/artist gating, version penalties, Japanese
// romanization, ...).
package shared

import "strings"

// Similarity is a longest-common-subsequence-based string similarity
// (0-100), ported verbatim from each adapter's own copy of this function.
func Similarity(a, b string) float64 {
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
	return Round(float64(matches) / float64(len([]rune(longer))) * 100)
}

// Round matches Math.round()'s half-up rounding (Go's math.Round rounds
// half away from zero, which agrees for the non-negative scores every
// caller here produces).
func Round(f float64) float64 { return float64(int(f + 0.5)) }

// ScoreResult is the same titleWeight/artistWeight blend every adapter's
// scoreResult() uses.
func ScoreResult(sourceTitle, sourceArtist, targetTitle, targetArtist string) float64 {
	return Round(Similarity(sourceTitle, targetTitle)*0.6 + Similarity(sourceArtist, targetArtist)*0.4)
}

package matching

import (
	"regexp"
	"strings"
	"unicode"
)

// normalizeForComparison ports matching.ts's normalizeForComparison():
// case-folds, strips accents (NFKD then drop combining marks, NFC to
// recompose Japanese voiced-kana marks the accent strip would otherwise
// mangle), strips quote/punctuation variants, folds "&" to "and", collapses
// "-ing" to "-in" so contraction spelling differences ("livin'" vs "living")
// land on the same word, and keeps only letters/digits from any script
// (not just ASCII) plus whitespace.
func normalizeForComparison(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "ß", "ss") // ß has no case-fold decomposition
	s = nfkd(s)
	s = stripCombiningMarks(s)
	s = nfc(s)
	s = curlyQuotePattern.ReplaceAllString(s, "")
	s = ingPattern.ReplaceAllString(s, "${1}in")
	s = curlyDoubleQuotePattern.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "$", "s")
	s = strings.ReplaceAll(s, "/", "")
	s = strings.ReplaceAll(s, ".", "")
	s = strings.ReplaceAll(s, "&", " and ")
	s = nonLetterOrDigitPattern.ReplaceAllString(s, "")
	s = ampmPattern.ReplaceAllString(s, "$1$2")
	return strings.TrimSpace(whitespacePattern.ReplaceAllString(s, " "))
}

var (
	curlyQuotePattern       = regexp.MustCompile(`[\x{2018}\x{2019}\x{201A}\x{201B}'` + "`" + `\x{00B4}\x{FFFD}]`)
	curlyDoubleQuotePattern = regexp.MustCompile(`[\x{201C}\x{201D}\x{201E}\x{201F}"]`)
	ingPattern              = regexp.MustCompile(`\b(\w{2,})ing\b`)
	nonLetterOrDigitPattern = regexp.MustCompile(`[^\p{L}\p{N}\s]`)
	whitespacePattern       = regexp.MustCompile(`\s+`)
	ampmPattern             = regexp.MustCompile(`(?i)(\d)\s+([ap]m)\b`)
	hanScriptPattern        = regexp.MustCompile(`\p{Han}`)
)

// containsWholeWord ports matching.ts's word-boundary-aware containment
// check. Go's regexp (RE2) has no lookaround, so the boundary check is done
// manually: an occurrence of needle counts only if the character
// immediately before/after it (if any) is not itself a letter or digit.
// Falls back to a prefix/suffix check for a needle within
// wholeWordFallbackMaxLengthGap characters of haystack's length, catching
// contraction-expansion edge cases ("believin" vs "believing") without also
// matching two genuinely different words.
func containsWholeWord(haystack, needle string) bool {
	if haystack == "" || needle == "" {
		return false
	}
	hr := []rune(haystack)
	nr := []rune(needle)
	for i := 0; i+len(nr) <= len(hr); i++ {
		if string(hr[i:i+len(nr)]) != needle {
			continue
		}
		beforeOK := i == 0 || !isLetterOrDigit(hr[i-1])
		afterOK := i+len(nr) == len(hr) || !isLetterOrDigit(hr[i+len(nr)])
		if beforeOK && afterOK {
			return true
		}
	}

	const wholeWordFallbackMaxLengthGap = 2
	if abs(len(hr)-len(nr)) > wholeWordFallbackMaxLengthGap {
		return false
	}
	return strings.HasPrefix(haystack, needle) || strings.HasSuffix(haystack, needle)
}

func isLetterOrDigit(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// MULTI_ARTIST_SEPARATOR_PATTERN: &, comma, semicolon, slash, or the word
// "and" - the semicolon matters because it's Plex's own convention for a
// multi-artist originalTitle (see matching.ts for the full rationale).
var multiArtistSeparatorPattern = regexp.MustCompile(`(?i)\s*(?:&|,|;|/|\band\b)\s*`)

// splitArtists splits a possibly multi-artist credit string into individual
// names.
func splitArtists(artist string) []string {
	if artist == "" {
		return nil
	}
	parts := multiArtistSeparatorPattern.Split(artist, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// stripFeaturedArtists removes a trailing "feat./featuring X" credit when
// settings.IgnoreFeaturedArtists is on.
func stripFeaturedArtists(artist string, settings Settings) string {
	if artist == "" {
		return ""
	}
	cleaned := artist
	if settings.IgnoreFeaturedArtists {
		for _, pattern := range settings.FeaturedArtistPatterns {
			escaped := regexp.QuoteMeta(strings.TrimSuffix(pattern, "."))
			re := regexp.MustCompile(`(?i)\s+` + escaped + `\.?\s+.+$`)
			cleaned = re.ReplaceAllString(cleaned, "")
		}
	}
	cleaned = strings.TrimSpace(whitespacePattern.ReplaceAllString(cleaned, " "))
	if cleaned == "" {
		return artist
	}
	return cleaned
}

func cleanArtistName(artist string, settings Settings) string {
	if artist == "" {
		return ""
	}
	cleaned := artist
	if settings.UseFirstArtistOnly && multiArtistSeparatorPattern.MatchString(cleaned) {
		cleaned = strings.TrimSpace(multiArtistSeparatorPattern.Split(cleaned, 2)[0])
	}
	return stripFeaturedArtists(cleaned, settings)
}

// editionQualifierPatterns strips remaster/deluxe/anniversary/expanded/
// special-edition/bonus-track qualifiers, shared by getCoreTitle (drives the
// Plex search query) and cleanTrackTitle (drives comparison/scoring) so the
// two can never drift apart the way two independently-copied pattern lists
// would.
var editionQualifierPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\s*-\s*remaster(?:ed)?\s*\d{4}`),
	regexp.MustCompile(`(?i)\s*-\s*\d{4}\s*remaster(?:ed)?`),
	regexp.MustCompile(`(?i)\s*-\s*remaster(?:ed)?`),
	regexp.MustCompile(`(?i)\s*\(remaster(?:ed)?\s*\d{4}\)`),
	regexp.MustCompile(`(?i)\s*\(remaster(?:ed)?\)`),
	regexp.MustCompile(`(?i)\s*\[remaster(?:ed)?\s*\d{4}\]`),
	regexp.MustCompile(`(?i)\s*\[remaster(?:ed)?\]`),
	regexp.MustCompile(`(?i)\s+remaster(?:ed)?$`),
	regexp.MustCompile(`(?i)\s*-\s*deluxe\s*edition`),
	regexp.MustCompile(`(?i)\s*\(deluxe\s*edition\)`),
	regexp.MustCompile(`(?i)\s*\[deluxe\s*edition\]`),
	regexp.MustCompile(`(?i)\s*-\s*deluxe`),
	regexp.MustCompile(`(?i)\s*\(deluxe\)`),
	regexp.MustCompile(`(?i)\s*\[deluxe\]`),
	regexp.MustCompile(`(?i)\s+deluxe$`),
	regexp.MustCompile(`(?i)\s*-\s*\d{4}\s*edition`),
	regexp.MustCompile(`(?i)\s*\(\d{4}\s*edition\)`),
	regexp.MustCompile(`(?i)\s*\[\d{4}\s*edition\]`),
	regexp.MustCompile(`(?i)\s*-\s*anniversary\s*edition`),
	regexp.MustCompile(`(?i)\s*\(anniversary\s*edition\)`),
	regexp.MustCompile(`(?i)\s*\[anniversary\s*edition\]`),
	regexp.MustCompile(`(?i)\s*-\s*expanded\s*edition`),
	regexp.MustCompile(`(?i)\s*\(expanded\s*edition\)`),
	regexp.MustCompile(`(?i)\s*\[expanded\s*edition\]`),
	regexp.MustCompile(`(?i)\s*-\s*special\s*edition`),
	regexp.MustCompile(`(?i)\s*\(special\s*edition\)`),
	regexp.MustCompile(`(?i)\s*\[special\s*edition\]`),
	regexp.MustCompile(`(?i)\s*-\s*bonus\s*track`),
	regexp.MustCompile(`(?i)\s*\(bonus\s*track\)`),
	regexp.MustCompile(`(?i)\s*\[bonus\s*track\]`),
}

// movieTieInPattern matches a trailing "- From <Movie>" qualifier (e.g.
// Spotify's `Try Everything - From "Zootropolis"`) that Plex's own track
// titles normally omit.
var movieTieInPattern = regexp.MustCompile(`(?i)\s*-\s*from\s+.+$`)

var parenthesesPattern = regexp.MustCompile(`\([^)]*\)`)
var bracketsPattern = regexp.MustCompile(`\[[^\]]*\]`)

func stripEditionQualifiers(title string, settings Settings) string {
	cleaned := title
	for _, pattern := range editionQualifierPatterns {
		cleaned = pattern.ReplaceAllString(cleaned, "")
	}
	cleaned = movieTieInPattern.ReplaceAllString(cleaned, "")
	if settings.StripParentheses {
		cleaned = parenthesesPattern.ReplaceAllString(cleaned, "")
	}
	if settings.StripBrackets {
		cleaned = bracketsPattern.ReplaceAllString(cleaned, "")
	}
	return strings.TrimSpace(whitespacePattern.ReplaceAllString(cleaned, " "))
}

func getCoreTitle(title string, settings Settings) string {
	return stripEditionQualifiers(title, settings)
}

func cleanTrackTitle(title string, settings Settings) string {
	if cleaned := stripEditionQualifiers(title, settings); cleaned != "" {
		return cleaned
	}
	return title
}

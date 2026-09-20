package matching

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/drevilish/playlist-lab/internal/services/plex"
)

var (
	letterOrDigitOnlyPattern     = regexp.MustCompile(`[^\p{L}\p{N}]`)
	curlyApostrophePattern       = regexp.MustCompile(`[\x{2018}\x{2019}\x{201A}\x{201B}` + "`" + `\x{00B4}'` + "`" + `\x{FFFD}]`)
	nonWordDashPattern           = regexp.MustCompile(`[^\p{L}\p{N}_\s\-]`)
	nonWordDashApostrophePattern = regexp.MustCompile(`[^\p{L}\p{N}_\s\-']`)
)

// normalizeSearch/normalizeSearchKeepApostrophe port matching.ts's two
// query-normalization helpers used to build the literal-substring queries
// Plex's own search filters expect (as opposed to the fuzzy comparison
// normalizeForComparison does for scoring). The apostrophe-preserving
// variant exists because Plex's title/artist filters are a literal
// substring match: stripping the apostrophe changes the literal query, so a
// library that kept "Wan'na" as tagged needs a search that also keeps it.
func normalizeSearch(s string) string {
	s = stripCombiningMarks(nfkd(s))
	s = nfc(s)
	s = curlyApostrophePattern.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "/", " ")
	s = strings.ReplaceAll(s, ".", "")
	s = nonWordDashPattern.ReplaceAllString(s, " ")
	return strings.TrimSpace(whitespacePattern.ReplaceAllString(s, " "))
}

func normalizeSearchKeepApostrophe(s string) string {
	s = stripCombiningMarks(nfkd(s))
	s = nfc(s)
	s = curlyApostrophePattern.ReplaceAllString(s, "'")
	s = strings.ReplaceAll(s, "/", " ")
	s = strings.ReplaceAll(s, ".", "")
	s = nonWordDashApostrophePattern.ReplaceAllString(s, " ")
	return strings.TrimSpace(whitespacePattern.ReplaceAllString(s, " "))
}

// FindPlexCandidates runs the same multi-tier Plex search findBestMatch
// uses and returns the raw, unscored, unfiltered results - exported so a
// manual-rematch search endpoint (later phase) can retrieve candidates for
// a track the same way, rather than maintaining a second implementation.
// tierLog, if given, collects a one-line record of every search tier that
// ran and what it returned, so a failed match can be explained afterwards.
func FindPlexCandidates(track Track, client *plex.Client, libraryID string, settings Settings, tierLog *[]string) ([]plex.Track, error) {
	// Counts letters/digits in any script - restricted to [a-zA-Z0-9], this
	// guard would see a fully Japanese/Cyrillic/Korean title as having no
	// characters at all and never search Plex for it even once.
	titleWithoutPunctuation := letterOrDigitOnlyPattern.ReplaceAllString(track.Title, "")
	if len([]rune(titleWithoutPunctuation)) < 2 {
		return nil, nil
	}

	cleanedArtist := cleanArtistName(track.Artist, settings)
	coreTitle := getCoreTitle(track.Title, settings)

	searchTitle := normalizeSearch(coreTitle)
	searchArtist := normalizeSearch(cleanedArtist)
	searchTitleWithApostrophe := normalizeSearchKeepApostrophe(coreTitle)
	searchArtistWithApostrophe := normalizeSearchKeepApostrophe(cleanedArtist)
	originalTitle := normalizeSearch(track.Title)
	searchArtistNoHyphen := strings.TrimSpace(whitespacePattern.ReplaceAllString(strings.ReplaceAll(searchArtist, "-", " "), " "))

	var allResults []plex.Track
	seen := map[string]bool{}

	runTier := func(name string, search func() ([]plex.Track, error)) error {
		results, err := search()
		if err != nil {
			if _, ok := err.(*plex.AuthError); ok {
				return err
			}
			if tierLog != nil {
				*tierLog = append(*tierLog, name+" -> ERROR: "+err.Error())
			}
			return nil
		}
		before := len(allResults)
		for _, r := range results {
			if !seen[r.RatingKey] {
				seen[r.RatingKey] = true
				allResults = append(allResults, r)
			}
		}
		if tierLog != nil {
			*tierLog = append(*tierLog, name+" -> "+strconv.Itoa(len(results))+" found, "+strconv.Itoa(len(allResults)-before)+" new")
		}
		return nil
	}

	stillSearching := func() bool { return !HasGateWorthyCandidate(track, allResults, settings) }

	if strings.Contains(searchTitleWithApostrophe, "'") || strings.Contains(searchArtistWithApostrophe, "'") {
		if err := runTier("apostrophe-preserved", func() ([]plex.Track, error) {
			return client.SearchTrack("", libraryID, searchArtistWithApostrophe, searchTitleWithApostrophe)
		}); err != nil {
			return nil, err
		}
	}

	if stillSearching() {
		if err := runTier("filtered", func() ([]plex.Track, error) {
			return client.SearchTrack("", libraryID, searchArtist, searchTitle)
		}); err != nil {
			return nil, err
		}
	}

	if stillSearching() && originalTitle != searchTitle {
		if err := runTier("original-title", func() ([]plex.Track, error) {
			return client.SearchTrack("", libraryID, searchArtist, originalTitle)
		}); err != nil {
			return nil, err
		}
	}

	if stillSearching() && searchArtistNoHyphen != searchArtist {
		if err := runTier("artist-no-hyphen", func() ([]plex.Track, error) {
			return client.SearchTrack("", libraryID, searchArtistNoHyphen, searchTitle)
		}); err != nil {
			return nil, err
		}
	}

	titleWithoutParens := normalizeSearch(strings.TrimSpace(whitespacePattern.ReplaceAllString(parenthesesPattern.ReplaceAllString(coreTitle, ""), " ")))
	if stillSearching() && titleWithoutParens != "" && titleWithoutParens != searchTitle {
		if err := runTier("parenthetical-stripped", func() ([]plex.Track, error) {
			return client.SearchTrack("", libraryID, searchArtist, titleWithoutParens)
		}); err != nil {
			return nil, err
		}
	}

	if stillSearching() {
		hubQuery := cleanedArtist + " " + cleanTrackTitle(track.Title, settings)
		if err := runTier("hub-search", func() ([]plex.Track, error) {
			return client.SearchTrack(hubQuery, libraryID, "", "")
		}); err != nil {
			return nil, err
		}
	}

	return allResults, nil
}

package matching

import (
	"strings"

	"github.com/drevilish/playlist-lab/internal/services/plex"
)

// Track is the minimal source-track shape the matching engine scores
// against a Plex result - mirrors ExternalTrack from scrapers.ts.
type Track struct {
	Title  string
	Artist string
	Album  string
}

func titlesMatch(sourceTitle, plexTitle string, settings Settings) bool {
	cleanSource := normalizeForComparison(cleanTrackTitle(sourceTitle, settings))
	cleanPlex := normalizeForComparison(cleanTrackTitle(plexTitle, settings))
	if cleanSource == cleanPlex {
		return true
	}
	if strings.ReplaceAll(cleanSource, " ", "") == strings.ReplaceAll(cleanPlex, " ", "") {
		return true
	}
	return containsWholeWord(cleanSource, cleanPlex) || containsWholeWord(cleanPlex, cleanSource)
}

func artistsMatch(sourceArtist, plexArtist string, settings Settings) bool {
	cleanSource := normalizeForComparison(cleanArtistName(sourceArtist, settings))
	cleanPlex := normalizeForComparison(cleanArtistName(plexArtist, settings))
	if cleanSource == cleanPlex {
		return true
	}
	if strings.ReplaceAll(cleanSource, " ", "") == strings.ReplaceAll(cleanPlex, " ", "") {
		return true
	}
	return containsWholeWord(cleanSource, cleanPlex) || containsWholeWord(cleanPlex, cleanSource)
}

// anyArtistCreditMatches is the general N-vs-M multi-artist match: true if
// any individually-named credit on one side matches any on the other.
// Comparing two whole multi-artist blobs only works when the matching name
// happens to be first/last, since normalizeForComparison strips the
// separator punctuation between names - splitting both sides first has no
// such blind spot.
func anyArtistCreditMatches(sourceArtist string, plexArtistFields ...string) bool {
	sourceNames := splitArtists(sourceArtist)
	var plexNames []string
	for _, f := range plexArtistFields {
		plexNames = append(plexNames, splitArtists(f)...)
	}
	for _, a := range sourceNames {
		cleanA := normalizeForComparison(a)
		if cleanA == "" {
			continue
		}
		for _, b := range plexNames {
			cleanB := normalizeForComparison(b)
			if cleanB == "" {
				continue
			}
			if cleanA == cleanB || containsWholeWord(cleanB, cleanA) || containsWholeWord(cleanA, cleanB) {
				return true
			}
		}
	}
	return false
}

func isVariousArtistsAlbum(albumArtist string, settings Settings) bool {
	lower := strings.ToLower(albumArtist)
	for _, name := range settings.VariousArtistsNames {
		nameLower := strings.ToLower(name)
		if lower == nameLower || strings.Contains(lower, nameLower) {
			return true
		}
	}
	normalized := normalizeForComparison(albumArtist)
	return strings.Contains(normalized, "various") || strings.Contains(normalized, "compilation")
}

func isSoundtrackAlbum(albumArtist, albumName string) bool {
	a, n := strings.ToLower(albumArtist), strings.ToLower(albumName)
	return strings.Contains(n, "soundtrack") || strings.Contains(n, "ost") ||
		strings.Contains(a, "cast") || strings.Contains(a, "soundtrack")
}

// MatchGateResult is the single title/artist gate a Plex search result must
// clear to be treated as a real candidate for a source track.
type MatchGateResult struct {
	Passes                 bool
	TitleMatches           bool
	AlbumArtistMatches     bool
	TrackArtistMatches     bool
	AnyArtistMatches       bool
	HasDistinctTrackArtist bool
	ArtistGateMatches      bool
	IsCompilation          bool
	AllowTitleOnlyMatch    bool
}

// EvaluateMatchGate ports matching.ts's evaluateMatchGate(): a high score
// alone is not enough to accept a candidate, since a title match against a
// completely wrong artist still scores well (artist scoring has a floor of
// 70) - this is the artist/title gate every automatic match must clear
// first.
func EvaluateMatchGate(track Track, result plex.Track, settings Settings) MatchGateResult {
	plexTitle := result.Title
	albumArtist := result.GrandparentTitle
	albumName := result.ParentTitle
	trackArtist := result.OriginalTitle

	titleMatches := titlesMatch(track.Title, plexTitle, settings)
	albumArtistMatches := albumArtist != "" && artistsMatch(track.Artist, albumArtist, settings)
	trackArtistMatches := trackArtist != "" && artistsMatch(track.Artist, trackArtist, settings)
	anyArtistMatches := anyArtistCreditMatches(track.Artist, albumArtist, trackArtist)

	isCompilation := isVariousArtistsAlbum(albumArtist, settings) || isSoundtrackAlbum(albumArtist, albumName)
	hasDistinctTrackArtist := trackArtist != "" && normalizeForComparison(trackArtist) != normalizeForComparison(albumArtist)
	isCompilation = isCompilation || hasDistinctTrackArtist

	cleanSourceTitle := normalizeForComparison(cleanTrackTitle(track.Title, settings))
	cleanPlexTitle := normalizeForComparison(cleanTrackTitle(plexTitle, settings))
	exactTitleMatch := cleanSourceTitle == cleanPlexTitle
	allowTitleOnlyMatch := isCompilation && exactTitleMatch

	var artistGateMatches bool
	if hasDistinctTrackArtist {
		artistGateMatches = trackArtistMatches || anyArtistMatches
	} else {
		artistGateMatches = albumArtistMatches || trackArtistMatches || anyArtistMatches
	}

	passes := titleMatches && (artistGateMatches || allowTitleOnlyMatch)

	return MatchGateResult{
		Passes: passes, TitleMatches: titleMatches, AlbumArtistMatches: albumArtistMatches,
		TrackArtistMatches: trackArtistMatches, AnyArtistMatches: anyArtistMatches,
		HasDistinctTrackArtist: hasDistinctTrackArtist, ArtistGateMatches: artistGateMatches,
		IsCompilation: isCompilation, AllowTitleOnlyMatch: allowTitleOnlyMatch,
	}
}

// HasGateWorthyCandidate reports whether any of results would pass
// EvaluateMatchGate for track - used to decide whether an earlier, cheaper
// search tier already found something real, or a broader tier is worth the
// extra Plex round trip.
func HasGateWorthyCandidate(track Track, results []plex.Track, settings Settings) bool {
	for _, r := range results {
		if EvaluateMatchGate(track, r, settings).Passes {
			return true
		}
	}
	return false
}

// ScoredCandidate is the same score a candidate would get during a real
// import: calculateMatchScore() plus every version/compilation bonus and
// penalty findBestMatch() applies while picking a track's best match.
type ScoredCandidate struct {
	Score       float64
	RankScore   float64
	PlexTitle   string
	PlexArtist  string
	PlexAlbum   string
	PlexCodec   string
	PlexBitrate int
}

// ScorePlexCandidate ports matching.ts's scorePlexCandidate().
func ScorePlexCandidate(sourceTitle, sourceArtist string, result plex.Track, settings Settings) ScoredCandidate {
	plexTitle := result.Title
	albumArtist := result.GrandparentTitle
	albumName := result.ParentTitle
	trackArtist := result.OriginalTitle

	albumArtistMatches := albumArtist != "" && artistsMatch(sourceArtist, albumArtist, settings)
	trackArtistMatches := trackArtist != "" && artistsMatch(sourceArtist, trackArtist, settings)
	anyArtistMatches := anyArtistCreditMatches(sourceArtist, albumArtist, trackArtist)

	isCompilation := isVariousArtistsAlbum(albumArtist, settings) || isSoundtrackAlbum(albumArtist, albumName)
	hasDistinctTrackArtist := trackArtist != "" && normalizeForComparison(trackArtist) != normalizeForComparison(albumArtist)
	isCompilation = isCompilation || hasDistinctTrackArtist

	cleanSourceTitle := normalizeForComparison(cleanTrackTitle(sourceTitle, settings))
	cleanPlexTitle := normalizeForComparison(cleanTrackTitle(plexTitle, settings))
	exactTitleMatch := cleanSourceTitle == cleanPlexTitle
	allowTitleOnlyMatch := isCompilation && exactTitleMatch

	var plexArtist string
	switch {
	case isCompilation && trackArtist != "":
		plexArtist = trackArtist
	case albumArtistMatches:
		plexArtist = albumArtist
	case trackArtist != "":
		plexArtist = trackArtist
	default:
		plexArtist = albumArtist
	}

	score := calculateMatchScore(sourceTitle, sourceArtist, plexTitle, plexArtist, settings)

	var artistTrustedMatch bool
	if hasDistinctTrackArtist {
		artistTrustedMatch = trackArtistMatches || anyArtistMatches
	} else {
		artistTrustedMatch = albumArtistMatches || trackArtistMatches || anyArtistMatches
	}

	if isCompilation && !artistTrustedMatch {
		score -= 40
	}
	if allowTitleOnlyMatch && !trackArtistMatches && !anyArtistMatches {
		score -= 20
	}
	if !hasReRecordedIndicator(sourceTitle) && hasReRecordedIndicator(plexTitle) {
		score -= 50
	}
	if !hasSpeedModifiedIndicator(sourceTitle) && hasSpeedModifiedIndicator(plexTitle) {
		score -= 50
	}

	// A live/session album's individual track titles are usually just the
	// plain song name - the qualifier is tagged on the ALBUM title instead,
	// so checking plexTitle alone misses live-album tracks tagged this way.
	plexIsAlternateVersion := hasAlternateVersionIndicator(plexTitle) || hasAlternateVersionIndicator(albumName)
	plexIsRemix := hasRemixIndicator(plexTitle) || hasRemixIndicator(albumName)
	remixPenalty := 0.0
	if !hasRemixIndicator(sourceTitle) && plexIsRemix {
		remixPenalty = 30
	}
	alternateVersionPenalty := 0.0
	if !hasAlternateVersionIndicator(sourceTitle) && plexIsAlternateVersion {
		alternateVersionPenalty = 35
	}
	score -= max(remixPenalty, alternateVersionPenalty)

	if !hasDemoIndicator(sourceTitle) && hasDemoIndicator(plexTitle) {
		score -= 35
	}
	if (hasRemasterIndicator(plexTitle) || hasRemasterIndicator(albumName)) && !plexIsRemix {
		score += 5
	}

	normalizedAlbum := normalizeForComparison(albumName)
	normalizedTrack := normalizeForComparison(plexTitle)
	if normalizedAlbum != "" && normalizedTrack != "" && normalizedAlbum == normalizedTrack && artistTrustedMatch {
		score += 10
	}

	if settings.PreferNonCompilation {
		switch {
		case hasDistinctTrackArtist:
			if !trackArtistMatches && !anyArtistMatches && !allowTitleOnlyMatch {
				score -= 30
			}
		case albumArtistMatches:
			score += 50
		case isCompilation:
			score -= 30
		}
	}

	clamped := min(100, max(0, score))
	return ScoredCandidate{
		Score: clamped, RankScore: score, PlexTitle: plexTitle, PlexArtist: plexArtist, PlexAlbum: albumName,
		PlexCodec: result.Codec(), PlexBitrate: firstBitrate(result),
	}
}

func firstBitrate(t plex.Track) int {
	if len(t.Media) == 0 {
		return 0
	}
	return t.Media[0].Bitrate
}

func calculateMatchScore(sourceTitle, sourceArtist, plexTitle, plexArtist string, settings Settings) float64 {
	cleanSourceTitle := normalizeForComparison(cleanTrackTitle(sourceTitle, settings))
	cleanPlexTitle := normalizeForComparison(cleanTrackTitle(plexTitle, settings))

	var titleScore float64
	switch {
	case cleanSourceTitle == "" || cleanPlexTitle == "":
		// Two titles that both normalize away to nothing are not the same
		// track, they are two things that can't be compared - scoring them
		// 100 is how every non-Latin-script track used to match every other.
		titleScore = 0
	case cleanSourceTitle == cleanPlexTitle:
		titleScore = 100
	case containsWholeWord(cleanSourceTitle, cleanPlexTitle) || containsWholeWord(cleanPlexTitle, cleanSourceTitle):
		titleScore = 90
	default:
		sourceWords := strings.Fields(cleanSourceTitle)
		plexWords := strings.Fields(cleanPlexTitle)
		matches := 0
		for _, w := range sourceWords {
			for _, pw := range plexWords {
				if pw == w {
					matches++
					break
				}
			}
		}
		denom := max(len(sourceWords), len(plexWords))
		titleScore = roundf(float64(matches) / float64(denom) * 80)
	}

	artistScore := bestArtistScore(sourceArtist, plexArtist, settings)
	return roundf(titleScore*0.7 + artistScore*0.3)
}

// bestArtistScore is the artist-half of calculateMatchScore. With
// UseFirstArtistOnly on, it's a single first-vs-first comparison; off (the
// default), every individually-split credit on one side is compared against
// every one on the other and the best pairwise bucket wins, so a source or
// Plex credit listing several artists in a different order (or a
// compilation crediting a different one of the same names) still matches.
func bestArtistScore(sourceArtist, plexArtist string, settings Settings) float64 {
	if settings.UseFirstArtistOnly {
		cleanSource := normalizeForComparison(cleanArtistName(sourceArtist, settings))
		cleanPlex := normalizeForComparison(cleanArtistName(plexArtist, settings))
		return artistPairScore(cleanSource, cleanPlex)
	}

	sourceNames := normalizeList(splitArtists(sourceArtist), settings)
	plexNames := normalizeList(splitArtists(plexArtist), settings)
	if len(sourceNames) == 0 || len(plexNames) == 0 {
		return artistPairScore("", "")
	}

	best := 0.0
	for _, a := range sourceNames {
		for _, b := range plexNames {
			best = max(best, artistPairScore(a, b))
			if best == 100 {
				return best
			}
		}
	}
	return best
}

func normalizeList(names []string, settings Settings) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = normalizeForComparison(stripFeaturedArtists(n, settings))
	}
	return out
}

func artistPairScore(cleanSource, cleanPlex string) float64 {
	if cleanSource == cleanPlex {
		return 100
	}
	if containsWholeWord(cleanSource, cleanPlex) || containsWholeWord(cleanPlex, cleanSource) {
		return 90
	}
	return 70
}

// IsPreferredCandidate breaks a tie between two gate-passing candidates
// that scored identically (e.g. the same song on both a "Greatest Hits"
// reissue and the original album): prefer a Remastered edition, then
// whichever release is older. Never overrides a genuine score difference.
func IsPreferredCandidate(candidateResult plex.Track, candidate ScoredCandidate, currentResult plex.Track, current ScoredCandidate) bool {
	if candidate.RankScore != current.RankScore {
		return candidate.RankScore > current.RankScore
	}

	candidateRemastered := hasRemasterIndicator(candidateResult.Title) || hasRemasterIndicator(candidateResult.ParentTitle)
	currentRemastered := hasRemasterIndicator(currentResult.Title) || hasRemasterIndicator(currentResult.ParentTitle)
	if candidateRemastered != currentRemastered {
		return candidateRemastered
	}

	candidateYear := yearOrInfinity(candidateResult)
	currentYear := yearOrInfinity(currentResult)
	return candidateYear < currentYear
}

func yearOrInfinity(t plex.Track) int {
	if t.ParentYear != 0 {
		return t.ParentYear
	}
	if t.Year != 0 {
		return t.Year
	}
	return 1 << 30
}

func roundf(f float64) float64 {
	if f < 0 {
		return float64(int(f - 0.5))
	}
	return float64(int(f + 0.5))
}

package matching

import (
	"fmt"

	"github.com/drevilish/playlist-lab/internal/services/limiter"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

// PickEffectiveVariant scores result against every one of variants and
// keeps whichever one actually explains it: a gate-passing reading beats
// one that isn't, and among several passing (or several failing) readings
// the highest-scoring one wins. Shared by FindBestMatch and any future
// deemix-style acquisition path, so a Japanese loanword title/artist gets
// the same romanized-reading fallback everywhere.
func PickEffectiveVariant(variants []Track, result plex.Track, settings Settings) (Track, MatchGateResult) {
	effectiveTrack := variants[0]
	effectiveGate := EvaluateMatchGate(variants[0], result, settings)
	for _, variant := range variants[1:] {
		candidateGate := EvaluateMatchGate(variant, result, settings)
		better := (!effectiveGate.Passes && candidateGate.Passes) ||
			(effectiveGate.Passes == candidateGate.Passes &&
				ScorePlexCandidate(variant.Title, variant.Artist, result, settings).Score >
					ScorePlexCandidate(effectiveTrack.Title, effectiveTrack.Artist, result, settings).Score)
		if better {
			effectiveTrack = variant
			effectiveGate = candidateGate
		}
	}
	return effectiveTrack, effectiveGate
}

// RejectedCandidate is the highest-scoring candidate the artist gate turned
// away, if any - reported so ExplainNoMatch can say exactly why.
type RejectedCandidate struct {
	RatingKey   string
	PlexTitle   string
	AlbumArtist string
	TrackArtist string
	Album       string
	Score       float64
	Reason      string
}

// MatchAttempt records why a track ended up unmatched, captured while the
// decision is being made so it can be reported afterwards instead of
// having to be reproduced by re-running the import with debug logging on.
type MatchAttempt struct {
	Tiers          []string
	CandidateCount int
	TitleRejected  int
	ArtistRejected int
	BestRejected   *RejectedCandidate
}

func describeArtistGateFailure(track Track, result plex.Track, gate MatchGateResult) string {
	albumArtist := result.GrandparentTitle
	if albumArtist == "" {
		albumArtist = "(none)"
	}
	trackArtist := result.OriginalTitle
	if trackArtist == "" {
		trackArtist = "(none)"
	}
	trusted := fmt.Sprintf("no distinct track artist, so album artist (%q) is the track's artist", albumArtist)
	if gate.HasDistinctTrackArtist {
		trusted = fmt.Sprintf("Plex has a distinct track artist (%q), so its album artist (%q) is not trusted for this track", trackArtist, albumArtist)
	}
	reason := fmt.Sprintf("source artist %q matched neither (albumArtist=%v, trackArtist=%v, anyCredit=%v); %s",
		track.Artist, gate.AlbumArtistMatches, gate.TrackArtistMatches, gate.AnyArtistMatches, trusted)
	if gate.IsCompilation {
		reason += "; treated as a compilation but the title was not an exact match, so title-only matching did not apply"
	}
	return reason
}

// Match is a resolved candidate: the Plex track findBestMatch picked, and
// how confident that pick is.
type Match struct {
	RatingKey   string
	Score       float64
	PlexTitle   string
	PlexArtist  string
	PlexAlbum   string
	PlexCodec   string
	PlexBitrate int
}

// FindBestMatch runs the tiered Plex search (across kana-romanized variants,
// and - once those fail - kuromoji-style kanji variants, see kana.go) and
// picks the highest-scoring gate-passing candidate.
func FindBestMatch(track Track, client *plex.Client, libraryID string, settings Settings) (*Match, MatchAttempt, error) {
	var tiers []string
	attempt := MatchAttempt{Tiers: tiers}

	variants := buildKanaVariants(track)
	allResults, err := FindPlexCandidates(variants[0], client, libraryID, settings, &attempt.Tiers)
	if err != nil {
		return nil, attempt, err
	}
	seen := map[string]bool{}
	for _, r := range allResults {
		seen[r.RatingKey] = true
	}
	for _, variant := range variants[1:] {
		variantResults, err := FindPlexCandidates(variant, client, libraryID, settings, &attempt.Tiers)
		if err != nil {
			return nil, attempt, err
		}
		for _, r := range variantResults {
			if !seen[r.RatingKey] {
				seen[r.RatingKey] = true
				allResults = append(allResults, r)
			}
		}
	}

	alreadyPassing := false
	for _, r := range allResults {
		for _, v := range variants {
			if EvaluateMatchGate(v, r, settings).Passes {
				alreadyPassing = true
				break
			}
		}
		if alreadyPassing {
			break
		}
	}
	if !alreadyPassing {
		for _, variant := range buildKanjiVariants(track) {
			variantResults, err := FindPlexCandidates(variant, client, libraryID, settings, &attempt.Tiers)
			if err != nil {
				return nil, attempt, err
			}
			for _, r := range variantResults {
				if !seen[r.RatingKey] {
					seen[r.RatingKey] = true
					allResults = append(allResults, r)
				}
			}
			variants = append(variants, variant)
		}
	}

	attempt.CandidateCount = len(allResults)
	if len(allResults) == 0 {
		return nil, attempt, nil
	}

	var best *Match
	var bestResult plex.Track
	var bestScored ScoredCandidate

	for _, result := range allResults {
		effectiveTrack, effectiveGate := PickEffectiveVariant(variants, result, settings)

		if !effectiveGate.TitleMatches {
			attempt.TitleRejected++
			continue
		}
		if !effectiveGate.Passes {
			attempt.ArtistRejected++
			scored := ScorePlexCandidate(effectiveTrack.Title, effectiveTrack.Artist, result, settings)
			if attempt.BestRejected == nil || scored.Score > attempt.BestRejected.Score {
				attempt.BestRejected = &RejectedCandidate{
					RatingKey: result.RatingKey, PlexTitle: result.Title,
					AlbumArtist: result.GrandparentTitle, TrackArtist: result.OriginalTitle,
					Album: result.ParentTitle, Score: scored.Score,
					Reason: describeArtistGateFailure(effectiveTrack, result, effectiveGate),
				}
			}
			continue
		}

		scored := ScorePlexCandidate(effectiveTrack.Title, effectiveTrack.Artist, result, settings)
		if best == nil || IsPreferredCandidate(result, scored, bestResult, bestScored) {
			best = &Match{
				RatingKey: result.RatingKey, Score: scored.Score, PlexTitle: scored.PlexTitle,
				PlexArtist: scored.PlexArtist, PlexAlbum: scored.PlexAlbum, PlexCodec: scored.PlexCodec, PlexBitrate: scored.PlexBitrate,
			}
			bestResult = result
			bestScored = scored
		}
	}

	return best, attempt, nil
}

// ExplainNoMatch spells out, in the terms the gate actually uses, why a
// title-matching candidate was still rejected - so automatic matching's log
// says which artist comparison failed rather than just "artist mismatch".
// The important case is the last one: automatic matching applies the
// artist gate on top of the score, while a manual-rematch search only
// scores - so a candidate can legitimately read 100% there and still be
// refused automatically.
func ExplainNoMatch(attempt MatchAttempt, match *Match, minScore float64) string {
	if attempt.CandidateCount == 0 {
		return "No candidates: every search tier came back empty, so this track is probably not in the library under any searchable spelling."
	}
	if match != nil {
		return fmt.Sprintf("Best gate-passing candidate %q - %q scored %.0f%%, below the %.0f%% minimum.", match.PlexArtist, match.PlexTitle, match.Score, minScore)
	}
	if attempt.BestRejected != nil {
		r := attempt.BestRejected
		if r.Score >= minScore {
			return fmt.Sprintf("Title matched but the artist gate rejected every candidate. Best was %q - %q (album %q) scoring %.0f%% - at or above the %.0f%% minimum, so Manual Match WILL show this as a match. Manual Match only scores; automatic matching also requires the artist gate, which failed: %s.",
				r.AlbumArtist, r.PlexTitle, r.Album, r.Score, minScore, r.Reason)
		}
		return fmt.Sprintf("Title matched but the artist gate rejected every candidate. Best was %q - %q (album %q) scoring %.0f%%, and also below the %.0f%% minimum. Gate failure: %s.",
			r.AlbumArtist, r.PlexTitle, r.Album, r.Score, minScore, r.Reason)
	}
	return fmt.Sprintf("%d candidate(s) found but none had a matching title.", attempt.CandidateCount)
}

// MatchPlaylist caps how many full matching runs are in flight at once
// app-wide (see the limiter package) - every caller (import, deemix,
// lidarr, missing-track retry, cross-import, ...) should go through this
// one function rather than each capping itself locally.
func MatchPlaylist(tracks []Track, client *plex.Client, libraryID string, settings Settings, progress func(processed, total int), isCancelled func() bool, remembered map[string]string) ([]MatchedTrack, error) {
	return limiter.Run(limiter.Matching, func() ([]MatchedTrack, error) {
		return matchPlaylistImpl(tracks, client, libraryID, settings, progress, isCancelled, remembered)
	})
}

func matchPlaylistImpl(tracks []Track, client *plex.Client, libraryID string, settings Settings, progress func(processed, total int), isCancelled func() bool, remembered map[string]string) ([]MatchedTrack, error) {
	// minMatchScore is stored as a fraction (<=1) or already a 0-100
	// percentage - matching.ts scales the fraction form up once per run
	// rather than mutating the caller's shared settings.
	effectiveSettings := settings
	if effectiveSettings.MinMatchScore <= 1 {
		effectiveSettings.MinMatchScore *= 100
	}

	const batchSize = 5
	matched := make([]MatchedTrack, len(tracks))
	processed := 0

	for i := 0; i < len(tracks); i += batchSize {
		if isCancelled != nil && isCancelled() {
			return nil, fmt.Errorf("import cancelled by user")
		}
		end := min(i+batchSize, len(tracks))
		for j := i; j < end; j++ {
			track := tracks[j]
			result, err := matchOneTrack(track, client, libraryID, effectiveSettings, remembered)
			if err != nil {
				if _, ok := err.(*plex.AuthError); ok {
					return nil, err
				}
				matched[j] = MatchedTrack{Title: track.Title, Artist: track.Artist, Album: track.Album}
				continue
			}
			matched[j] = result
		}
		processed = end
		if progress != nil {
			progress(processed, len(tracks))
		}
	}

	return matched, nil
}

func matchOneTrack(track Track, client *plex.Client, libraryID string, settings Settings, remembered map[string]string) (MatchedTrack, error) {
	if remembered != nil {
		if ratingKey, ok := remembered[RememberedMatchKey(track.Title, track.Artist, track.Album)]; ok {
			details, err := client.GetTrackDetails(ratingKey)
			if err != nil {
				if _, ok := err.(*plex.AuthError); ok {
					return MatchedTrack{}, err
				}
			}
			if details != nil {
				return MatchedTrack{
					Title: track.Title, Artist: track.Artist, Album: track.Album, Matched: true,
					PlexRatingKey: ratingKey, PlexTitle: details.Title, PlexArtist: details.DisplayArtist(),
					PlexAlbum: details.ParentTitle, PlexCodec: details.Codec(), PlexBitrate: firstBitrate(*details), Score: 100,
				}, nil
			}
			// Remembered match no longer exists in Plex - fall through to a
			// normal search instead.
		}
	}

	match, _, err := FindBestMatch(track, client, libraryID, settings)
	if err != nil {
		return MatchedTrack{}, err
	}
	if match == nil || match.Score < settings.MinMatchScore {
		return MatchedTrack{Title: track.Title, Artist: track.Artist, Album: track.Album}, nil
	}
	return MatchedTrack{
		Title: track.Title, Artist: track.Artist, Album: track.Album, Matched: true,
		PlexRatingKey: match.RatingKey, PlexTitle: match.PlexTitle, PlexArtist: match.PlexArtist,
		PlexAlbum: match.PlexAlbum, PlexCodec: match.PlexCodec, PlexBitrate: match.PlexBitrate, Score: match.Score,
	}, nil
}

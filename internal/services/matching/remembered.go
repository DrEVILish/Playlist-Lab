package matching

import (
	"strconv"
	"strings"
)

// RememberedMatch mirrors a user's saved manual match row (database.ts's
// ManualMatch shape).
type RememberedMatch struct {
	Title         string
	Artist        string
	Album         string
	PlexRatingKey string
}

// RememberedMatchKey is the same (title, artist, album) key
// addMissingTracks()/recordManualMatch() dedupe on - kept as one function so
// building the map and looking it up can never use two different notions of
// "the same track".
// Fields are joined length-prefixed rather than with a fixed "|||"
// separator: a title/artist containing "|||" itself used to be able to
// make two distinct (title, artist, album) triples collide onto the same
// key (e.g. ("a|||b", "c", "") and ("a", "b|||c", "") both produced
// "a|||b|||c|||"), silently applying one track's remembered match to a
// different one. Prefixing each field with its own byte length makes the
// encoding unambiguous regardless of what characters the fields contain.
func RememberedMatchKey(title, artist, album string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	a := strings.ToLower(strings.TrimSpace(artist))
	b := strings.ToLower(strings.TrimSpace(album))
	return strconv.Itoa(len(t)) + ":" + t + strconv.Itoa(len(a)) + ":" + a + strconv.Itoa(len(b)) + ":" + b
}

// BuildRememberedMatchMap builds matchPlaylist()'s remembered-match lookup
// from a user's saved manual matches.
func BuildRememberedMatchMap(manualMatches []RememberedMatch) map[string]string {
	m := make(map[string]string, len(manualMatches))
	for _, match := range manualMatches {
		m[RememberedMatchKey(match.Title, match.Artist, match.Album)] = match.PlexRatingKey
	}
	return m
}

// MatchedTrack is one source track's matching outcome.
type MatchedTrack struct {
	Title         string
	Artist        string
	Album         string
	Matched       bool
	PlexRatingKey string
	PlexTitle     string
	PlexArtist    string
	PlexAlbum     string
	PlexCodec     string
	PlexBitrate   int
	Score         float64
}

// DedupeByPlexRatingKey removes later duplicates that resolved to the same
// Plex track (a gate/scoring false-positive can resolve two distinct source
// tracks to the same Plex track) - callers building a playlist's track URI
// list should run matched tracks through this first so that doesn't turn
// into a silent duplicate in the playlist.
func DedupeByPlexRatingKey(tracks []MatchedTrack) []MatchedTrack {
	seen := make(map[string]bool, len(tracks))
	out := make([]MatchedTrack, 0, len(tracks))
	for _, t := range tracks {
		if t.PlexRatingKey == "" {
			out = append(out, t)
			continue
		}
		if seen[t.PlexRatingKey] {
			continue
		}
		seen[t.PlexRatingKey] = true
		out = append(out, t)
	}
	return out
}

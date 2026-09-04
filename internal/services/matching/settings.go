// Package matching ports services/matching.ts's track-matching/scoring
// engine to Go: deciding whether a Plex search result is the same song as a
// source track, and how confident that decision is. This file only covers
// the fields the scoring engine itself reads (minMatchScore,
// stripParentheses/Brackets, useFirstArtistOnly, ignoreFeaturedArtists +
// its patterns, preferNonCompilation, variousArtistsNames) - the remaining
// database/types.ts MatchingSettings fields (playlist prefixes, rating
// preferences, ...) belong to routes/services this Go port hasn't reached
// yet and can be added to this struct when those land.
package matching

// Settings mirrors database/types.ts's MatchingSettings.
type Settings struct {
	MinMatchScore          float64
	StripParentheses       bool
	StripBrackets          bool
	UseFirstArtistOnly     bool
	IgnoreFeaturedArtists  bool
	FeaturedArtistPatterns []string
	PreferNonCompilation   bool
	VariousArtistsNames    []string
}

// DefaultSettings mirrors the Node server's DEFAULT_MATCHING_SETTINGS
// defaults for the fields this package uses. MinMatchScore is a fraction
// (0.8) here, same as the stored setting - matchPlaylist's caller scales it
// to a 0-100 percentage before scoring, exactly like matchPlaylistImpl does.
func DefaultSettings() Settings {
	return Settings{
		MinMatchScore:          0.8,
		StripParentheses:       true,
		StripBrackets:          true,
		UseFirstArtistOnly:     false,
		IgnoreFeaturedArtists:  true,
		FeaturedArtistPatterns: []string{"feat.", "ft.", "featuring"},
		PreferNonCompilation:   true,
		VariousArtistsNames:    []string{"Various Artists", "Various", "VA"},
	}
}

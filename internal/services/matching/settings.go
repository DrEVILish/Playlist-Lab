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

import "encoding/json"

// Settings mirrors database/types.ts's MatchingSettings. JSON tags match
// the field names the Node server already stores in
// user_settings.matching_settings, so SettingsFromJSON can unmarshal that
// column directly without a translation layer.
type Settings struct {
	MinMatchScore          float64  `json:"minMatchScore"`
	StripParentheses       bool     `json:"stripParentheses"`
	StripBrackets          bool     `json:"stripBrackets"`
	UseFirstArtistOnly     bool     `json:"useFirstArtistOnly"`
	IgnoreFeaturedArtists  bool     `json:"ignoreFeaturedArtists"`
	FeaturedArtistPatterns []string `json:"featuredArtistPatterns"`
	PreferNonCompilation   bool     `json:"preferNonCompilation"`
	VariousArtistsNames    []string `json:"variousArtistsNames"`
}

// SettingsFromJSON parses a user_settings.matching_settings JSON blob (as
// returned by db.GetMatchingSettingsJSON), falling back to DefaultSettings
// when raw is empty or fails to parse - matching database.ts's
// getUserSettings().matching_settings || DEFAULT_MATCHING_SETTINGS pattern.
func SettingsFromJSON(raw string) Settings {
	settings := DefaultSettings()
	if raw == "" {
		return settings
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return DefaultSettings()
	}
	return settings
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

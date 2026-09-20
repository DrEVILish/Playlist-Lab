// Package medialist is the common item shape every external list/chart
// provider (tmdb, imdb, tvdb, letterboxd) normalizes into, so Collections'
// external-list builder (DESIGN.md §11.11) can match against a Plex
// library with one generic code path instead of one per provider.
package medialist

// Item is one external list/chart entry. GuidKey is the exact string
// Plex's own Guid array carries for a matching library item - e.g.
// "tmdb://11974", "imdb://tt0096734", "tvdb://5869" (confirmed live against
// a real Plex server: these are the three external-agent prefixes Plex
// exposes via includeGuids=1). Letterboxd has no such id of its own, so its
// provider resolves each entry to the equivalent "tmdb://..." key instead
// of inventing an unmatchable "letterboxd://" one.
type Item struct {
	GuidKey   string
	MediaType string // "movie" | "tv" - empty when the provider can't say
	Title     string
	Year      int
}

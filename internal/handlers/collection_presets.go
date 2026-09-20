// collection_presets.go is a "Browse Presets" gallery for the New
// Collection / New Dynamic Collection Set modals, built from a real
// survey of github.com/Kometa-Team/Community-Configs rather than invented
// examples: dozens of real users' configs were pulled and grepped for
// their actual collections: blocks. Three patterns showed up repeatedly
// enough to be worth a one-click preset:
//
//  1. Per-library breakdowns (Genre/Decade/Actor/Director/Writer/Studio/
//     Content Rating) - these need zero curated data, since this app's
//     existing Dynamic Collections feature already generates them straight
//     from whatever's actually in a user's own library.
//  2. Award-show-by-year collections, near-universally sourced (across 15+
//     different contributors' configs) from one curator's Trakt lists -
//     trakt.tv/users/pjcob/lists/<year>-oscars and siblings for Golden
//     Globes/BAFTA/Cannes/Critics Choice/Independent Spirit Awards/
//     Sundance (hjone72/Oscars.yml, GoldenGlobes.yml, BAFTA.yml, ISA.yml,
//     Cannes.yml, Critics_Choice.yml, Sundance.yml) - the direct answer to
//     "separate for each award... from multiple years": one preset per
//     award show, parameterized by year, exactly mirroring how Kometa's
//     own config templates do it (a `template: {name: Oscars, year: X}`).
//  3. Holiday collections sourced from Kometa maintainer meisnate12's own
//     IMDb lists (meisnate12/Holiday.yml), and a handful of franchise
//     collections sourced from real TMDb collection ids referenced in that
//     same file's Halloween Movies entry.
//
// Deliberately NOT included: dozens of plausible-looking TMDb franchise
// collection ids or additional award shows this session couldn't verify
// against the actual repo or a live API call - a wrong id would silently
// build the wrong collection, which is worse than a shorter, trustworthy
// list. This is a plain Go slice specifically so it's trivial to grow
// later (each entry is independent, no shared state to break).
package handlers

import "strings"

type collectionPreset struct {
	Slug        string
	Category    string // "Library Breakdown" | "Awards" | "Holiday" | "Franchise"
	Name        string
	Description string
	Source      string // one line citing where this came from, shown in the gallery

	// Kind picks which create form this preset targets: "dynamic" opens
	// New Dynamic Collection Set pre-selecting FacetType; "list" opens New
	// Collection pre-selecting the External List/Chart builder with
	// Provider/Mode/ListID.
	Kind      string
	FacetType string // dynamic only

	Provider  string // list only: "trakt" | "imdb" | "tmdb"
	Mode      string // list only: "list" | "collection"
	ListID    string // list only - "<<year>>" is replaced with the picker's year input, if NeedsYear
	NeedsYear bool

	// MediaType is "movie" | "tv" | "" (unknown/mixed) - list only, every
	// entry below happens to be movie-only (award shows, Kometa's own
	// Holiday/Franchise lists) since no TV-sourced preset exists yet, but
	// this is what auto-selects a matching library in New Collection's
	// picker (newForm) rather than leaving a user to find "Movies" (or
	// "Films", or whatever they happened to name it) themselves every time.
	MediaType string
}

var collectionPresets = []collectionPreset{
	// --- Library Breakdown (Dynamic Collections - zero curated data) ---
	{Slug: "genres", Category: "Library Breakdown", Kind: "dynamic", FacetType: "genre",
		Name: "Genres", Description: "One collection per genre already in this library.",
		Source: "Matches Kometa's own genre dynamic_collections pattern."},
	{Slug: "decades", Category: "Library Breakdown", Kind: "dynamic", FacetType: "decade",
		Name: "Decades", Description: "One collection per decade (1980s, 1990s, ...).",
		Source: "Matches Kometa's own decade dynamic_collections pattern."},
	{Slug: "actors", Category: "Library Breakdown", Kind: "dynamic", FacetType: "actor",
		Name: "Actors", Description: "One collection per actor with enough films in this library.",
		Source: "fscorrupt/actors.yml"},
	{Slug: "directors", Category: "Library Breakdown", Kind: "dynamic", FacetType: "director",
		Name: "Directors", Description: "One collection per director (Christopher Nolan, ...).",
		Source: "fscorrupt/directors.yml"},
	{Slug: "writers", Category: "Library Breakdown", Kind: "dynamic", FacetType: "writer",
		Name: "Writers", Description: "One collection per writer (Aaron Sorkin, ...).",
		Source: "fscorrupt/directors.yml's Writer template"},
	{Slug: "studios", Category: "Library Breakdown", Kind: "dynamic", FacetType: "studio",
		Name: "Studios & Networks", Description: "One collection per studio (A24, Pixar, ...) or network.",
		Source: "fscorrupt/movies-studios.yml"},
	{Slug: "content-ratings", Category: "Library Breakdown", Kind: "dynamic", FacetType: "content_rating",
		Name: "Content Ratings", Description: "One collection per content rating (PG-13, R, ...).",
		Source: "Matches Kometa's own content_rating dynamic_collections pattern."},

	// --- Awards (Trakt, year-parameterized - pjcob's yearly lists) ---
	{Slug: "oscars", Category: "Awards", Kind: "list", Provider: "trakt", Mode: "list", NeedsYear: true, MediaType: "movie",
		ListID: "https://trakt.tv/users/pjcob/lists/<<year>>-oscars",
		Name:   "Academy Awards (Oscars)", Description: "Pick a year - creates \"Oscars <year>\".",
		Source: "hjone72/Oscars.yml"},
	{Slug: "oscars-best-picture-winners", Category: "Awards", Kind: "list", Provider: "trakt", Mode: "list", MediaType: "movie",
		ListID: "https://trakt.tv/users/pjcob/lists/1970-2021-oscars-best-picture-winners",
		Name:   "Oscars Best Picture Winners (1970-2021)", Description: "Every Best Picture winner in one collection.",
		Source: "hjone72/Oscars.yml"},
	{Slug: "golden-globes", Category: "Awards", Kind: "list", Provider: "trakt", Mode: "list", NeedsYear: true, MediaType: "movie",
		ListID: "https://trakt.tv/users/pjcob/lists/<<year>>-golden-globes",
		Name:   "Golden Globes", Description: "Pick a year - creates \"Golden Globes <year>\".",
		Source: "hjone72/GoldenGlobes.yml"},
	{Slug: "bafta", Category: "Awards", Kind: "list", Provider: "trakt", Mode: "list", NeedsYear: true, MediaType: "movie",
		ListID: "https://trakt.tv/users/pjcob/lists/<<year>>-bafta",
		Name:   "BAFTA", Description: "Pick a year - creates \"BAFTA <year>\".",
		Source: "hjone72/BAFTA.yml"},
	{Slug: "cannes", Category: "Awards", Kind: "list", Provider: "trakt", Mode: "list", NeedsYear: true, MediaType: "movie",
		ListID: "https://trakt.tv/users/pjcob/lists/<<year>>-cannes",
		Name:   "Cannes Film Festival", Description: "Pick a year - creates \"Cannes <year>\".",
		Source: "hjone72/Cannes.yml"},
	{Slug: "critics-choice", Category: "Awards", Kind: "list", Provider: "trakt", Mode: "list", NeedsYear: true, MediaType: "movie",
		ListID: "https://trakt.tv/users/pjcob/lists/<<year>>-critic-s-choice",
		Name:   "Critics' Choice Awards", Description: "Pick a year - creates \"Critics' Choice <year>\".",
		Source: "hjone72/Critics_Choice.yml"},
	{Slug: "independent-spirit", Category: "Awards", Kind: "list", Provider: "trakt", Mode: "list", NeedsYear: true, MediaType: "movie",
		ListID: "https://trakt.tv/users/pjcob/lists/<<year>>-independent-spirits",
		Name:   "Independent Spirit Awards", Description: "Pick a year - creates \"Independent Spirit Awards <year>\".",
		Source: "hjone72/ISA.yml"},
	{Slug: "sundance", Category: "Awards", Kind: "list", Provider: "trakt", Mode: "list", NeedsYear: true, MediaType: "movie",
		ListID: "https://trakt.tv/users/pjcob/lists/<<year>>-sundance",
		Name:   "Sundance Film Festival", Description: "Pick a year - creates \"Sundance <year>\".",
		Source: "hjone72/Sundance.yml"},

	// --- Holiday (IMDb lists) ---
	{Slug: "christmas", Category: "Holiday", Kind: "list", Provider: "imdb", Mode: "list", MediaType: "movie",
		ListID: "https://www.imdb.com/list/ls000096828/",
		Name:   "Christmas Movies", Description: "", Source: "meisnate12/Holiday.yml"},
	{Slug: "halloween", Category: "Holiday", Kind: "list", Provider: "imdb", Mode: "list", MediaType: "movie",
		ListID: "https://www.imdb.com/list/ls023118929/",
		Name:   "Halloween Movies", Description: "", Source: "meisnate12/Holiday.yml"},
	{Slug: "thanksgiving", Category: "Holiday", Kind: "list", Provider: "imdb", Mode: "list", MediaType: "movie",
		ListID: "https://www.imdb.com/list/ls000835734/",
		Name:   "Thanksgiving Movies", Description: "", Source: "meisnate12/Holiday.yml"},
	{Slug: "valentines-day", Category: "Holiday", Kind: "list", Provider: "imdb", Mode: "list", MediaType: "movie",
		ListID: "https://www.imdb.com/list/ls000094398/",
		Name:   "Valentine's Day Movies", Description: "", Source: "meisnate12/Holiday.yml"},
	{Slug: "st-patricks-day", Category: "Holiday", Kind: "list", Provider: "imdb", Mode: "list", MediaType: "movie",
		ListID: "https://www.imdb.com/list/ls063934595/",
		Name:   "St. Patrick's Day Movies", Description: "", Source: "meisnate12/Holiday.yml"},
	{Slug: "new-years-day", Category: "Holiday", Kind: "list", Provider: "imdb", Mode: "list", MediaType: "movie",
		ListID: "https://www.imdb.com/list/ls066838460/",
		Name:   "New Year's Day Movies", Description: "", Source: "meisnate12/Holiday.yml"},

	// --- Franchise (TMDb collection ids) ---
	{Slug: "halloween-collection", Category: "Franchise", Kind: "list", Provider: "tmdb", Mode: "collection", MediaType: "movie",
		ListID: "91361", Name: "Halloween Collection", Description: "", Source: "meisnate12/Holiday.yml"},
	{Slug: "nightmare-on-elm-street", Category: "Franchise", Kind: "list", Provider: "tmdb", Mode: "collection", MediaType: "movie",
		ListID: "8581", Name: "A Nightmare on Elm Street Collection", Description: "", Source: "meisnate12/Holiday.yml"},
	{Slug: "mummy-collection", Category: "Franchise", Kind: "list", Provider: "tmdb", Mode: "collection", MediaType: "movie",
		ListID: "1733", Name: "The Mummy Collection", Description: "", Source: "meisnate12/Holiday.yml"},
	{Slug: "alien-collection", Category: "Franchise", Kind: "list", Provider: "tmdb", Mode: "collection", MediaType: "movie",
		ListID: "8091", Name: "Alien Collection", Description: "", Source: "meisnate12/Holiday.yml"},
}

func findCollectionPreset(slug string) *collectionPreset {
	for i := range collectionPresets {
		if collectionPresets[i].Slug == slug {
			return &collectionPresets[i]
		}
	}
	return nil
}

// presetsByCategory groups the registry for the gallery template in a
// stable, deliberate order (Library Breakdown first - it's the one every
// user can use immediately with no external service - then Awards/Holiday/
// Franchise), rather than a map's undefined iteration order.
func presetsByCategory() []struct {
	Category string
	Presets  []collectionPreset
} {
	order := []string{"Library Breakdown", "Awards", "Holiday", "Franchise"}
	out := make([]struct {
		Category string
		Presets  []collectionPreset
	}, 0, len(order))
	for _, cat := range order {
		var presets []collectionPreset
		for _, p := range collectionPresets {
			if p.Category == cat {
				presets = append(presets, p)
			}
		}
		out = append(out, struct {
			Category string
			Presets  []collectionPreset
		}{Category: cat, Presets: presets})
	}
	return out
}

// resolvedListID substitutes a year-parameterized preset's "<<year>>"
// placeholder (Kometa's own convention, matching the rest of this app's
// Dynamic Collections title format) with the picker's chosen year.
func (p collectionPreset) resolvedListID(year string) string {
	if !p.NeedsYear || year == "" {
		return p.ListID
	}
	return strings.ReplaceAll(p.ListID, "<<year>>", year)
}

func (p collectionPreset) resolvedName(year string) string {
	if !p.NeedsYear || year == "" {
		return p.Name
	}
	return p.Name + " " + year
}

// librarysearch.go generalizes discovery.go's GetTracksWithAdvancedFilters
// (music-only) to movie/show libraries too, for Collections' "smart"
// rule-based builder (DESIGN.md §11.11). GetTracksWithAdvancedFilters
// itself is untouched - this is a deliberately smaller, curated-field
// sibling, not a replacement: no general boolean-expression DSL, just a
// flat list of fields AND-combined together, matching what's realistically
// buildable as a plain HTML form.
package plex

import (
	"net/url"
	"strconv"
	"strings"
)

// LibraryFilterOptions is the movie/show/music-generalized sibling of
// AdvancedFilterOptions (discovery.go). Genres/Moods/Styles/Actors are each
// OR'd together internally (any matching value), never across fields.
type LibraryFilterOptions struct {
	Genres         []string
	YearFrom       int
	YearTo         int      // YearFrom==YearTo expresses a single year; a decade is YearFrom=1990,YearTo=1999
	ContentRatings []string // movie only
	Studios        []string // movie studio, or show network (Plex has no separate network field)
	Actors         []string
	Directors      []string
	Writers        []string
	Unwatched      bool
	Moods          []string // music only
	Styles         []string // music only
	Limit          int
}

// SearchLibraryItems is GetTracksWithAdvancedFilters generalized across
// item types. itemType: 1=movie, 2=show, 10=track - music collections
// operate at track level in v1, matching this app's existing track-centric
// model rather than album/artist level.
func (c *Client) SearchLibraryItems(libraryID string, itemType int, opts LibraryFilterOptions) ([]Track, error) {
	var filters []string

	if opts.YearFrom > 0 {
		// Single >=/<= (not doubled >>=/<<=, unlike discovery.go's
		// parentYear filters used to before this same fix) - confirmed live
		// against a real movie library: the doubled form silently returns
		// zero results when left unescaped (Go's URL handling mangles raw
		// '>>'/'<<' in a path) and gives different, smaller result counts
		// even when percent-escaped, while the single form both matches
		// this file's own addedAt/lastViewedAt/userRating filters below and
		// is confirmed correct against real data (year>=X&year<=X yields
		// the same results as an exact year= match).
		filters = append(filters, "year>="+strconv.Itoa(opts.YearFrom))
	}
	if opts.YearTo > 0 {
		filters = append(filters, "year<="+strconv.Itoa(opts.YearTo))
	}
	if opts.Unwatched {
		// viewCount=0 is the same never-played filter already proven out by
		// GetStalePlayedTracks' neverPath (discovery.go).
		filters = append(filters, "viewCount=0")
	}
	switch {
	case len(opts.ContentRatings) == 1:
		filters = append(filters, "contentRating="+url.QueryEscape(opts.ContentRatings[0]))
	case len(opts.ContentRatings) > 1:
		filters = append(filters, "push=1", "contentRating="+url.QueryEscape(opts.ContentRatings[0]))
		for _, r := range opts.ContentRatings[1:] {
			filters = append(filters, "or=1", "contentRating="+url.QueryEscape(r))
		}
		filters = append(filters, "pop=1")
	}

	path := "/library/sections/" + url.PathEscape(libraryID) + "/all?type=" + strconv.Itoa(itemType)
	if len(filters) > 0 {
		path += "&" + strings.Join(filters, "&")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 1000
	}
	path += "&X-Plex-Container-Size=" + strconv.Itoa(limit)

	mc, err := c.get(path)
	if err != nil {
		return nil, err
	}
	items, err := decodeTracks(mc.MediaContainer.Metadata)
	if err != nil {
		return nil, err
	}

	items = filterByTags(items, opts.Genres, nil, func(t Track) []Tag { return t.Genre })
	items = filterByTags(items, opts.Moods, nil, func(t Track) []Tag { return t.Mood })
	items = filterByTags(items, opts.Styles, nil, func(t Track) []Tag { return t.Style })
	items = filterByTags(items, opts.Actors, nil, func(t Track) []Tag { return t.Role })
	items = filterByTags(items, opts.Directors, nil, func(t Track) []Tag { return t.Director })
	items = filterByTags(items, opts.Writers, nil, func(t Track) []Tag { return t.Writer })

	if len(opts.Studios) > 0 {
		studios := lowerAll(opts.Studios)
		items = filterSlice(items, func(t Track) bool {
			return containsAnySubstring(studios, strings.ToLower(t.Studio))
		})
	}

	return items, nil
}

// GetLibraryItemsWithGuids fetches an entire library section's items with
// their external agent GUIDs included (includeGuids=1 - Plex omits the Guid
// array by default, confirmed live). Used by Collections' external-list
// builder (DESIGN.md §11.11) to build a guid -> ratingKey lookup for
// matching a TMDb/IMDb/TVDb list/chart's entries against what's actually in
// the library. Pages through the whole section (X-Plex-Container-Start/
// -Size) rather than one fixed-size request - a library past the old
// single-request cap silently had everything beyond it invisible to
// matching, misreporting genuinely-owned items as missing.
func (c *Client) GetLibraryItemsWithGuids(libraryID string, itemType int) ([]Track, error) {
	const pageSize = 1000
	path := "/library/sections/" + url.PathEscape(libraryID) + "/all?type=" + strconv.Itoa(itemType) + "&includeGuids=1"
	var all []Track
	for start := 0; ; start += pageSize {
		mc, err := c.getWithHeaders(path, map[string]string{
			"X-Plex-Container-Start": strconv.Itoa(start),
			"X-Plex-Container-Size":  strconv.Itoa(pageSize),
		})
		if err != nil {
			return nil, err
		}
		items, err := decodeTracks(mc.MediaContainer.Metadata)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < pageSize {
			return all, nil
		}
	}
}

// hubTypeForItemType maps itemType to the Hub.Type name Plex's /hubs/search
// groups results under.
var hubTypeForItemType = map[int]string{1: "movie", 2: "show", 10: "track"}

// SearchLibraryItemsByQuery does a free-text title search scoped to one
// library section, for the Collections manual-item picker (DESIGN.md
// §11.11). Uses /hubs/search (confirmed against a live server to support
// sectionId scoping) rather than a title= library filter, since that filter
// needs a type-specific field prefix - track.title= works for tracks
// (search.go's searchByTitleOnly), but a bare title= silently matches
// everything rather than filtering, and the correct prefix for movie/show
// sections is unverified.
func (c *Client) SearchLibraryItemsByQuery(libraryID string, itemType int, query string, limit int) ([]Track, error) {
	if limit <= 0 {
		limit = 25
	}
	mc, err := c.get("/hubs/search?query=" + url.QueryEscape(query) + "&sectionId=" + url.QueryEscape(libraryID) + "&limit=" + strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	wantType := hubTypeForItemType[itemType]
	for i := range mc.MediaContainer.Hub {
		h := &mc.MediaContainer.Hub[i]
		if h.Type == wantType {
			return decodeTracks(h.Metadata)
		}
	}
	return nil, nil
}

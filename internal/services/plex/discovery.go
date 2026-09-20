// This file ports plex.ts's discovery/mix-seeding queries (roughly lines
// 1428-2128): recently-played and stale-played track lookups, artist
// search/details/albums/popular-tracks, sonic and metadata-based similarity,
// related hubs, recently-added albums, and the advanced multi-field track
// filter. internal/services/mixes (not yet ported) is the consumer - these
// are all read-only GET queries against a user's own Plex server.
package plex

import (
	"math/rand"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GetRecentTracks returns tracks played within the last `days` days, most
// recently played first. Plex's own sort only orders server-side; the
// day-cutoff filter happens client-side same as plex.ts's getRecentTracks.
func (c *Client) GetRecentTracks(libraryID string, days, limit int) ([]Track, error) {
	if days <= 0 {
		days = 7
	}
	if limit <= 0 {
		limit = 200
	}
	path := "/library/sections/" + url.PathEscape(libraryID) + "/all?type=10&sort=lastViewedAt:desc&lastViewedAt>>=0"
	mc, err := c.getWithHeaders(path, map[string]string{"X-Plex-Container-Size": strconv.Itoa(limit)})
	if err != nil {
		return nil, err
	}
	tracks, err := decodeTracks(mc.MediaContainer.Metadata)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	out := make([]Track, 0, len(tracks))
	for _, t := range tracks {
		if t.LastViewedAt*1000 >= cutoff {
			out = append(out, t)
		}
	}
	return out, nil
}

// SearchArtist finds a single artist by exact-ish title match, returning nil
// (not an error) if the library has none by that name.
func (c *Client) SearchArtist(libraryID, name string) (*Track, error) {
	path := "/library/sections/" + url.PathEscape(libraryID) + "/all?type=8&title=" + url.QueryEscape(name)
	mc, err := c.getWithHeaders(path, map[string]string{"X-Plex-Container-Size": "1"})
	if err != nil {
		return nil, err
	}
	artists, err := decodeTracks(mc.MediaContainer.Metadata)
	if err != nil || len(artists) == 0 {
		return nil, err
	}
	return &artists[0], nil
}

// GetArtistPopularTracks prefers Plex's external-popularity hubs (Last.fm-
// backed "Popular"/"Top Tracks"), falling back to the artist's own catalog
// sorted by local play count when no such hub exists.
func (c *Client) GetArtistPopularTracks(libraryID, artistKey string, limit int) ([]Track, error) {
	if limit <= 0 {
		limit = 10
	}
	path := "/hubs/sections/" + url.PathEscape(libraryID) + "?metadataItemId=" + url.QueryEscape(artistKey) + "&count=" + strconv.Itoa(limit)
	if mc, err := c.get(path); err == nil {
		for _, h := range mc.MediaContainer.Hub {
			title := strings.ToLower(h.Title)
			if title == "popular" || title == "top tracks" {
				tracks, err := decodeTracks(h.Metadata)
				if err != nil {
					return nil, err
				}
				if len(tracks) > 0 {
					if len(tracks) > limit {
						tracks = tracks[:limit]
					}
					return tracks, nil
				}
			}
		}
	}

	mc, err := c.getWithHeaders(
		"/library/metadata/"+url.PathEscape(artistKey)+"/allLeaves?sort=viewCount:desc",
		map[string]string{"X-Plex-Container-Size": strconv.Itoa(limit)},
	)
	if err != nil {
		return nil, err
	}
	tracks, err := decodeTracks(mc.MediaContainer.Metadata)
	if err != nil {
		return nil, err
	}
	if len(tracks) > limit {
		tracks = tracks[:limit]
	}
	return tracks, nil
}

// GetArtistDetails fetches an artist's full metadata (including its
// external-popularity ratingCount), returning nil, nil if it no longer
// exists.
func (c *Client) GetArtistDetails(artistKey string) (*Track, error) {
	mc, err := c.get("/library/metadata/" + url.PathEscape(artistKey))
	if err != nil {
		return nil, err
	}
	if len(mc.MediaContainer.Metadata) == 0 {
		return nil, nil
	}
	tracks, err := decodeTracks(mc.MediaContainer.Metadata[:1])
	if err != nil || len(tracks) == 0 {
		return nil, err
	}
	return &tracks[0], nil
}

// GetArtistAlbums returns every album belonging to an artist.
func (c *Client) GetArtistAlbums(artistKey string) ([]Track, error) {
	mc, err := c.get("/library/metadata/" + url.PathEscape(artistKey) + "/children")
	if err != nil {
		return nil, err
	}
	return decodeTracks(mc.MediaContainer.Metadata)
}

// GetAlbumTracks returns every track on an album.
func (c *Client) GetAlbumTracks(albumRatingKey string) ([]Track, error) {
	mc, err := c.get("/library/metadata/" + url.PathEscape(albumRatingKey) + "/children")
	if err != nil {
		return nil, err
	}
	return decodeTracks(mc.MediaContainer.Metadata)
}

// GetSimilarTracks tries Plex's sonic-analysis "nearest" endpoint first,
// falling back to metadata-based "similar" if sonic analysis isn't
// available for this track. Matches plex.ts: both tiers failing yields an
// empty slice, not an error - similarity is a nice-to-have for mix seeding,
// never worth aborting a whole mix generation over.
func (c *Client) GetSimilarTracks(trackKey string, limit int) ([]Track, error) {
	if limit <= 0 {
		limit = 10
	}
	path := "/library/metadata/" + url.PathEscape(trackKey) + "/nearest"
	if mc, err := c.getWithHeaders(path, map[string]string{"X-Plex-Container-Size": strconv.Itoa(limit)}); err == nil {
		return decodeTracks(mc.MediaContainer.Metadata)
	}
	mc, err := c.get("/library/metadata/" + url.PathEscape(trackKey) + "/similar?count=" + strconv.Itoa(limit))
	if err != nil {
		return nil, nil
	}
	return decodeTracks(mc.MediaContainer.Metadata)
}

// GetRelatedHubs returns an artist's or track's "Related" content hubs
// (e.g. "Fans Also Like", "Similar Artists"). A failure here is swallowed
// (returns an empty slice) same as plex.ts, except for an already-known
// unreachable server, which propagates so callers can distinguish "no
// related content" from "server down".
func (c *Client) GetRelatedHubs(ratingKey string) ([]Hub, error) {
	mc, err := c.get("/library/metadata/" + url.PathEscape(ratingKey) + "/related")
	if err != nil {
		if _, ok := err.(*UnreachableError); ok {
			return nil, err
		}
		return []Hub{}, nil
	}
	return mc.MediaContainer.Hub, nil
}

// GetStalePlayedTracks returns tracks not played in daysAgo days (oldest
// play first), optionally interleaved with never-played tracks so they
// aren't crowded out by a stale-play pool that's already bigger than limit
// on its own - see plex.ts's comment on why Time Capsule wants
// includeNeverPlayed=true while Daily Mix rediscoveries want it false.
func (c *Client) GetStalePlayedTracks(libraryID string, daysAgo, limit int, includeNeverPlayed bool) ([]Track, error) {
	path := "/library/sections/" + url.PathEscape(libraryID) + "/all?type=10&sort=lastViewedAt:asc&lastViewedAt>>=0"
	mc, err := c.getWithHeaders(path, map[string]string{"X-Plex-Container-Size": strconv.Itoa(limit * 2)})
	if err != nil {
		return nil, err
	}
	tracks, err := decodeTracks(mc.MediaContainer.Metadata)
	if err != nil {
		return nil, err
	}

	cutoff := time.Now().Add(-time.Duration(daysAgo) * 24 * time.Hour).UnixMilli()
	staleTracks := make([]Track, 0, len(tracks))
	for _, t := range tracks {
		if t.LastViewedAt*1000 < cutoff {
			staleTracks = append(staleTracks, t)
		}
	}

	if !includeNeverPlayed {
		if len(staleTracks) > limit {
			staleTracks = staleTracks[:limit]
		}
		return staleTracks, nil
	}

	neverPath := "/library/sections/" + url.PathEscape(libraryID) + "/all?type=10&viewCount=0"
	neverMC, err := c.getWithHeaders(neverPath, map[string]string{"X-Plex-Container-Size": strconv.Itoa(limit * 2)})
	if err != nil {
		return nil, err
	}
	neverPlayed, err := decodeTracks(neverMC.MediaContainer.Metadata)
	if err != nil {
		return nil, err
	}

	merged := make([]Track, 0, limit)
	seenKeys := map[string]bool{}
	poolLength := len(staleTracks)
	if len(neverPlayed) > poolLength {
		poolLength = len(neverPlayed)
	}
	for i := 0; i < poolLength && len(merged) < limit; i++ {
		for _, pool := range [][]Track{staleTracks, neverPlayed} {
			if i >= len(pool) || len(merged) >= limit {
				continue
			}
			t := pool[i]
			if !seenKeys[t.RatingKey] {
				merged = append(merged, t)
				seenKeys[t.RatingKey] = true
			}
		}
	}
	return merged, nil
}

// GetRecentlyAddedAlbums returns the most recently added albums in a
// library.
func (c *Client) GetRecentlyAddedAlbums(libraryID string, limit int) ([]Track, error) {
	if limit <= 0 {
		limit = 10
	}
	path := "/library/sections/" + url.PathEscape(libraryID) + "/recentlyAdded?type=9"
	mc, err := c.getWithHeaders(path, map[string]string{"X-Plex-Container-Size": strconv.Itoa(limit)})
	if err != nil {
		return nil, err
	}
	return decodeTracks(mc.MediaContainer.Metadata)
}

// AdvancedFilterOptions mirrors plex.ts's getTracksWithAdvancedFilters
// options object 1:1 - see that function's comments for what each field
// means. Fields left at their zero value are simply omitted from the
// query/client-side filtering, same as `undefined` in the TS version.
type AdvancedFilterOptions struct {
	PlayedInLastDays    int
	NotPlayedInLastDays int
	AddedInLastDays     int

	ReleasedAfterYear  int
	ReleasedBeforeYear int

	MinRating int
	MaxRating int

	MinPlayCount *int // pointer: 0 is a meaningful value here, unlike the others
	MaxPlayCount *int

	MinDuration int // seconds
	MaxDuration int // seconds

	MinTrackNumber int
	MaxTrackNumber int
	DiscNumber     int

	MinBitrate    int
	AudioCodec    []string
	MinSampleRate int
	LosslessOnly  bool

	Genres        []string
	ExcludeGenres []string
	Moods         []string
	ExcludeMoods  []string
	Styles        []string
	ExcludeStyles []string
	Collections   []string
	Labels        []string
	ArtistNames   []string
	AlbumTitles   []string

	// "" (unset), "random", "playCount", "lastPlayed", "dateAdded",
	// "releaseDate", "rating", "duration", "title".
	SortBy        string
	SortDirection string // "asc" or "desc", default "desc"

	Limit int
}

var advancedFilterSortFields = map[string]string{
	"playCount":   "viewCount",
	"lastPlayed":  "lastViewedAt",
	"dateAdded":   "addedAt",
	"releaseDate": "year",
	"rating":      "userRating",
	"duration":    "duration",
	"title":       "titleSort",
}

// GetTracksWithAdvancedFilters supports Plex's full filter-field surface
// plus complex boolean logic (push/or/pop for "any of several codecs") via
// raw query-string filters, then applies client-side filtering for the
// fields Plex can't query server-side (genre/mood/style/collection/label
// tag lists, artist/album substring match) - a direct port of plex.ts's
// same two-stage approach.
func (c *Client) GetTracksWithAdvancedFilters(libraryID string, opts AdvancedFilterOptions) ([]Track, error) {
	now := time.Now().Unix()
	var filters []string

	if opts.PlayedInLastDays > 0 {
		filters = append(filters, "lastViewedAt>="+strconv.FormatInt(now-int64(opts.PlayedInLastDays)*86400, 10))
	}
	if opts.NotPlayedInLastDays > 0 {
		filters = append(filters, "lastViewedAt<"+strconv.FormatInt(now-int64(opts.NotPlayedInLastDays)*86400, 10))
	}
	if opts.AddedInLastDays > 0 {
		filters = append(filters, "addedAt>="+strconv.FormatInt(now-int64(opts.AddedInLastDays)*86400, 10))
	}
	if opts.ReleasedAfterYear > 0 {
		// Single >=, not doubled >>= - confirmed live (librarysearch.go's
		// SearchLibraryItems had the identical bug) that the doubled form
		// silently returns zero/wrong results unless carefully escaped,
		// while single >=/<= works correctly raw, matching every other
		// filter in this function.
		filters = append(filters, "parentYear>="+strconv.Itoa(opts.ReleasedAfterYear))
	}
	if opts.ReleasedBeforeYear > 0 {
		filters = append(filters, "parentYear<="+strconv.Itoa(opts.ReleasedBeforeYear))
	}
	if opts.MinRating > 0 {
		filters = append(filters, "userRating>="+strconv.Itoa(opts.MinRating))
	}
	if opts.MaxRating > 0 {
		filters = append(filters, "userRating<="+strconv.Itoa(opts.MaxRating))
	}
	if opts.MinPlayCount != nil {
		filters = append(filters, "viewCount>="+strconv.Itoa(*opts.MinPlayCount))
	}
	if opts.MaxPlayCount != nil {
		filters = append(filters, "viewCount<="+strconv.Itoa(*opts.MaxPlayCount))
	}
	if opts.MinDuration > 0 {
		filters = append(filters, "duration>="+strconv.Itoa(opts.MinDuration*1000))
	}
	if opts.MaxDuration > 0 {
		filters = append(filters, "duration<="+strconv.Itoa(opts.MaxDuration*1000))
	}
	if opts.MinTrackNumber > 0 {
		filters = append(filters, "index>="+strconv.Itoa(opts.MinTrackNumber))
	}
	if opts.MaxTrackNumber > 0 {
		filters = append(filters, "index<="+strconv.Itoa(opts.MaxTrackNumber))
	}
	if opts.DiscNumber > 0 {
		filters = append(filters, "parentIndex="+strconv.Itoa(opts.DiscNumber))
	}
	if opts.MinBitrate > 0 {
		filters = append(filters, "bitrate>="+strconv.Itoa(opts.MinBitrate))
	}
	switch {
	case opts.LosslessOnly:
		filters = append(filters,
			"push=1", "audioCodec=flac",
			"or=1", "audioCodec=alac",
			"or=1", "audioCodec=ape",
			"or=1", "audioCodec=wav",
			"pop=1")
	case len(opts.AudioCodec) == 1:
		filters = append(filters, "audioCodec="+opts.AudioCodec[0])
	case len(opts.AudioCodec) > 1:
		filters = append(filters, "push=1", "audioCodec="+opts.AudioCodec[0])
		for _, codec := range opts.AudioCodec[1:] {
			filters = append(filters, "or=1", "audioCodec="+codec)
		}
		filters = append(filters, "pop=1")
	}
	if opts.MinSampleRate > 0 {
		filters = append(filters, "sampleRate>="+strconv.Itoa(opts.MinSampleRate))
	}

	path := "/library/sections/" + url.PathEscape(libraryID) + "/all?type=10"
	if len(filters) > 0 {
		path += "&" + strings.Join(filters, "&")
	}
	if opts.SortBy != "" && opts.SortBy != "random" {
		sortField := opts.SortBy
		if mapped, ok := advancedFilterSortFields[opts.SortBy]; ok {
			sortField = mapped
		}
		direction := opts.SortDirection
		if direction == "" {
			direction = "desc"
		}
		path += "&sort=" + sortField + ":" + direction
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
	tracks, err := decodeTracks(mc.MediaContainer.Metadata)
	if err != nil {
		return nil, err
	}

	tracks = filterByTags(tracks, opts.Genres, opts.ExcludeGenres, func(t Track) []Tag { return t.Genre })
	tracks = filterByTags(tracks, opts.Moods, opts.ExcludeMoods, func(t Track) []Tag { return t.Mood })
	tracks = filterByTags(tracks, opts.Styles, opts.ExcludeStyles, func(t Track) []Tag { return t.Style })
	tracks = filterByTags(tracks, opts.Collections, nil, func(t Track) []Tag { return t.Collection })

	if len(opts.Labels) > 0 {
		labels := lowerAll(opts.Labels)
		tracks = filterSlice(tracks, func(t Track) bool {
			label := strings.ToLower(t.ParentStudio)
			return containsAnySubstring(labels, label)
		})
	}
	if len(opts.ArtistNames) > 0 {
		names := lowerAll(opts.ArtistNames)
		tracks = filterSlice(tracks, func(t Track) bool {
			return containsAnySubstring(names, strings.ToLower(t.GrandparentTitle))
		})
	}
	if len(opts.AlbumTitles) > 0 {
		titles := lowerAll(opts.AlbumTitles)
		tracks = filterSlice(tracks, func(t Track) bool {
			return containsAnySubstring(titles, strings.ToLower(t.ParentTitle))
		})
	}

	if opts.SortBy == "random" {
		rand.Shuffle(len(tracks), func(i, j int) { tracks[i], tracks[j] = tracks[j], tracks[i] })
	}

	return tracks, nil
}

func lowerAll(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = strings.ToLower(v)
	}
	return out
}

// containsAnySubstring reports whether needle is a substring of haystack
// for any needle in needles - matches plex.ts's `artistLower.some(artist =>
// artistName.includes(artist))` pattern used for label/artist/album
// filters.
func containsAnySubstring(needles []string, haystack string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

func filterSlice(tracks []Track, keep func(Track) bool) []Track {
	out := tracks[:0]
	for _, t := range tracks {
		if keep(t) {
			out = append(out, t)
		}
	}
	return out
}

// filterByTags applies plex.ts's include/exclude genre-style tag filtering:
// keep a track if it has any of the included tags (skipped when includes is
// empty), then drop it if it has any of the excluded tags.
func filterByTags(tracks []Track, includes, excludes []string, tagsOf func(Track) []Tag) []Track {
	if len(includes) > 0 {
		want := lowerAll(includes)
		tracks = filterSlice(tracks, func(t Track) bool {
			for _, tag := range tagsOf(t) {
				for _, w := range want {
					if strings.ToLower(tag.Tag) == w {
						return true
					}
				}
			}
			return false
		})
	}
	if len(excludes) > 0 {
		unwant := lowerAll(excludes)
		tracks = filterSlice(tracks, func(t Track) bool {
			for _, tag := range tagsOf(t) {
				for _, u := range unwant {
					if strings.ToLower(tag.Tag) == u {
						return false
					}
				}
			}
			return true
		})
	}
	return tracks
}

// SonicRange is an inclusive [Min, Max] band for one of the sonic-analysis
// dimensions (tempo/energy/danceability) in GetSonicallySimilarTracks.
type SonicRange struct {
	Min, Max float64
}

// SonicSimilarOptions mirrors plex.ts's getSonicallySimilarTracks options.
type SonicSimilarOptions struct {
	// MaxDistance is accepted for API-shape parity with plex.ts but unused
	// there too (Plex's /nearest endpoint doesn't take a distance param) -
	// kept as a documented no-op rather than silently dropped.
	MaxDistance float64
	Limit       int

	TempoRange        *SonicRange
	EnergyRange       *SonicRange
	DanceabilityRange *SonicRange
}

// GetSonicallySimilarTracks finds tracks near a seed track using Plex's
// sonic (audio-fingerprint) analysis, optionally narrowed to tempo/energy/
// danceability bands. Falls back to GetSimilarTracks (metadata-based) if
// the seed track has no sonic analysis available yet.
func (c *Client) GetSonicallySimilarTracks(seedTrackKey, _libraryID string, opts SonicSimilarOptions) ([]Track, error) {
	seedTrack, err := c.GetTrackDetails(seedTrackKey)
	if err != nil {
		return nil, err
	}
	if seedTrack == nil {
		return nil, &notFoundError{"Seed track not found"}
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	mc, err := c.get("/library/metadata/" + url.PathEscape(seedTrackKey) + "/nearest?limit=" + strconv.Itoa(limit))
	if err != nil {
		return c.GetSimilarTracks(seedTrackKey, limit)
	}
	tracks, err := decodeTracks(mc.MediaContainer.Metadata)
	if err != nil {
		return nil, err
	}

	if opts.TempoRange == nil && opts.EnergyRange == nil && opts.DanceabilityRange == nil {
		return tracks, nil
	}
	return filterSlice(tracks, func(t Track) bool {
		a := t.MusicAnalysis
		if a == nil {
			return true // include if no analysis data, matching plex.ts
		}
		if r := opts.TempoRange; r != nil && (a.Tempo < r.Min || a.Tempo > r.Max) {
			return false
		}
		if r := opts.EnergyRange; r != nil && (a.Energy < r.Min || a.Energy > r.Max) {
			return false
		}
		if r := opts.DanceabilityRange; r != nil && (a.Danceability < r.Min || a.Danceability > r.Max) {
			return false
		}
		return true
	}), nil
}

type notFoundError struct{ Message string }

func (e *notFoundError) Error() string { return e.Message }

// libraryDirectoryTitles fetches a library's genre/mood/style/collection
// "Directory" list (Plex's per-library metadata browse endpoints) and
// returns their titles sorted alphabetically. Matches plex.ts's four
// getLibrary*() methods: any request failure yields an empty slice rather
// than an error, since these feed optional filter dropdowns.
func (c *Client) libraryDirectoryTitles(libraryID, kind string) []string {
	mc, err := c.get("/library/sections/" + url.PathEscape(libraryID) + "/" + kind)
	if err != nil {
		return []string{}
	}
	out := make([]string, 0, len(mc.MediaContainer.Directory))
	for _, d := range mc.MediaContainer.Directory {
		out = append(out, d.Title)
	}
	sort.Strings(out)
	return out
}

// GetLibraryGenres ports plex.ts's getLibraryGenres.
func (c *Client) GetLibraryGenres(libraryID string) []string {
	return c.libraryDirectoryTitles(libraryID, "genre")
}

// GetLibraryMoods ports plex.ts's getLibraryMoods.
func (c *Client) GetLibraryMoods(libraryID string) []string {
	return c.libraryDirectoryTitles(libraryID, "mood")
}

// GetLibraryStyles ports plex.ts's getLibraryStyles.
func (c *Client) GetLibraryStyles(libraryID string) []string {
	return c.libraryDirectoryTitles(libraryID, "style")
}

// GetLibraryCollections ports plex.ts's getLibraryCollections.
func (c *Client) GetLibraryCollections(libraryID string) []string {
	return c.libraryDirectoryTitles(libraryID, "collection")
}

// GetLibraryStudios, GetLibraryContentRatings, GetLibraryYears, and
// GetLibraryActors round out libraryDirectoryTitles' facet coverage for
// Dynamic Collections (DESIGN.md §11.11's dynamic-collection-set builder,
// scheduler.GenerateDynamicCollections) - same Plex "Directory" browse
// endpoint as genre/mood/style/collection above, just a different facet
// kind. "decade" has no such endpoint on its own; callers derive it from
// GetLibraryYears instead.
func (c *Client) GetLibraryStudios(libraryID string) []string {
	return c.libraryDirectoryTitles(libraryID, "studio")
}

func (c *Client) GetLibraryContentRatings(libraryID string) []string {
	return c.libraryDirectoryTitles(libraryID, "contentRating")
}

func (c *Client) GetLibraryYears(libraryID string) []string {
	return c.libraryDirectoryTitles(libraryID, "year")
}

func (c *Client) GetLibraryActors(libraryID string) []string {
	return c.libraryDirectoryTitles(libraryID, "actor")
}

// GetLibraryDirectors and GetLibraryWriters round out the facet set with
// the two credit types Kometa's own community configs lean on most for
// person-based collections (fscorrupt/directors.yml's Director/Writer
// templates) - not independently live-verified against a real Plex server
// the way genre/actor/studio were (same caveat as GetLibraryContentRatings/
// GetLibraryYears): Plex's per-library "Directory" browse endpoint is
// confirmed to exist for genre/mood/style/collection/actor, and director/
// writer are documented as the same kind of facet, but a field-name
// mismatch here is the first place to look if either comes back empty.
func (c *Client) GetLibraryDirectors(libraryID string) []string {
	return c.libraryDirectoryTitles(libraryID, "director")
}

func (c *Client) GetLibraryWriters(libraryID string) []string {
	return c.libraryDirectoryTitles(libraryID, "writer")
}

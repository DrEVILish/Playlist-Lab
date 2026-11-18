package plex

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SearchTrack runs the same multi-strategy Plex track search plex.ts's
// searchTrack() does. With a library, artist, and title all given it tries
// an artist-first lookup (cheap once the artist's catalog is cached, and
// tolerant of title punctuation/spelling differences since the title filter
// happens here rather than as a server-side substring match), then falls
// back through increasingly loose Plex-side filters, and finally a
// title-only fetch ranked by artist match. With only a query, it falls back
// to Plex's hub search across all fields.
func (c *Client) SearchTrack(query, libraryID, artist, title string) ([]Track, error) {
	return c.searchTrack(query, libraryID, artist, title, false)
}

// SearchTrackComplete rejects incomplete searches before a playlist can be replaced.
func (c *Client) SearchTrackComplete(query, libraryID, artist, title string) ([]Track, error) {
	return c.searchTrack(query, libraryID, artist, title, true)
}

func (c *Client) searchTrack(query, libraryID, artist, title string, strict bool) ([]Track, error) {
	cacheKey := strconv.FormatBool(strict) + "|" + query + "|" + orAll(libraryID) + "|" + artist + "|" + title
	if cached, ok := c.getCache(c.searchCache, cacheKey); ok {
		return cached, nil
	}

	var tracks []Track
	var err error

	switch {
	case libraryID != "" && artist != "" && title != "":
		tracks, err = c.searchByArtistFirst(libraryID, artist, title, strict)
	case libraryID != "" && title != "":
		tracks, err = c.searchByTitleOnly(libraryID, title)
	default:
		tracks, err = c.searchHub(query, libraryID, strict)
	}
	if err != nil {
		return nil, err
	}

	if len(tracks) > 0 {
		c.setCache(c.searchCache, cacheKey, tracks)
	}
	return tracks, nil
}

func orAll(s string) string {
	if s == "" {
		return "all"
	}
	return s
}

func normalizeLoose(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (c *Client) searchByArtistFirst(libraryID, artist, title string, strict bool) ([]Track, error) {
	artistLookupKey := libraryID + "|" + strings.ToLower(artist)
	artists, ok := c.getCache(c.artistLookupCache, artistLookupKey)
	if !ok {
		mc, err := c.get("/library/sections/" + libraryID + "/all?type=8&artist.title=" + url.QueryEscape(artist))
		if err != nil {
			if strict {
				return nil, err
			}
			return c.artistFirstFallback(libraryID, artist, title, strict)
		}
		artists, err = decodeTracks(mc.MediaContainer.Metadata)
		if err != nil {
			return nil, err
		}
		c.setCache(c.artistLookupCache, artistLookupKey, artists)
	}

	var tracks []Track
	if len(artists) > 0 {
		artistKey := artists[0].RatingKey
		artistTracksKey := libraryID + "|" + artistKey
		artistTracks, ok := c.getCache(c.artistTracksCache, artistTracksKey)
		if !ok {
			mc, err := c.getWithHeaders(
				"/library/metadata/"+artistKey+"/allLeaves?type=10",
				map[string]string{"X-Plex-Container-Start": "0", "X-Plex-Container-Size": strconv.Itoa(maxArtistCatalog)},
			)
			if err != nil {
				if strict {
					return nil, err
				}
				return c.artistFirstFallback(libraryID, artist, title, strict)
			}
			artistTracks, err = decodeTracks(mc.MediaContainer.Metadata)
			if err != nil {
				return nil, err
			}
			c.setCache(c.artistTracksCache, artistTracksKey, artistTracks)
		}

		normalizedSearchTitle := normalizeLoose(title)
		for _, t := range artistTracks {
			nt := normalizeLoose(t.Title)
			if strings.Contains(nt, normalizedSearchTitle) || strings.Contains(normalizedSearchTitle, nt) {
				tracks = append(tracks, t)
			}
		}
	}

	if len(tracks) == 0 {
		return c.artistFirstFallback(libraryID, artist, title, strict)
	}
	return tracks, nil
}

// artistFirstFallback runs the three progressively looser tiers plex.ts's
// searchTrack falls through to once the artist-first strategy (or the
// artist/catalog lookups it depends on) comes up empty.
func (c *Client) artistFirstFallback(libraryID, artist, title string, strict bool) ([]Track, error) {
	path := "/library/sections/" + libraryID + "/all?type=10"
	trackTitle := url.QueryEscape(title)
	trackArtist := url.QueryEscape(artist)
	mc, err := c.get(path + "&artist.title=" + trackArtist + "&track.title=" + trackTitle)
	if err == nil {
		tracks, decodeErr := decodeTracks(mc.MediaContainer.Metadata)
		if decodeErr != nil && strict {
			return nil, decodeErr
		}
		if len(tracks) > 0 {
			return tracks, nil
		}
	} else if strict {
		return nil, err
	}

	// Older Plex versions can reject the optional original-title filter.
	if mc, err := c.get(path + "&track.title=" + trackTitle + "&track.originalTitle=" + trackArtist); err == nil {
		tracks, decodeErr := decodeTracks(mc.MediaContainer.Metadata)
		if decodeErr != nil && strict {
			return nil, decodeErr
		}
		if len(tracks) > 0 {
			return tracks, nil
		}
	} else if strict && err != readStatusError(http.StatusBadRequest) && err.Error() != "not found" {
		return nil, err
	}

	mc, err = c.get("/library/sections/" + libraryID + "/all?type=10&track.title=" + url.QueryEscape(title))
	if err != nil {
		return nil, err
	}
	allTracks, err := decodeTracks(mc.MediaContainer.Metadata)
	if err != nil {
		return nil, err
	}

	// Don't hard-filter by artist - compilation/soundtrack tagging is
	// inconsistent enough that a strict filter would discard the correct
	// track before the matching engine's compilation-aware scoring ever
	// sees it. Rank artist-matching candidates first, keep the rest, cap
	// defensively in case a very generic title returns a huge result set.
	normalizedSearchArtist := normalizeLoose(artist)
	artistMatches := func(t Track) bool {
		trackArtist := normalizeLoose(t.OriginalTitle)
		albumArtist := normalizeLoose(t.GrandparentTitle)
		return (trackArtist != "" && (strings.Contains(trackArtist, normalizedSearchArtist) || strings.Contains(normalizedSearchArtist, trackArtist))) ||
			(albumArtist != "" && (strings.Contains(albumArtist, normalizedSearchArtist) || strings.Contains(normalizedSearchArtist, albumArtist)))
	}
	sort.SliceStable(allTracks, func(i, j int) bool {
		return boolToInt(artistMatches(allTracks[i])) > boolToInt(artistMatches(allTracks[j]))
	})
	if len(allTracks) > 50 {
		allTracks = allTracks[:50]
	}
	return allTracks, nil
}

func (c *Client) searchByTitleOnly(libraryID, title string) ([]Track, error) {
	mc, err := c.get("/library/sections/" + libraryID + "/all?type=10&track.title=" + url.QueryEscape(title))
	if err != nil {
		return nil, err
	}
	return decodeTracks(mc.MediaContainer.Metadata)
}

func (c *Client) searchHub(query, libraryID string, strict bool) ([]Track, error) {
	mc, err := c.get("/hubs/search?query=" + url.QueryEscape(query) + "&limit=100")
	if err != nil {
		return nil, err
	}
	var trackHub, albumHub *Hub
	for i := range mc.MediaContainer.Hub {
		h := &mc.MediaContainer.Hub[i]
		if h.Type == "track" && trackHub == nil {
			trackHub = h
		}
		if h.Type == "album" && albumHub == nil {
			albumHub = h
		}
	}

	var allTracks []Track
	if trackHub != nil {
		allTracks, err = decodeTracks(trackHub.Metadata)
		if err != nil {
			return nil, err
		}
	}

	if albumHub != nil {
		albums, err := decodeTracks(albumHub.Metadata)
		if err != nil && strict {
			return nil, err
		}
		if err == nil {
			limit := min(5, len(albums))
			for _, album := range albums[:limit] {
				amc, err := c.get(album.Key)
				if err != nil {
					if strict {
						return nil, err
					}
					continue
				}
				albumTracks, err := decodeTracks(amc.MediaContainer.Metadata)
				if err != nil {
					if strict {
						return nil, err
					}
					continue
				}
				allTracks = append(allTracks, albumTracks...)
			}
		}
	}

	if libraryID == "" || len(allTracks) == 0 {
		return allTracks, nil
	}
	libraryIDNum, err := strconv.Atoi(libraryID)
	if err != nil {
		return allTracks, nil
	}
	filtered := allTracks[:0]
	for _, t := range allTracks {
		if t.LibrarySectionID == libraryIDNum {
			filtered = append(filtered, t)
		}
	}
	return filtered, nil
}

// DecodeTracks exposes decodeTracks for callers outside this package (e.g.
// internal/services/mixes) that need to decode a Hub's raw Metadata list.
func DecodeTracks(raw []json.RawMessage) ([]Track, error) { return decodeTracks(raw) }

func decodeTracks(raw []json.RawMessage) ([]Track, error) {
	out := make([]Track, 0, len(raw))
	for _, r := range raw {
		var t Track
		if err := json.Unmarshal(r, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (c *Client) getCache(cache map[string]cacheEntry[[]Track], key string) ([]Track, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	entry, ok := cache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.value, true
}

func (c *Client) setCache(cache map[string]cacheEntry[[]Track], key string, value []Track) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if len(cache) >= maxCacheEntries {
		for k := range cache {
			delete(cache, k)
			break
		}
	}
	cache[key] = cacheEntry[[]Track]{value: value, expiresAt: time.Now().Add(searchCacheTTL)}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

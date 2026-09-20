// Package lastfm ports services/lastfm.ts: read-only access to Last.fm's
// public chart/tag endpoints, used by the mix-generation "popular tracks
// optimization" path (services/mixes.ts) to find popular artists without
// querying the whole Plex library. No API key registration needed - Last.fm
// publishes a public read-only key for exactly this kind of chart data,
// ported verbatim rather than replaced (it's not a secret to rotate).
package lastfm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const (
	apiBase = "https://ws.audioscrobbler.com/2.0/"
	apiKey  = "b25b959554ed76058ac220b7b2e0a026"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

type Artist struct {
	Name      string
	PlayCount string
	Listeners string
	MBID      string
	URL       string
}

func get(ctx context.Context, params url.Values, out interface{}) error {
	params.Set("api_key", apiKey)
	params.Set("format", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("lastfm api error: status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type topArtistsResponse struct {
	Artists struct {
		Artist []rawArtist `json:"artist"`
	} `json:"artists"`
}

type tagTopArtistsResponse struct {
	TopArtists struct {
		Artist []rawArtist `json:"artist"`
	} `json:"topartists"`
}

type rawArtist struct {
	Name      string `json:"name"`
	PlayCount string `json:"playcount"`
	Listeners string `json:"listeners"`
	MBID      string `json:"mbid"`
	URL       string `json:"url"`
}

func (a rawArtist) toArtist() Artist {
	return Artist(a)
}

// GetTopArtists ports getTopArtists (lastfm.ts:33-63) using chart.gettopartists.
// On any request/decode error it returns (nil, nil), matching the TS
// behavior of logging and returning an empty array rather than propagating.
func GetTopArtists(ctx context.Context, limit int) []Artist {
	if limit <= 0 {
		limit = 50
	}
	params := url.Values{"method": {"chart.gettopartists"}, "limit": {fmt.Sprint(limit)}}
	var resp topArtistsResponse
	if err := get(ctx, params, &resp); err != nil {
		return nil
	}
	out := make([]Artist, 0, len(resp.Artists.Artist))
	for _, a := range resp.Artists.Artist {
		out = append(out, a.toArtist())
	}
	return out
}

// Track mirrors ExternalTrack's shape for the Last.fm chart endpoints used
// by scrapeLastfmPlaylist (scrapers.ts:955).
type Track struct {
	Title  string
	Artist string
}

type rawTrack struct {
	Name   string `json:"name"`
	Artist struct {
		Name string `json:"name"`
	} `json:"artist"`
}

func (t rawTrack) toTrack() Track {
	title, artist := t.Name, t.Artist.Name
	if title == "" {
		title = "Unknown Track"
	}
	if artist == "" {
		artist = "Unknown Artist"
	}
	return Track{Title: title, Artist: artist}
}

// GetTopTracks ports the 'top-tracks' branch of scrapeLastfmPlaylist using
// chart.gettoptracks.
func GetTopTracks(ctx context.Context, limit int) []Track {
	if limit <= 0 {
		limit = 100
	}
	var resp struct {
		Tracks struct {
			Track []rawTrack `json:"track"`
		} `json:"tracks"`
	}
	params := url.Values{"method": {"chart.gettoptracks"}, "limit": {fmt.Sprint(limit)}}
	if err := get(ctx, params, &resp); err != nil {
		return nil
	}
	out := make([]Track, 0, len(resp.Tracks.Track))
	for _, t := range resp.Tracks.Track {
		out = append(out, t.toTrack())
	}
	return out
}

// GetArtistTopTracks ports the artist.gettoptracks call inside the
// 'top-artists' branch of scrapeLastfmPlaylist.
func GetArtistTopTracks(ctx context.Context, artist string, limit int) []Track {
	if limit <= 0 {
		limit = 5
	}
	var resp struct {
		TopTracks struct {
			Track []rawTrack `json:"track"`
		} `json:"toptracks"`
	}
	params := url.Values{"method": {"artist.gettoptracks"}, "artist": {artist}, "limit": {fmt.Sprint(limit)}}
	if err := get(ctx, params, &resp); err != nil {
		return nil
	}
	out := make([]Track, 0, len(resp.TopTracks.Track))
	for _, t := range resp.TopTracks.Track {
		out = append(out, t.toTrack())
	}
	return out
}

// GetTopTags ports the chart.gettoptags call inside the 'top-tags' branch of
// scrapeLastfmPlaylist, returning just the tag names.
func GetTopTags(ctx context.Context, limit int) []string {
	if limit <= 0 {
		limit = 10
	}
	var resp struct {
		Tags struct {
			Tag []struct {
				Name string `json:"name"`
			} `json:"tag"`
		} `json:"tags"`
	}
	params := url.Values{"method": {"chart.gettoptags"}, "limit": {fmt.Sprint(limit)}}
	if err := get(ctx, params, &resp); err != nil {
		return nil
	}
	out := make([]string, 0, len(resp.Tags.Tag))
	for _, t := range resp.Tags.Tag {
		out = append(out, t.Name)
	}
	return out
}

// GetTagTopTracks ports the tag.gettoptracks call used by both the
// 'top-tags' and 'tag' branches of scrapeLastfmPlaylist.
func GetTagTopTracks(ctx context.Context, tag string, limit int) []Track {
	if limit <= 0 {
		limit = 10
	}
	var resp struct {
		Tracks struct {
			Track []rawTrack `json:"track"`
		} `json:"tracks"`
	}
	params := url.Values{"method": {"tag.gettoptracks"}, "tag": {tag}, "limit": {fmt.Sprint(limit)}}
	if err := get(ctx, params, &resp); err != nil {
		return nil
	}
	out := make([]Track, 0, len(resp.Tracks.Track))
	for _, t := range resp.Tracks.Track {
		out = append(out, t.toTrack())
	}
	return out
}

// GetTopArtistsByTag ports getTopArtistsByTag (lastfm.ts:98-...) using
// tag.gettopartists, for genre-specific mixes.
func GetTopArtistsByTag(ctx context.Context, tag string, limit int) []Artist {
	if limit <= 0 {
		limit = 50
	}
	params := url.Values{"method": {"tag.gettopartists"}, "tag": {tag}, "limit": {fmt.Sprint(limit)}}
	var resp tagTopArtistsResponse
	if err := get(ctx, params, &resp); err != nil {
		return nil
	}
	out := make([]Artist, 0, len(resp.TopArtists.Artist))
	for _, a := range resp.TopArtists.Artist {
		out = append(out, a.toArtist())
	}
	return out
}

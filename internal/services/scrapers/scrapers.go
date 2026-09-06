// Package scrapers ports the plain-HTTP-only half of scrapers.ts (Deezer's
// and Apple Music RSS's public, unauthenticated JSON APIs) - the pieces
// that need no headless browser at all. The browser-driven pieces
// (Puppeteer in Node, chromedp in Go) live per-adapter instead, e.g.
// internal/adapters/apple/source.go - see that package's doc for why
// Apple Music's own playlist page needs a real DOM.
package scrapers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

func getJSON(rawURL string, out any) error {
	resp, err := httpClient.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d from %s", resp.StatusCode, rawURL)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Playlist mirrors ExternalPlaylist's fields as used by chart scraping
// (getDeezerCharts) - title/artist/album only, no per-track IDs.
type Playlist struct {
	ID          string
	Name        string
	Description string
	Source      string
	Tracks      []Track
}

type Track struct {
	Title  string
	Artist string
	Album  string
}

// PopularPlaylist mirrors the Array<{name,url,description,count}> shape
// returned by getDeezerPopularPlaylists/searchAppleMusicPlaylists/etc.
type PopularPlaylist struct {
	Name        string
	URL         string
	Description string
	Count       int
}

var deezerCountryNames = map[string]string{
	"global": "Global", "us": "United States", "gb": "United Kingdom",
	"au": "Australia", "ca": "Canada", "de": "Germany", "fr": "France",
	"es": "Spain", "br": "Brazil", "jp": "Japan",
}

// DeezerCharts ports getDeezerCharts: Deezer's public chart/search API,
// no auth required.
func DeezerCharts(country string) []Playlist {
	var out []Playlist

	var top struct {
		Data []struct {
			Title  string `json:"title"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist"`
			Album struct {
				Title string `json:"title"`
			} `json:"album"`
		} `json:"data"`
	}
	if err := getJSON("https://api.deezer.com/chart/0/tracks?limit=50", &top); err != nil {
		slog.Error("[Deezer] charts error", "error", err)
		return out
	}
	if len(top.Data) > 0 {
		tracks := make([]Track, 0, len(top.Data))
		for _, t := range top.Data {
			artist := t.Artist.Name
			if artist == "" {
				artist = "Unknown"
			}
			tracks = append(tracks, Track{Title: t.Title, Artist: artist, Album: t.Album.Title})
		}
		out = append(out, Playlist{
			ID: "deezer-top-global", Name: "Top 50 Global",
			Description: "Most played tracks worldwide", Source: "deezer", Tracks: tracks,
		})
	}

	if country == "global" {
		return out
	}
	countryName := deezerCountryNames[country]
	if countryName == "" {
		countryName = country
	}

	var search struct {
		Data []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"data"`
	}
	q := url.QueryEscape("Top 50 " + countryName)
	if err := getJSON("https://api.deezer.com/search/playlist?q="+q+"&limit=5", &search); err != nil {
		slog.Error("[Deezer] charts search error", "error", err)
		return out
	}
	var chartPlaylistID int64
	for _, p := range search.Data {
		if isChartTitleMatch(p.Title, countryName) {
			chartPlaylistID = p.ID
			break
		}
	}
	if chartPlaylistID == 0 {
		return out
	}

	var pl struct {
		Data []struct {
			Title  string `json:"title"`
			Artist struct {
				Name string `json:"name"`
			} `json:"artist"`
			Album struct {
				Title string `json:"title"`
			} `json:"album"`
		} `json:"data"`
	}
	if err := getJSON(fmt.Sprintf("https://api.deezer.com/playlist/%d/tracks?limit=50", chartPlaylistID), &pl); err != nil {
		slog.Error("[Deezer] charts playlist error", "error", err)
		return out
	}
	if len(pl.Data) > 0 {
		tracks := make([]Track, 0, len(pl.Data))
		for _, t := range pl.Data {
			artist := t.Artist.Name
			if artist == "" {
				artist = "Unknown"
			}
			tracks = append(tracks, Track{Title: t.Title, Artist: artist, Album: t.Album.Title})
		}
		out = append(out, Playlist{
			ID: "deezer-top-" + country, Name: "Top 50 " + countryName,
			Description: "Top tracks in " + countryName, Source: "deezer", Tracks: tracks,
		})
	}
	return out
}

// isChartTitleMatch ports the .find() predicate inside getDeezerCharts:
// a search result only counts as "the" country chart playlist if its title
// mentions both "top" and the country name, case-insensitively.
func isChartTitleMatch(title, countryName string) bool {
	lt := strings.ToLower(title)
	return strings.Contains(lt, "top") && strings.Contains(lt, strings.ToLower(countryName))
}

// AriaCharts ports scrapeAriaCharts, which - in the Node original too - is
// a deliberate no-op: individual ARIA chart URLs are imported one at a time
// via scrapeAriaPlaylist (a browser scrape), not listed in bulk here.
func AriaCharts() []Playlist { return nil }

var popularCountryNames = map[string]string{
	"US": "USA", "GB": "UK", "CA": "Canada", "AU": "Australia",
	"DE": "Germany", "FR": "France", "ES": "Spain", "IT": "Italy",
	"BR": "Brazil", "MX": "Mexico", "JP": "Japan", "KR": "South Korea",
	"IN": "India", "NL": "Netherlands", "SE": "Sweden", "NO": "Norway",
	"PL": "Poland", "AR": "Argentina", "CL": "Chile", "NZ": "New Zealand",
}

type deezerPlaylistEntry struct {
	ID       json.Number `json:"id"`
	Title    string      `json:"title"`
	NbTracks int         `json:"nb_tracks"`
	User     struct {
		Name string `json:"name"`
	} `json:"user"`
}

// DeezerPopularPlaylists ports getDeezerPopularPlaylists: charts + a
// country search + a handful of genre searches, deduped by playlist ID.
// The three request groups run sequentially here (vs Promise.allSettled in
// the original) - simpler code, and this only ever runs on a slow daily/
// on-demand path, not a user-facing hot loop, so the latency difference
// doesn't matter enough to bring in a goroutine/errgroup fan-out for it.
func DeezerPopularPlaylists(country string) []PopularPlaylist {
	name := popularCountryNames[country]
	if name == "" {
		name = country
	}
	seen := map[string]bool{}
	var results []PopularPlaylist

	add := func(p deezerPlaylistEntry) {
		id := p.ID.String()
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		desc := fmt.Sprintf("%d tracks", p.NbTracks)
		if p.User.Name != "" {
			desc = fmt.Sprintf("by %s · %d tracks", p.User.Name, p.NbTracks)
		}
		results = append(results, PopularPlaylist{
			Name: p.Title, URL: "https://www.deezer.com/playlist/" + id,
			Description: desc, Count: p.NbTracks,
		})
	}

	var chart struct {
		Data []deezerPlaylistEntry `json:"data"`
	}
	if err := getJSON("https://api.deezer.com/chart/0/playlists?limit=5", &chart); err == nil {
		for _, p := range chart.Data {
			add(p)
		}
	}

	var countryRes struct {
		Data []deezerPlaylistEntry `json:"data"`
	}
	q := url.QueryEscape("Top " + name)
	if err := getJSON("https://api.deezer.com/search/playlist?q="+q+"&limit=5", &countryRes); err == nil {
		for _, p := range countryRes.Data {
			add(p)
		}
	}

	for _, genre := range []string{"Pop", "Rock", "Hip Hop", "Electronic", "R&B", "Latin"} {
		var genreRes struct {
			Data []deezerPlaylistEntry `json:"data"`
		}
		q := url.QueryEscape(genre + " hits")
		if err := getJSON("https://api.deezer.com/search/playlist?q="+q+"&limit=1", &genreRes); err == nil && len(genreRes.Data) > 0 {
			add(genreRes.Data[0])
		}
	}

	return results
}

// SpotifyPopularPlaylists ports getSpotifyPopularPlaylists (scrapers.ts:1728):
// featured-playlists + toplists-category browse endpoints, deduped by ID.
// token is a Client Credentials token (see adapters/spotify.
// GetClientCredentialsToken) - the caller is responsible for obtaining it,
// since that needs DB/config access this package doesn't have.
func SpotifyPopularPlaylists(country, token string) []PopularPlaylist {
	type spotifyPlaylist struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Description  string `json:"description"`
		ExternalURLs struct {
			Spotify string `json:"spotify"`
		} `json:"external_urls"`
		Tracks struct {
			Total int `json:"total"`
		} `json:"tracks"`
	}
	get := func(rawURL string) []spotifyPlaylist {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		var out struct {
			Playlists struct {
				Items []spotifyPlaylist `json:"items"`
			} `json:"playlists"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil
		}
		return out.Playlists.Items
	}

	seen := map[string]bool{}
	var results []PopularPlaylist
	add := func(p spotifyPlaylist) {
		if p.ID == "" || seen[p.ID] {
			return
		}
		seen[p.ID] = true
		desc := p.Description
		if desc == "" {
			desc = fmt.Sprintf("%d tracks", p.Tracks.Total)
		}
		playlistURL := p.ExternalURLs.Spotify
		if playlistURL == "" {
			playlistURL = "https://open.spotify.com/playlist/" + p.ID
		}
		results = append(results, PopularPlaylist{Name: p.Name, URL: playlistURL, Description: desc, Count: p.Tracks.Total})
	}

	q := url.Values{"country": {country}, "limit": {"20"}}
	for _, p := range get("https://api.spotify.com/v1/browse/featured-playlists?" + q.Encode()) {
		add(p)
	}
	q.Set("limit", "50")
	for _, p := range get("https://api.spotify.com/v1/browse/categories/toplists/playlists?" + q.Encode()) {
		add(p)
	}
	return results
}

// SearchAppleMusicPlaylists ports searchAppleMusicPlaylists: Apple's free
// public RSS "most played" feed, no auth required.
func SearchAppleMusicPlaylists(country string) []PopularPlaylist {
	storefront := strings.ToLower(country)
	var feed struct {
		Feed struct {
			Results []struct {
				Name       string `json:"name"`
				URL        string `json:"url"`
				ArtistName string `json:"artistName"`
			} `json:"results"`
		} `json:"feed"`
	}
	rawURL := fmt.Sprintf("https://rss.applemarketingtools.com/api/v2/%s/music/most-played/25/playlists.json", storefront)
	if err := getJSON(rawURL, &feed); err != nil {
		slog.Error("[Apple Music] failed to fetch playlists", "country", country, "error", err)
		return nil
	}
	out := make([]PopularPlaylist, 0, len(feed.Feed.Results))
	for _, p := range feed.Feed.Results {
		out = append(out, PopularPlaylist{Name: p.Name, URL: p.URL, Description: p.ArtistName})
	}
	return out
}

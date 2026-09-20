// Package tmdb is a minimal client for The Movie Database's v3 API,
// covering just what Collections' external list/chart builder needs
// (DESIGN.md §11.11): a public list's items, or one of a small set of
// standard charts. No auth beyond the free v3 API key - no account/session
// endpoints, no write operations, no keyword/company/discover builders
// (out of scope for this pass - see DESIGN.md §11.11).
package tmdb

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/drevilish/playlist-lab/internal/services/medialist"
)

const baseURL = "https://api.themoviedb.org/3"

type Client struct {
	APIKey string
	http   *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{APIKey: apiKey, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) get(path string, query url.Values) ([]byte, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("no TMDb API key configured")
	}
	if query == nil {
		query = url.Values{}
	}
	query.Set("api_key", c.APIKey)
	// Collections should be imported/processed in English only - TMDb
	// already defaults to en-US when this is omitted, but pinning it
	// explicitly means a title/overview never silently shifts to whatever
	// language a future default or account setting implies.
	if query.Get("language") == "" {
		query.Set("language", "en-US")
	}
	req, err := http.NewRequest(http.MethodGet, baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TMDb request failed: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("TMDb request failed: status %d", resp.StatusCode)
	}
	return b, nil
}

// ValidateKey makes a cheap request against TMDb's own auth-check endpoint
// to confirm the key actually works, so an admin gets told immediately
// when they save a bad/expired key (DESIGN.md §11.11) rather than only
// finding out much later when a scheduled collection refresh silently
// fails. Surfaces TMDb's own status_message when it rejects the key
// (e.g. "Invalid API key: You must be granted a valid key.") rather than
// just a bare status code.
func (c *Client) ValidateKey() error {
	if c.APIKey == "" {
		return fmt.Errorf("no API key provided")
	}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/authentication?api_key="+url.QueryEscape(c.APIKey), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach TMDb: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var body struct {
			StatusMessage string `json:"status_message"`
		}
		if json.Unmarshal(b, &body) == nil && body.StatusMessage != "" {
			return fmt.Errorf("%s", body.StatusMessage)
		}
		return fmt.Errorf("TMDb rejected the key: status %d", resp.StatusCode)
	}
	return nil
}

// GetListItems fetches a public TMDb list's items - GET /list/{list_id}.
// listID accepts either a bare numeric id or a full list URL
// (themoviedb.org/list/<id>-slug), matching how a user would actually copy
// it out of their browser.
func (c *Client) GetListItems(listID string) ([]medialist.Item, error) {
	id := extractListID(listID)
	if id == "" {
		return nil, fmt.Errorf("invalid TMDb list id %q", listID)
	}
	raw, err := c.get("/list/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Items []struct {
			ID           int    `json:"id"`
			MediaType    string `json:"media_type"`
			Title        string `json:"title"`
			Name         string `json:"name"` // tv entries use "name" not "title"
			ReleaseDate  string `json:"release_date"`
			FirstAirDate string `json:"first_air_date"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := make([]medialist.Item, 0, len(body.Items))
	for _, it := range body.Items {
		title, date, mediaType := it.Title, it.ReleaseDate, it.MediaType
		if mediaType == "" {
			mediaType = "movie"
		}
		if it.Name != "" {
			title, date = it.Name, it.FirstAirDate
		}
		out = append(out, medialist.Item{
			GuidKey: "tmdb://" + strconv.Itoa(it.ID), MediaType: mediaType, Title: title, Year: yearFromDate(date),
		})
	}
	return out, nil
}

// extractListID pulls the leading numeric id off either a bare id or a
// full "https://www.themoviedb.org/list/12345-my-list" style URL.
func extractListID(v string) string {
	v = strings.TrimSpace(v)
	if slash := strings.LastIndex(v, "/list/"); slash != -1 {
		v = v[slash+len("/list/"):]
	}
	end := 0
	for end < len(v) && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	return v[:end]
}

// GetChart fetches one of a small curated set of standard TMDb charts -
// "popular" or "top_rated" - for a given media type ("movie" | "tv").
func (c *Client) GetChart(mediaType, chart string, limit int) ([]medialist.Item, error) {
	if chart != "popular" && chart != "top_rated" {
		return nil, fmt.Errorf("unsupported TMDb chart %q", chart)
	}
	if mediaType != "movie" && mediaType != "tv" {
		return nil, fmt.Errorf("unsupported TMDb media type %q", mediaType)
	}
	if limit <= 0 {
		limit = 20
	}
	var out []medialist.Item
	for page := 1; len(out) < limit && page <= 5; page++ {
		raw, err := c.get(fmt.Sprintf("/%s/%s", mediaType, chart), url.Values{"page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		var body struct {
			Results []struct {
				ID           int    `json:"id"`
				Title        string `json:"title"`
				Name         string `json:"name"`
				ReleaseDate  string `json:"release_date"`
				FirstAirDate string `json:"first_air_date"`
			} `json:"results"`
			TotalPages int `json:"total_pages"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		if len(body.Results) == 0 {
			break
		}
		for _, r := range body.Results {
			title, date := r.Title, r.ReleaseDate
			if r.Name != "" {
				title, date = r.Name, r.FirstAirDate
			}
			out = append(out, medialist.Item{
				GuidKey: "tmdb://" + strconv.Itoa(r.ID), MediaType: mediaType, Title: title, Year: yearFromDate(date),
			})
			if len(out) >= limit {
				break
			}
		}
		if page >= body.TotalPages {
			break
		}
	}
	return out, nil
}

// GetTrending fetches TMDb's own trending feed (a different URL shape than
// GetChart's /movie|tv/popular|top_rated - trending lives at
// /trending/{media_type}/{time_window}), for the external list/chart
// builder's "Trending (Daily)"/"Trending (Weekly)" modes. window is "day" or
// "week".
func (c *Client) GetTrending(mediaType, window string, limit int) ([]medialist.Item, error) {
	if window != "day" && window != "week" {
		return nil, fmt.Errorf("unsupported TMDb trending window %q", window)
	}
	if mediaType != "movie" && mediaType != "tv" {
		return nil, fmt.Errorf("unsupported TMDb media type %q", mediaType)
	}
	if limit <= 0 {
		limit = 20
	}
	var out []medialist.Item
	for page := 1; len(out) < limit && page <= 5; page++ {
		raw, err := c.get(fmt.Sprintf("/trending/%s/%s", mediaType, window), url.Values{"page": {strconv.Itoa(page)}})
		if err != nil {
			return nil, err
		}
		var body struct {
			Results []struct {
				ID           int    `json:"id"`
				Title        string `json:"title"`
				Name         string `json:"name"`
				ReleaseDate  string `json:"release_date"`
				FirstAirDate string `json:"first_air_date"`
			} `json:"results"`
			TotalPages int `json:"total_pages"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, err
		}
		if len(body.Results) == 0 {
			break
		}
		for _, r := range body.Results {
			title, date := r.Title, r.ReleaseDate
			if r.Name != "" {
				title, date = r.Name, r.FirstAirDate
			}
			out = append(out, medialist.Item{
				GuidKey: "tmdb://" + strconv.Itoa(r.ID), MediaType: mediaType, Title: title, Year: yearFromDate(date),
			})
			if len(out) >= limit {
				break
			}
		}
		if page >= body.TotalPages {
			break
		}
	}
	return out, nil
}

// CollectionSearchResult is one hit from SearchCollections - just enough to
// show a picker result and build a Franchise/Collection external-list
// source from it (GetCollectionMovies takes the same ID).
type CollectionSearchResult struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// SearchCollections finds real TMDb "collection" (franchise) ids by name -
// GET /search/collection?query=... - the direct answer to discovering a
// franchise preset instead of only having the handful this app hardcodes
// (collection_presets.go's Franchise category).
func (c *Client) SearchCollections(query string) ([]CollectionSearchResult, error) {
	raw, err := c.get("/search/collection", url.Values{"query": {query}})
	if err != nil {
		return nil, err
	}
	return decodeCollectionSearchResponse(raw)
}

// decodeCollectionSearchResponse is split out from SearchCollections so a
// test can feed it a captured response body directly, same convention as
// this package's other decode helpers.
func decodeCollectionSearchResponse(raw []byte) ([]CollectionSearchResult, error) {
	var body struct {
		Results []CollectionSearchResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	return body.Results, nil
}

// GetCollectionMovies fetches every movie in a TMDb "collection" (a
// franchise grouping, e.g. TMDb id 91361 = the Halloween Collection - GET
// /collection/{id}) - a different concept from GetListItems' user-curated
// lists, added specifically for the franchise-style presets real Kometa
// community configs build this way (meisnate12/Holiday.yml's Halloween
// Movies pulls in several tmdb_collection ids alongside its IMDb lists).
func (c *Client) GetCollectionMovies(collectionID string) ([]medialist.Item, error) {
	id := extractListID(collectionID)
	if id == "" {
		// extractListID only strips a "/list/" URL prefix - a bare numeric
		// collection id (the common case here) already round-trips as-is,
		// this just also tolerates a pasted "themoviedb.org/collection/91361-..."
		// URL the same way GetListItems does for a list URL.
		trimmed := strings.TrimSpace(collectionID)
		if slash := strings.LastIndex(trimmed, "/collection/"); slash != -1 {
			trimmed = trimmed[slash+len("/collection/"):]
		}
		end := 0
		for end < len(trimmed) && trimmed[end] >= '0' && trimmed[end] <= '9' {
			end++
		}
		id = trimmed[:end]
	}
	if id == "" {
		return nil, fmt.Errorf("invalid TMDb collection id %q", collectionID)
	}
	raw, err := c.get("/collection/"+id, nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Parts []struct {
			ID          int    `json:"id"`
			Title       string `json:"title"`
			ReleaseDate string `json:"release_date"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := make([]medialist.Item, 0, len(body.Parts))
	for _, p := range body.Parts {
		out = append(out, medialist.Item{
			GuidKey: "tmdb://" + strconv.Itoa(p.ID), MediaType: "movie", Title: p.Title, Year: yearFromDate(p.ReleaseDate),
		})
	}
	return out, nil
}

func yearFromDate(s string) int {
	if len(s) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(s[:4])
	return y
}

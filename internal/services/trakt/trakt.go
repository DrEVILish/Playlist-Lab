// Package trakt is a minimal client for Trakt's public list API, added
// specifically for Collections' external list/chart builder (DESIGN.md
// §11.11): a review of real-world Kometa community configs
// (github.com/Kometa-Team/Community-Configs) turned up Trakt public lists
// as by far the single most common collection source for anything that
// isn't a plain library facet - award-show-by-year collections in
// particular (Oscars/Golden Globes/BAFTA/Cannes/Critics Choice/Independent
// Spirit Awards/Sundance) are near-universally sourced from one curator's
// (trakt.tv/users/pjcob) yearly lists across dozens of different configs.
// No OAuth/user login needed - a public list is readable with just a free
// API app's Client ID, the same "id" (not a secret) most Trakt docs call an
// api-key.
//
// ponytail: not live-verified against a real Trakt account (no API key was
// available this session) - the request shape (headers, path) matches
// Trakt's documented v2 API precisely, but if a real key surfaces a field-
// name mismatch, decodeListResponse below is the first place to look, same
// caveat already carried by the tvdb package for the same reason.
package trakt

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

const baseURL = "https://api.trakt.tv"

type Client struct {
	APIKey string
	http   *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{APIKey: apiKey, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) get(path string) ([]byte, int, error) {
	if c.APIKey == "" {
		return nil, 0, fmt.Errorf("no Trakt Client ID configured")
	}
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", c.APIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("Trakt request failed: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

// ValidateKey mirrors tmdb.Client.ValidateKey - a cheap, always-public
// endpoint that still requires a valid api-key header, so a bad/missing
// Client ID is caught at save time (Settings > Administration > Trakt)
// rather than on the next silent scheduled refresh.
func (c *Client) ValidateKey() error {
	_, status, err := c.get("/movies/trending?limit=1")
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("Trakt rejected this Client ID (status %d)", status)
	}
	if status >= 400 {
		return fmt.Errorf("Trakt request failed: status %d", status)
	}
	return nil
}

// ListSearchResult is one hit from SearchLists - enough to show a picker
// result and build the same "https://trakt.tv/users/{user}/lists/{slug}"
// URL GetListItems/extractUserList already expect, so a search result
// slots straight into the existing external-list source shape.
type ListSearchResult struct {
	Name        string
	Description string
	ItemCount   int
	Likes       int
	URL         string
}

// SearchLists finds real public Trakt lists by name/keyword - GET
// /search/list?query=... - the direct answer to discovering a premade list
// instead of only having the handful of award shows this app hardcodes
// (collection_presets.go's Awards category uses one curator's lists; this
// searches every public list on Trakt).
func (c *Client) SearchLists(query string) ([]ListSearchResult, error) {
	b, status, err := c.get("/search/list?query=" + url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("Trakt request failed: status %d", status)
	}
	return decodeSearchListsResponse(b)
}

// decodeSearchListsResponse is split out from SearchLists so a test can
// feed it a captured response body directly, same convention as
// decodeListResponse above.
func decodeSearchListsResponse(b []byte) ([]ListSearchResult, error) {
	var entries []struct {
		List struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			ItemCount   int    `json:"item_count"`
			Likes       int    `json:"likes"`
			IDs         struct {
				Slug string `json:"slug"`
			} `json:"ids"`
			User struct {
				IDs struct {
					Slug string `json:"slug"`
				} `json:"ids"`
			} `json:"user"`
		} `json:"list"`
	}
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("unexpected Trakt response shape: %w", err)
	}
	out := make([]ListSearchResult, 0, len(entries))
	for _, e := range entries {
		if e.List.User.IDs.Slug == "" || e.List.IDs.Slug == "" {
			continue
		}
		out = append(out, ListSearchResult{
			Name: e.List.Name, Description: e.List.Description, ItemCount: e.List.ItemCount, Likes: e.List.Likes,
			URL: "https://trakt.tv/users/" + e.List.User.IDs.Slug + "/lists/" + e.List.IDs.Slug,
		})
	}
	return out, nil
}

// GetListItems fetches a public Trakt list's items - GET
// /users/{username}/lists/{list}/items. listURL accepts a full
// "https://trakt.tv/users/{username}/lists/{slug}" URL (list accepts either
// its slug or numeric id in Trakt's own API, so whichever the user pasted
// round-trips as-is). mediaType picks which of a mixed list's entries to
// keep ("movie" | "tv" - Trakt itself calls the latter "show").
func (c *Client) GetListItems(listURL, mediaType string) ([]medialist.Item, error) {
	username, list, err := extractUserList(listURL)
	if err != nil {
		return nil, err
	}
	b, status, err := c.get("/users/" + url.PathEscape(username) + "/lists/" + url.PathEscape(list) + "/items")
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("Trakt request failed: status %d", status)
	}
	return decodeListResponse(b, mediaType)
}

// decodeListResponse is split out from GetListItems so a real-key smoke
// test could feed it a captured response body directly, same convention as
// tvdb.decodeListResponse.
func decodeListResponse(b []byte, mediaType string) ([]medialist.Item, error) {
	var entries []struct {
		Type  string `json:"type"`
		Movie *struct {
			Title string `json:"title"`
			Year  int    `json:"year"`
			IDs   struct {
				TMDB int    `json:"tmdb"`
				IMDB string `json:"imdb"`
			} `json:"ids"`
		} `json:"movie"`
		Show *struct {
			Title string `json:"title"`
			Year  int    `json:"year"`
			IDs   struct {
				TMDB int    `json:"tmdb"`
				IMDB string `json:"imdb"`
			} `json:"ids"`
		} `json:"show"`
	}
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("unexpected Trakt response shape: %w", err)
	}
	out := make([]medialist.Item, 0, len(entries))
	for _, e := range entries {
		var title string
		var year, tmdbID int
		var imdbID string
		itemType := e.Type
		switch {
		case e.Movie != nil:
			title, year, tmdbID, imdbID, itemType = e.Movie.Title, e.Movie.Year, e.Movie.IDs.TMDB, e.Movie.IDs.IMDB, "movie"
		case e.Show != nil:
			title, year, tmdbID, imdbID, itemType = e.Show.Title, e.Show.Year, e.Show.IDs.TMDB, e.Show.IDs.IMDB, "tv"
		default:
			continue
		}
		if mediaType != "" && itemType != mediaType {
			continue
		}
		// Prefer the TMDb id (present for both movies and shows, and the
		// format this app's other providers already normalize to) - fall
		// back to IMDb's when Trakt didn't have a TMDb match for an entry.
		var guidKey string
		switch {
		case tmdbID != 0:
			guidKey = "tmdb://" + strconv.Itoa(tmdbID)
		case imdbID != "":
			guidKey = "imdb://" + imdbID
		default:
			continue
		}
		out = append(out, medialist.Item{GuidKey: guidKey, MediaType: itemType, Title: title, Year: year})
	}
	return out, nil
}

// extractUserList pulls (username, list) off a
// "https://trakt.tv/users/{username}/lists/{slug}?sort=..." URL - the exact
// shape a user copies out of their browser (confirmed against every
// community-config example this app's Awards presets are built from).
func extractUserList(v string) (username, list string, err error) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "https://")
	v = strings.TrimPrefix(v, "http://")
	v = strings.TrimPrefix(v, "trakt.tv/")
	parts := strings.Split(v, "/")
	if len(parts) < 4 || parts[0] != "users" || parts[2] != "lists" {
		return "", "", fmt.Errorf("invalid Trakt list URL %q - expected https://trakt.tv/users/{username}/lists/{list}", v)
	}
	list = parts[3]
	if q := strings.IndexByte(list, '?'); q != -1 {
		list = list[:q]
	}
	return parts[1], list, nil
}

// Package tvdb is a minimal client for TheTVDB's v4 API, covering just
// what Collections' external list/chart builder needs (DESIGN.md §11.11):
// a public list's items.
//
// ponytail: unlike tmdb/imdb/letterboxd (each smoke-tested live against a
// real server/page this same session), this client is written to TVDb's
// documented v4 login + list-extended shape but NOT live-verified end to
// end - no TVDb API key was available to test against (the login and
// unauthorized-list-fetch endpoints were confirmed reachable and returning
// the expected error shapes, but a real list response was never seen). If
// a real key surfaces a field-name mismatch, the fix is almost certainly
// in decodeListResponse below, not the auth flow.
package tvdb

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

const baseURL = "https://api4.thetvdb.com/v4"

type Client struct {
	APIKey string
	http   *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{APIKey: apiKey, http: &http.Client{Timeout: 20 * time.Second}}
}

// login exchanges the API key for a short-lived bearer token - TVDb v4's
// only auth mechanism (no per-request api_key query param like TMDb).
func (c *Client) login() (string, error) {
	if c.APIKey == "" {
		return "", fmt.Errorf("no TVDb API key configured")
	}
	body, _ := json.Marshal(map[string]string{"apikey": c.APIKey})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/login", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("TVDb login failed: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Status string `json:"status"`
		Data   struct {
			Token string `json:"token"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Data.Token == "" {
		return "", fmt.Errorf("TVDb login failed: %s", out.Message)
	}
	return out.Data.Token, nil
}

// GetListItems fetches a public TVDb list's items - GET
// /lists/{id}/extended. listID accepts either a bare numeric id or a full
// list URL, matching how a user would actually copy it out of their
// browser. mediaType picks which of a mixed list's entities to keep
// ("movie" | "tv" - TVDb itself calls the latter "series").
func (c *Client) GetListItems(listID, mediaType string) ([]medialist.Item, error) {
	id := extractListID(listID)
	if id == "" {
		return nil, fmt.Errorf("invalid TVDb list id %q", listID)
	}
	token, err := c.login()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/lists/"+url.PathEscape(id)+"/extended", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// Collections should be imported/processed in English only - TVDb's
	// entity "name" field is otherwise whatever the show/movie's own
	// default/original-language title is (not necessarily English), and
	// "eng" is TVDb's documented 3-letter language code for this header.
	req.Header.Set("Accept-Language", "eng")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TVDb request failed: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("TVDb request failed: status %d", resp.StatusCode)
	}
	return decodeListResponse(b, mediaType)
}

// decodeListResponse is split out from GetListItems so a real-key smoke
// test can feed it a captured response body directly (see package doc).
func decodeListResponse(b []byte, mediaType string) ([]medialist.Item, error) {
	var body struct {
		Data struct {
			Entities []struct {
				MovieID  *int   `json:"movieId"`
				SeriesID *int   `json:"seriesId"`
				Name     string `json:"name"`
				Year     string `json:"year"`
			} `json:"entities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		return nil, fmt.Errorf("unexpected TVDb response shape: %w", err)
	}
	out := make([]medialist.Item, 0, len(body.Data.Entities))
	for _, e := range body.Data.Entities {
		var guidKey, itemType string
		switch {
		case e.MovieID != nil:
			guidKey, itemType = "tvdb://"+strconv.Itoa(*e.MovieID), "movie"
		case e.SeriesID != nil:
			guidKey, itemType = "tvdb://"+strconv.Itoa(*e.SeriesID), "tv"
		default:
			continue
		}
		if mediaType != "" && itemType != mediaType {
			continue
		}
		year, _ := strconv.Atoi(e.Year)
		title := e.Name
		if title == "" {
			title = guidKey
		}
		out = append(out, medialist.Item{GuidKey: guidKey, MediaType: itemType, Title: title, Year: year})
	}
	return out, nil
}

// extractListID pulls a bare id off either a bare numeric id or a full
// "https://thetvdb.com/lists/12345-my-list" style URL.
func extractListID(v string) string {
	v = strings.TrimSpace(v)
	if slash := strings.LastIndex(v, "/lists/"); slash != -1 {
		v = v[slash+len("/lists/"):]
	}
	end := 0
	for end < len(v) && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	return v[:end]
}

package spotify

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/drevilish/playlist-lab/internal/services/scrapers"
)

// spotifyPlaylistJSON mirrors just the fields SearchPlaylists/
// GetUserPlaylists need from a Spotify playlist object - a small
// duplicate of scrapers.go's own private spotifyPlaylist shape (that one
// isn't exported, and this package can't import an unexported type from
// another package), not a shared type worth introducing for four fields.
type spotifyPlaylistJSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	ExternalURLs struct {
		Spotify string `json:"spotify"`
	} `json:"external_urls"`
	Tracks struct {
		Total int `json:"total"`
	} `json:"tracks"`
	Images []struct {
		URL string `json:"url"`
	} `json:"images"`
}

func toPopularPlaylist(p spotifyPlaylistJSON) scrapers.PopularPlaylist {
	desc := p.Description
	if desc == "" {
		desc = fmt.Sprintf("%d tracks", p.Tracks.Total)
	}
	playlistURL := p.ExternalURLs.Spotify
	if playlistURL == "" {
		playlistURL = "https://open.spotify.com/playlist/" + p.ID
	}
	image := ""
	if len(p.Images) > 0 {
		image = p.Images[0].URL
	}
	return scrapers.PopularPlaylist{Name: p.Name, URL: playlistURL, Description: desc, Count: p.Tracks.Total, Image: image}
}

func getBearerJSON(ctx context.Context, rawURL, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d from %s", resp.StatusCode, rawURL)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// SearchPlaylists searches Spotify's public catalog for playlists matching
// a free-text query, using an app-level Client Credentials token (see
// GetClientCredentialsToken) - no per-user OAuth needed, same as the
// existing chart-browsing calls in charts.go.
func SearchPlaylists(ctx context.Context, sqlDB *sql.DB, sessionSecret string, userID int64, appClientID, appClientSecret, query string) ([]scrapers.PopularPlaylist, error) {
	token, err := GetClientCredentialsToken(ctx, sqlDB, sessionSecret, userID, appClientID, appClientSecret)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, nil
	}

	q := url.Values{"type": {"playlist"}, "q": {query}, "limit": {"25"}}
	var out struct {
		Playlists struct {
			Items []spotifyPlaylistJSON `json:"items"`
		} `json:"playlists"`
	}
	if err := getBearerJSON(ctx, "https://api.spotify.com/v1/search?"+q.Encode(), token, &out); err != nil {
		return nil, err
	}
	results := make([]scrapers.PopularPlaylist, 0, len(out.Playlists.Items))
	for _, p := range out.Playlists.Items {
		if p.ID == "" {
			continue
		}
		results = append(results, toPopularPlaylist(p))
	}
	return results, nil
}

// GetUserPlaylists lists a public Spotify user's public playlists, using
// the same app-level Client Credentials token as SearchPlaylists - Spotify
// allows reading a user's public playlists this way without that user
// having connected their account to this app.
func GetUserPlaylists(ctx context.Context, sqlDB *sql.DB, sessionSecret string, userID int64, appClientID, appClientSecret, spotifyUserID string) ([]scrapers.PopularPlaylist, error) {
	token, err := GetClientCredentialsToken(ctx, sqlDB, sessionSecret, userID, appClientID, appClientSecret)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, nil
	}

	rawURL := "https://api.spotify.com/v1/users/" + url.PathEscape(spotifyUserID) + "/playlists?limit=50"
	var out struct {
		Items []spotifyPlaylistJSON `json:"items"`
	}
	if err := getBearerJSON(ctx, rawURL, token, &out); err != nil {
		return nil, err
	}
	results := make([]scrapers.PopularPlaylist, 0, len(out.Items))
	for _, p := range out.Items {
		if p.ID == "" {
			continue
		}
		results = append(results, toPopularPlaylist(p))
	}
	return results, nil
}

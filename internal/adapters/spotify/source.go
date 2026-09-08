package spotify

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/drevilish/playlist-lab/internal/adapters"
)

// Source ports adapters/spotify-source.ts's fetchTracks in full: the user's
// own OAuth token first (same GetToken every other Spotify call uses),
// falling back to the Client Credentials grant (GetClientCredentialsToken,
// already used by the charts feature) when there's no token or Spotify
// rejects it as expired/invalid, and finally to scraping the public
// open.spotify.com web page (scrape.go) when even that fails - which,
// since Spotify's Nov 2024 API changes 403 client-credentials tokens on
// most playlist reads for apps outside Extended Quota Mode, is now the
// path that actually runs for most "import someone else's public
// playlist by URL" cases, not a rare fallback for an edge case.
type Source struct {
	DB              *sql.DB
	Secret          string
	AppClientID     string
	AppClientSecret string
	httpClient      *http.Client
}

func NewSource(sqlDB *sql.DB, secret, appClientID, appClientSecret string) *Source {
	return &Source{
		DB: sqlDB, Secret: secret, AppClientID: appClientID, AppClientSecret: appClientSecret,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *Source) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: "spotify", Name: "Spotify", Icon: "spotify"}
}

var spotifyPlaylistIDPattern = regexp.MustCompile(`playlist[/:]([A-Za-z0-9]+)`)

func extractPlaylistID(urlOrID string) string {
	if m := spotifyPlaylistIDPattern.FindStringSubmatch(urlOrID); m != nil {
		return m[1]
	}
	return urlOrID
}

type spotifyPlaylistMeta struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Tracks struct {
		Total int `json:"total"`
	} `json:"tracks"`
	Images []struct {
		URL string `json:"url"`
	} `json:"images"`
}

type spotifyTracksPage struct {
	Items []struct {
		Track *struct {
			Name    string `json:"name"`
			Artists []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Album struct {
				Name string `json:"name"`
			} `json:"album"`
		} `json:"track"`
	} `json:"items"`
}

func (s *Source) getJSON(url, token string, out any) (status int, err error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var errBody struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.Error.Message != "" {
			return resp.StatusCode, fmt.Errorf("%s", errBody.Error.Message)
		}
		return resp.StatusCode, fmt.Errorf("Spotify request failed: status %d", resp.StatusCode)
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}

// fetchWithToken fetches a playlist's metadata and every track page using
// one already-resolved access token - shared by the per-user-token and
// client-credentials paths in FetchTracks below, which differ only in how
// they got that token.
func (s *Source) fetchWithToken(playlistID, token string) (adapters.PlaylistInfo, []adapters.TrackInfo, int, error) {
	var meta spotifyPlaylistMeta
	status, err := s.getJSON(
		"https://api.spotify.com/v1/playlists/"+playlistID+"?fields=id,name,tracks(total),images", token, &meta)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, status, err
	}

	var tracks []adapters.TrackInfo
	for offset := 0; offset < meta.Tracks.Total; offset += 100 {
		var page spotifyTracksPage
		pageURL := fmt.Sprintf(
			"https://api.spotify.com/v1/playlists/%s/tracks?limit=100&offset=%d&fields=items(track(name,artists,album))",
			playlistID, offset)
		if status, err := s.getJSON(pageURL, token, &page); err != nil {
			return adapters.PlaylistInfo{}, nil, status, err
		}
		for _, item := range page.Items {
			if item.Track == nil {
				continue
			}
			artist := "Unknown Artist"
			if len(item.Track.Artists) > 0 {
				artist = item.Track.Artists[0].Name
			}
			tracks = append(tracks, adapters.TrackInfo{Title: item.Track.Name, Artist: artist, Album: item.Track.Album.Name})
		}
	}

	cover := ""
	if len(meta.Images) > 0 {
		cover = meta.Images[0].URL
	}
	playlist := adapters.PlaylistInfo{ID: "spotify-" + meta.ID, Name: meta.Name, TrackCount: len(tracks), CoverURL: cover}
	return playlist, tracks, 0, nil
}

func (s *Source) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	playlistID := extractPlaylistID(playlistURLOrID)

	if token, err := GetToken(s.DB, s.Secret, userID); err == nil && token != "" {
		playlist, tracks, status, err := s.fetchWithToken(playlistID, token)
		if err == nil {
			return playlist, tracks, nil
		}
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			return adapters.PlaylistInfo{}, nil, err
		}
		// Falls through to the client-credentials attempt below, same as
		// fetchTracks()'s 401/403 handling in spotify-source.ts.
	}

	token, err := GetClientCredentialsToken(s.DB, s.Secret, userID, s.AppClientID, s.AppClientSecret)
	if err == nil && token != "" {
		if playlist, tracks, _, err := s.fetchWithToken(playlistID, token); err == nil {
			return playlist, tracks, nil
		}
		// Falls through to scraping below - most commonly a 403 from
		// Spotify's Extended Quota Mode restriction on client-credentials
		// tokens, but any API failure is worth one more try via the
		// public web page rather than failing the whole import.
	}

	return fetchTracksByScraping(ctx, playlistID)
}

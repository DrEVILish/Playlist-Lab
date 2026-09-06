package deezer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/drevilish/playlist-lab/internal/adapters"
)

// Source ports adapters/deezer-source.ts: reading a Deezer playlist is a
// plain, unauthenticated public API call - no browser scraping needed,
// unlike Spotify/Apple/Tidal's source adapters.
type Source struct {
	httpClient *http.Client
}

func NewSource() *Source {
	return &Source{httpClient: &http.Client{Timeout: 15 * time.Second}}
}

func (s *Source) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "Deezer", Icon: "deezer"}
}

var deezerPlaylistIDPattern = regexp.MustCompile(`playlist/(\d+)`)

func extractPlaylistID(urlOrID string) string {
	if m := deezerPlaylistIDPattern.FindStringSubmatch(urlOrID); m != nil {
		return m[1]
	}
	return urlOrID
}

func (s *Source) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	playlistID := extractPlaylistID(playlistURLOrID)

	resp, err := s.httpClient.Get("https://api.deezer.com/playlist/" + playlistID)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("failed to scrape Deezer playlist: %w", err)
	}
	defer resp.Body.Close()

	// A 403 from Deezer's public API means the playlist itself is
	// unreachable (deleted, made private, or region-restricted) - not a
	// transient network/auth problem, so say that instead of a generic
	// status-code message.
	if resp.StatusCode == http.StatusForbidden {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("this Deezer playlist is unavailable (it may have been deleted, made private, or is region-restricted)")
	}

	var data struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		PictureXL   string `json:"picture_xl"`
		PictureBig  string `json:"picture_big"`
		PictureMed  string `json:"picture_medium"`
		Picture     string `json:"picture"`
		Tracks      struct {
			Data []struct {
				Title  string `json:"title"`
				Artist struct {
					Name string `json:"name"`
				} `json:"artist"`
				Album struct {
					Title string `json:"title"`
				} `json:"album"`
			} `json:"data"`
		} `json:"tracks"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("failed to scrape Deezer playlist: %w", err)
	}
	if data.Error != nil {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("%s", orDefault(data.Error.Message, "Deezer API error"))
	}

	tracks := make([]adapters.TrackInfo, len(data.Tracks.Data))
	for i, t := range data.Tracks.Data {
		artist := t.Artist.Name
		if artist == "" {
			artist = "Unknown"
		}
		tracks[i] = adapters.TrackInfo{Title: t.Title, Artist: artist, Album: t.Album.Title}
	}

	cover := firstNonEmpty(data.PictureXL, data.PictureBig, data.PictureMed, data.Picture)
	name := data.Title
	if name == "" {
		name = "Deezer Playlist"
	}
	playlist := adapters.PlaylistInfo{
		ID: "deezer-" + playlistID, Name: name, TrackCount: len(tracks), CoverURL: cover,
	}
	return playlist, tracks, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

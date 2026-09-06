package listenbrainz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/drevilish/playlist-lab/internal/adapters"
)

// Source ports adapters/listenbrainz-source.ts's fetchTracks: fetching a
// playlist by MBID is a plain public API call.
//
// listPlaylists() is NOT ported: the Node version reads a
// users.listenbrainz_username column that was never actually added to
// schema.sql (confirmed by reading it directly) - that query throws a raw
// SQL "no such column" error in the real app rather than the friendly
// "no username configured" message the code implies, so there is nothing
// working to port. Add it properly (with a real migration) if this
// feature is wanted.
type Source struct {
	httpClient *http.Client
}

func NewSource() *Source {
	return &Source{httpClient: &http.Client{Timeout: 15 * time.Second}}
}

func (s *Source) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: serviceName, Name: "ListenBrainz", Icon: "listenbrainz"}
}

var mbidPattern = regexp.MustCompile(`(?i)playlist/([0-9a-f-]{36})`)

func extractMBID(urlOrMBID string) string {
	if m := mbidPattern.FindStringSubmatch(urlOrMBID); m != nil {
		return m[1]
	}
	return urlOrMBID
}

func (s *Source) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	mbid := extractMBID(playlistURLOrID)

	resp, err := s.httpClient.Get(apiBase + "/1/playlist/" + mbid)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("ListenBrainz playlist not found: %s", mbid)
	}

	var data struct {
		Playlist struct {
			Title string `json:"title"`
			Track []struct {
				Title   string `json:"title"`
				Creator string `json:"creator"`
			} `json:"track"`
			Extension struct {
				JSPF struct {
					Picture string `json:"picture"`
				} `json:"https://musicbrainz.org/doc/jspf#playlist"`
			} `json:"extension"`
		} `json:"playlist"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	if data.Playlist.Title == "" && len(data.Playlist.Track) == 0 {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("ListenBrainz playlist not found: %s", mbid)
	}

	tracks := make([]adapters.TrackInfo, len(data.Playlist.Track))
	for i, t := range data.Playlist.Track {
		title, artist := t.Title, t.Creator
		if title == "" {
			title = "Unknown"
		}
		if artist == "" {
			artist = "Unknown"
		}
		tracks[i] = adapters.TrackInfo{Title: title, Artist: artist}
	}

	name := data.Playlist.Title
	if name == "" {
		name = "ListenBrainz Playlist"
	}
	playlist := adapters.PlaylistInfo{
		ID: "listenbrainz-" + mbid, Name: name, TrackCount: len(tracks),
		CoverURL: data.Playlist.Extension.JSPF.Picture,
	}
	return playlist, tracks, nil
}

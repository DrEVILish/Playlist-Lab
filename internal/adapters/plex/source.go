package plex

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	plexsvc "github.com/drevilish/playlist-lab/internal/services/plex"
)

// Source ports adapters/plex-source.ts: importing FROM the user's own Plex
// library (or a Plex Home managed user's library, via a
// "plex-home:{userId}:{playlistId}" compound ID).
type Source struct {
	DB   *sql.DB
	Auth *auth.PlexClient
}

func NewSource(sqlDB *sql.DB, authClient *auth.PlexClient) *Source {
	return &Source{DB: sqlDB, Auth: authClient}
}

func (s *Source) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: "plex", Name: "Plex", Icon: "plex"}
}

func toPlaylistInfo(p plexsvc.Playlist, serverURL, token string) adapters.PlaylistInfo {
	cover := ""
	if p.Composite != "" {
		cover = serverURL + p.Composite + "?X-Plex-Token=" + token
	}
	return adapters.PlaylistInfo{ID: p.RatingKey, Name: p.Title, TrackCount: p.LeafCount, DurationMs: p.Duration, CoverURL: cover}
}

func (s *Source) ListPlaylists(ctx context.Context, userID int64) ([]adapters.PlaylistInfo, error) {
	userServer, err := db.GetUserServer(s.DB, userID)
	if err != nil {
		return nil, err
	}
	if userServer == nil {
		return nil, fmt.Errorf("no Plex server configured for this user")
	}
	user, err := db.GetUserByID(s.DB, userID)
	if err != nil {
		return nil, err
	}

	client := plexsvc.NewClient(userServer.ServerURL, user.PlexToken, s.Auth.ClientID, "Playlist Lab")
	playlists, err := client.GetPlaylists()
	if err != nil {
		return nil, err
	}

	var out []adapters.PlaylistInfo
	for _, p := range playlists {
		if p.PlaylistType == "audio" {
			out = append(out, toPlaylistInfo(p, userServer.ServerURL, user.PlexToken))
		}
	}
	return out, nil
}

// FetchTracks accepts either a plain playlist ratingKey, or a compound ID
// "plex-home:{plexHomeUserId}:{playlistId}" to fetch from a Plex Home
// managed user's library instead of the caller's own.
func (s *Source) FetchTracks(ctx context.Context, playlistURLOrID string, userID int64) (adapters.PlaylistInfo, []adapters.TrackInfo, error) {
	userServer, err := db.GetUserServer(s.DB, userID)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	if userServer == nil {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("no Plex server configured for this user")
	}
	user, err := db.GetUserByID(s.DB, userID)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	token := user.PlexToken
	playlistID := playlistURLOrID
	if strings.HasPrefix(playlistURLOrID, "plex-home:") {
		parts := strings.SplitN(playlistURLOrID, ":", 3)
		if len(parts) < 3 {
			return adapters.PlaylistInfo{}, nil, fmt.Errorf("invalid plex-home source ID format: %s", playlistURLOrID)
		}
		plexHomeUserID, playlistPart := parts[1], parts[2]
		playlistID = playlistPart
		token, err = s.Auth.SwitchToManagedUser(user.PlexToken, plexHomeUserID)
		if err != nil {
			return adapters.PlaylistInfo{}, nil, err
		}
	}

	client := plexsvc.NewClient(userServer.ServerURL, token, s.Auth.ClientID, "Playlist Lab")

	playlists, err := client.GetPlaylists()
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}
	var plexPlaylist *plexsvc.Playlist
	for i := range playlists {
		if playlists[i].RatingKey == playlistID {
			plexPlaylist = &playlists[i]
			break
		}
	}
	if plexPlaylist == nil {
		return adapters.PlaylistInfo{}, nil, fmt.Errorf("playlist %s not found on Plex server", playlistID)
	}

	tracks, err := client.GetPlaylistTracks(playlistID)
	if err != nil {
		return adapters.PlaylistInfo{}, nil, err
	}

	trackInfos := make([]adapters.TrackInfo, len(tracks))
	for i, t := range tracks {
		trackInfos[i] = adapters.TrackInfo{Title: t.Title, Artist: t.DisplayArtist(), Album: t.ParentTitle}
	}

	return toPlaylistInfo(*plexPlaylist, userServer.ServerURL, token), trackInfos, nil
}

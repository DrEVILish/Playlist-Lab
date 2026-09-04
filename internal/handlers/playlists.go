package handlers

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

type PlaylistsHandler struct {
	DB       *sql.DB
	PlexAuth *auth.PlexClient
	Tmpl     *Templates
}

func RegisterPlaylists(r chi.Router, mw *auth.Middleware, h *PlaylistsHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/", h.index)
		r.Get("/playlists/{plexId}", h.editor)
		r.Put("/playlists/{plexId}/tracks/{trackId}/move", h.moveTrack)
		r.Delete("/playlists/{plexId}/tracks/{trackId}", h.removeTrack)
		r.Delete("/playlists/{plexId}", h.deletePlaylist)
	})
}

// client builds a plex.Client for the current user's selected server,
// resolving whether to use their account token or the server-specific one
// (see plex.ResolveToken). Returns nil, nil if no server is selected yet.
func (h *PlaylistsHandler) client(user *db.User) (*plex.Client, *db.UserServer, error) {
	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil {
		return nil, userServer, err
	}
	token := plex.ResolveToken(user.PlexToken, userServer.AccessToken.String)
	return plex.NewClient(userServer.ServerURL, token, h.PlexAuth.ClientID, "Playlist Lab"), userServer, nil
}

type playlistRow struct {
	PlexID     string
	Name       string
	Source     string
	TrackCount int
	Duration   int64
}

func (h *PlaylistsHandler) index(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	client, userServer, err := h.client(user)
	if err != nil {
		slog.Error("failed to load user server", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if userServer == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	plexPlaylists, err := client.GetPlaylists()
	if err != nil {
		slog.Error("failed to fetch playlists from Plex", "error", err)
		h.Tmpl.RenderPage(w, "home", map[string]any{"User": user, "PlexError": true})
		return
	}

	tracked, err := db.GetUserPlaylists(h.DB, user.ID)
	if err != nil {
		slog.Error("failed to load tracked playlists", "error", err)
	}
	trackedByPlexID := make(map[string]db.Playlist, len(tracked))
	for _, p := range tracked {
		trackedByPlexID[p.PlexPlaylistID] = p
	}

	var rows []playlistRow
	for _, p := range plexPlaylists {
		if p.PlaylistType != "audio" {
			continue
		}
		source := "plex"
		if t, ok := trackedByPlexID[p.RatingKey]; ok {
			source = t.Source
		}
		rows = append(rows, playlistRow{
			PlexID: p.RatingKey, Name: cleanPlaylistName(p.Title),
			Source: source, TrackCount: p.LeafCount, Duration: p.Duration,
		})
	}

	h.Tmpl.RenderPage(w, "home", map[string]any{"User": user, "Playlists": rows})
}

// cleanPlaylistName strips a duplicated leading prefix Plex sometimes
// produces (e.g. "All out - All out 60s" -> "All out 60s"), matching the
// Node route's cleanup.
func cleanPlaylistName(name string) string {
	before, after, found := strings.Cut(name, " - ")
	if !found {
		return name
	}
	firstWord, _, _ := strings.Cut(after, " ")
	if before == firstWord {
		return after
	}
	return name
}

type trackRow struct {
	RatingKey      string
	PlaylistItemID string
	Title          string
	Artist         string
	Album          string
	Codec          string
}

func (h *PlaylistsHandler) editor(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	tracks, err := client.GetPlaylistTracks(plexID)
	if err != nil {
		slog.Error("failed to fetch playlist tracks", "error", err, "plexId", plexID)
		http.Error(w, "Failed to load playlist", http.StatusBadGateway)
		return
	}

	h.Tmpl.RenderPage(w, "editor", map[string]any{
		"PlexID": plexID,
		"Tracks": toTrackRows(tracks),
	})
}

func toTrackRows(tracks []plex.Track) []trackRow {
	rows := make([]trackRow, len(tracks))
	for i, t := range tracks {
		itemID := ""
		if t.PlaylistItemID != 0 {
			itemID = strconv.Itoa(t.PlaylistItemID)
		}
		rows[i] = trackRow{
			RatingKey: t.RatingKey, PlaylistItemID: itemID,
			Title: t.Title, Artist: t.DisplayArtist(), Album: t.ParentTitle, Codec: t.Codec(),
		}
	}
	return rows
}

func (h *PlaylistsHandler) moveTrack(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	trackID := chi.URLParam(r, "trackId")
	afterID := r.FormValue("afterId")

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	if err := client.MovePlaylistItem(plexID, trackID, afterID); err != nil {
		slog.Error("failed to move track", "error", err)
		http.Error(w, "Failed to reorder track", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PlaylistsHandler) removeTrack(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	trackID := chi.URLParam(r, "trackId")

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	if err := client.RemoveFromPlaylist(plexID, trackID); err != nil {
		slog.Error("failed to remove track", "error", err)
		http.Error(w, "Failed to remove track", http.StatusBadGateway)
		return
	}
	if tracked, _ := db.GetPlaylistByPlexID(h.DB, user.ID, plexID); tracked != nil {
		_ = db.TouchPlaylist(h.DB, tracked.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PlaylistsHandler) deletePlaylist(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")

	client, userServer, err := h.client(user)
	if err != nil || userServer == nil {
		http.Error(w, "no server selected", http.StatusBadRequest)
		return
	}
	if err := client.DeletePlaylist(plexID); err != nil {
		slog.Error("failed to delete playlist in Plex", "error", err)
		http.Error(w, "Failed to delete playlist", http.StatusBadGateway)
		return
	}
	if tracked, _ := db.GetPlaylistByPlexID(h.DB, user.ID, plexID); tracked != nil {
		_ = db.DeletePlaylistRow(h.DB, tracked.ID)
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

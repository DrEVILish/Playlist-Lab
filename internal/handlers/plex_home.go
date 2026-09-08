// Plex Home users, ported from routes/plex-home.ts: browse the other
// managed/Home users on the admin's Plex account and copy one of their
// playlists into the current user's own account. Surfaced as an "Import"
// source, matching where the React app moved this to
// (components/ImportFromPlexHome.tsx's own doc comment: the old standalone
// Plex Home Users page's per-user playlist *management* - reordering, cover
// upload, add/replace tracks - never made the move, only this copy-to-self
// action did, since that's playlist import rather than playlist
// management).
package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

func RegisterPlexHome(r chi.Router, mw *auth.Middleware, h *ImportHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/import/plex-home/users", h.plexHomeUsers)
		r.Get("/import/plex-home/users/{homeUserId}/playlists", h.plexHomeUserPlaylists)
		r.Post("/import/plex-home/playlists/{playlistId}/copy", h.plexHomeCopyPlaylist)
	})
}

func (h *ImportHandler) plexHomeUsers(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	dbUser, err := db.GetUserByID(h.DB, user.ID)
	if err != nil || dbUser.PlexToken == "" {
		http.Error(w, "No Plex token found", http.StatusBadRequest)
		return
	}
	users, err := h.PlexAuth.GetHomeUsersDetailed(dbUser.PlexToken)
	if err != nil {
		http.Error(w, "Failed to fetch Plex Home users", http.StatusBadGateway)
		return
	}
	h.Tmpl.RenderPartial(w, "partials/plex_home_users.html", map[string]any{"Users": users})
}

func (h *ImportHandler) plexHomeUserPlaylists(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	homeUserID := chi.URLParam(r, "homeUserId")

	dbUser, err := db.GetUserByID(h.DB, user.ID)
	if err != nil || dbUser.PlexToken == "" {
		http.Error(w, "No Plex token found", http.StatusBadRequest)
		return
	}
	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil {
		http.Error(w, "No Plex server configured", http.StatusBadRequest)
		return
	}

	// Prefer the home user's own token so only playlists they created show
	// up; fall back to the admin token (showing every playlist on the
	// server, unfiltered) if the switch fails - same fallback
	// routes/plex-home.ts's GET /users/:id/playlists uses, and for the same
	// reason: a managed (non-guest) user's token doesn't always carry
	// separate library permissions worth failing the whole request over.
	token, err := h.PlexAuth.SwitchToManagedUser(dbUser.PlexToken, homeUserID)
	usedAdminToken := false
	if err != nil {
		token = plex.ResolveToken(dbUser.PlexToken, userServer.AccessToken.String)
		usedAdminToken = true
	}

	client := plex.NewClient(userServer.ServerURL, token, h.PlexAuth.ClientID, "Playlist Lab")
	playlists, err := client.GetPlaylists()
	if err != nil {
		http.Error(w, "Failed to fetch home user playlists", http.StatusBadGateway)
		return
	}

	type playlistOption struct {
		ID, Name       string
		TrackCount     int
		SourceHomeUser string
	}
	rows := make([]playlistOption, 0, len(playlists))
	for _, p := range playlists {
		if p.PlaylistType != "audio" {
			continue
		}
		rows = append(rows, playlistOption{ID: p.RatingKey, Name: cleanPlaylistName(p.Title), TrackCount: p.LeafCount, SourceHomeUser: homeUserID})
	}

	h.Tmpl.RenderPartial(w, "partials/plex_home_playlists.html", map[string]any{
		"Playlists": rows, "UsedAdminToken": usedAdminToken, "HomeUserID": homeUserID,
	})
}

// plexHomeCopyPlaylist ports POST /api/plex-home/playlists/:playlistId/copy,
// scoped to this app's only real caller of it (ImportFromPlexHome.tsx):
// always copying into the current user's own account, never to a third
// home user. The TS route's targetHomeUserId branch existed for a
// standalone Plex Home management page that itself was never carried over
// to this rewrite (per this file's own package doc), so there is no
// caller left that ever sets it to anything but "current".
func (h *ImportHandler) plexHomeCopyPlaylist(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	playlistID := chi.URLParam(r, "playlistId")
	_ = r.ParseForm()
	sourceHomeUserID := r.FormValue("sourceHomeUserId")
	newName := r.FormValue("newName")
	if sourceHomeUserID == "" {
		http.Error(w, "sourceHomeUserId is required", http.StatusBadRequest)
		return
	}

	dbUser, err := db.GetUserByID(h.DB, user.ID)
	if err != nil || dbUser.PlexToken == "" {
		http.Error(w, "No Plex token found", http.StatusBadRequest)
		return
	}
	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil || !userServer.LibraryID.Valid {
		http.Error(w, "No Plex server/library configured", http.StatusBadRequest)
		return
	}

	sourceToken, err := h.PlexAuth.SwitchToManagedUser(dbUser.PlexToken, sourceHomeUserID)
	if err != nil {
		http.Error(w, "Failed to access source user", http.StatusBadGateway)
		return
	}
	sourceClient := plex.NewClient(userServer.ServerURL, sourceToken, h.PlexAuth.ClientID, "Playlist Lab")

	tracks, err := sourceClient.GetPlaylistTracks(playlistID)
	if err != nil {
		http.Error(w, "Failed to load source playlist", http.StatusBadGateway)
		return
	}
	playlists, err := sourceClient.GetPlaylists()
	if err != nil {
		http.Error(w, "Failed to load source playlist", http.StatusBadGateway)
		return
	}
	var sourceTitle, sourceComposite string
	for _, p := range playlists {
		if p.RatingKey == playlistID {
			sourceTitle, sourceComposite = p.Title, p.Composite
			break
		}
	}
	if newName == "" {
		newName = sourceTitle
	}

	machineID, err := sourceClient.GetMachineIdentifier()
	if err != nil {
		http.Error(w, "Failed to reach Plex server", http.StatusBadGateway)
		return
	}
	trackURIs := make([]string, 0, len(tracks))
	for _, t := range tracks {
		if t.RatingKey != "" {
			trackURIs = append(trackURIs, sourceClient.BuildTrackURI(t.RatingKey, machineID))
		}
	}

	targetToken := plex.ResolveToken(dbUser.PlexToken, userServer.AccessToken.String)
	targetClient := plex.NewClient(userServer.ServerURL, targetToken, h.PlexAuth.ClientID, "Playlist Lab")
	libraryURI := targetClient.BuildLibraryURI(userServer.LibraryID.String, machineID)
	newPlaylist, err := targetClient.CreatePlaylist(newName, libraryURI, trackURIs)
	if err != nil {
		http.Error(w, "Failed to create playlist", http.StatusBadGateway)
		return
	}

	if sourceComposite != "" {
		// The download request authenticates via the token embedded in the
		// URL itself (UploadPlaylistPoster attaches no header of its own),
		// same as plex-home.ts's own `${serverUrl}${composite}?X-Plex-Token=...`.
		coverURL := userServer.ServerURL + sourceComposite + "?X-Plex-Token=" + url.QueryEscape(sourceToken)
		if err := targetClient.UploadPlaylistPoster(newPlaylist.RatingKey, coverURL); err != nil {
			// Non-fatal, matching plex-home.ts's own try/catch around cover
			// copying: the playlist itself already exists with its tracks.
			slog.Warn("plex-home copy: failed to copy cover art", "error", err, "playlistId", playlistID)
		}
	}

	progress := 100
	h.Notifications.Add(user.ID, notifications.TypeImport, fmt.Sprintf("Imported from Plex Home: %s", newName), "Added", notifications.StatusSuccess, &progress)
	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{"Notifications": h.Notifications.List(user.ID)})
}

package handlers

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

type ServersHandler struct {
	DB            *sql.DB
	Plex          *auth.PlexClient
	Tmpl          *Templates
	Notifications *notifications.Store
}

func RegisterServers(r chi.Router, mw *auth.Middleware, h *ServersHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/setup", h.setupPage)
		r.Get("/setup/libraries", h.setupLibraries)
		r.Post("/servers/select", h.selectServer)

		// Settings' Plex Servers tab (DESIGN.md §11.11) - ongoing
		// management (add/remove/default) once a user has completed /setup
		// at least once.
		r.Get("/settings/servers", h.serversPanel)
		r.Get("/settings/servers/new", h.newServerForm)
		r.Post("/settings/servers", h.addServer)
		r.Post("/settings/servers/{id}/default", h.setDefaultServer)
		r.Post("/settings/servers/{id}/refresh", h.refreshServerConnection)
		r.Delete("/settings/servers/{id}", h.removeServer)
	})
}

// serverOption is the compound "name|clientId|url" value packed into the
// <select> so the chosen server's connection details round-trip through the
// form. The access token is deliberately excluded here: this value is sent
// to the browser as plain HTML (visible in page source/DevTools), so the
// token is re-resolved server-side by ClientID via resolveServerAccessToken
// whenever it's actually needed instead of ever leaving the server.
type serverOption struct {
	Name, ClientID, URL string
}

func encodeServerOption(s auth.PlexServer) string {
	return strings.Join([]string{s.Name, s.ClientIdentifier, s.BestURL()}, "|")
}

func decodeServerOption(v string) (serverOption, bool) {
	parts := strings.SplitN(v, "|", 3)
	if len(parts) != 3 {
		return serverOption{}, false
	}
	return serverOption{Name: parts[0], ClientID: parts[1], URL: parts[2]}, true
}

// resolveServerAccessToken also returns whether the Plex account owns this
// server (vs being a shared/managed user on it) - Collections' access gate,
// DESIGN.md §11.11.
func (h *ServersHandler) resolveServerAccessToken(plexToken, clientID string) (string, bool, error) {
	servers, err := h.Plex.GetServers(plexToken)
	if err != nil {
		return "", false, err
	}
	for _, s := range servers {
		if s.ClientIdentifier == clientID {
			return s.AccessToken, s.Owned, nil
		}
	}
	return "", false, nil
}

// setupPage is first-login-only onboarding (DESIGN.md §11.11): once a user
// has linked at least one server, they never see this page again - all
// further server management (adding another, changing the default,
// removing one) happens through Settings instead. The only way back here
// is a fresh account (a Playlist-Lab admin deleting the user row cascades
// their user_servers rows too, so a re-created account naturally has zero
// and hits this gate again).
func (h *ServersHandler) setupPage(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	if servers, _ := db.GetUserServers(h.DB, user.ID); len(servers) > 0 {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	servers, err := h.Plex.GetServers(user.PlexToken)
	if err != nil {
		slog.Error("failed to get Plex servers", "error", err)
		http.Error(w, "Failed to fetch your Plex servers", http.StatusBadGateway)
		return
	}
	type option struct{ Value, Name string }
	options := make([]option, len(servers))
	for i, s := range servers {
		options[i] = option{Value: encodeServerOption(s), Name: s.Name}
	}
	h.Tmpl.RenderPage(w, r, "setup", map[string]any{"Servers": options})
}

func (h *ServersHandler) setupLibraries(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	opt, ok := decodeServerOption(r.URL.Query().Get("server"))
	if !ok {
		w.Write(nil)
		return
	}
	accessToken, _, err := h.resolveServerAccessToken(user.PlexToken, opt.ClientID)
	if err != nil {
		slog.Error("failed to resolve Plex server access token", "error", err)
		h.Tmpl.RenderPartial(w, "partials/libraries_select.html", map[string]any{"Error": "Failed to load libraries from that server."})
		return
	}
	client := plex.NewClient(opt.URL, plex.ResolveToken(user.PlexToken, accessToken), h.Plex.ClientID, "Playlist Lab")
	libraries, err := client.GetLibraries()
	if err != nil {
		slog.Error("failed to get libraries", "error", err)
		h.Tmpl.RenderPartial(w, "partials/libraries_select.html", map[string]any{"Error": "Failed to load libraries from that server."})
		return
	}
	type option struct{ Value, Name string }
	var music []option
	for _, lib := range libraries {
		if lib.Type == "artist" {
			music = append(music, option{Value: lib.ID + "|" + lib.Name, Name: lib.Name})
		}
	}
	h.Tmpl.RenderPartial(w, "partials/libraries_select.html", map[string]any{"Libraries": music})
}

// addServerFromSelection resolves a submitted server+library form
// selection and persists it as a new linked server (DESIGN.md §11.11) -
// shared by the first-run wizard (selectServer) and Settings' "add another
// server" flow (addServer), which differ only in how they respond once
// the server is saved.
func (h *ServersHandler) addServerFromSelection(r *http.Request) (*db.UserServer, error) {
	user := auth.CurrentUser(r)
	opt, ok := decodeServerOption(r.FormValue("server"))
	if !ok {
		return nil, errServerRequired
	}
	libID, libName, _ := strings.Cut(r.FormValue("library"), "|")

	accessToken, owned, err := h.resolveServerAccessToken(user.PlexToken, opt.ClientID)
	if err != nil {
		return nil, err
	}
	return db.AddUserServer(h.DB, user.ID, opt.Name, opt.ClientID, opt.URL, libID, libName, accessToken, owned)
}

var errServerRequired = &serverSelectionError{"server is required"}

type serverSelectionError struct{ msg string }

func (e *serverSelectionError) Error() string { return e.msg }

// selectServer is the first-run wizard's submit (POST /servers/select) -
// unchanged behavior, a full-page redirect to /.
func (h *ServersHandler) selectServer(w http.ResponseWriter, r *http.Request) {
	if _, err := h.addServerFromSelection(r); err != nil {
		if err == errServerRequired {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		slog.Error("failed to save server selection", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

// serverView adds display-only fields for the Settings server list -
// cascade counts baked into the Remove button's confirm text (DESIGN.md
// §11.11: removing a server cascades its collections/schedules, which must
// be surfaced before deleting, not left as a silent side effect).
type serverView struct {
	db.UserServer
	CollectionCount int
	ScheduleCount   int
}

func (h *ServersHandler) renderServerList(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	servers, err := db.GetUserServers(h.DB, user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	views := make([]serverView, len(servers))
	for i, s := range servers {
		collections, schedules, _ := db.CascadeCountsForServer(h.DB, s.ID)
		views[i] = serverView{UserServer: s, CollectionCount: collections, ScheduleCount: schedules}
	}
	h.Tmpl.RenderPartial(w, "partials/settings_servers.html", map[string]any{"Servers": views})
}

func (h *ServersHandler) serversPanel(w http.ResponseWriter, r *http.Request) {
	h.renderServerList(w, r)
}

// newServerForm renders the "add another server" modal - the same
// server/library picker the first-run wizard uses, reused for a second (or
// third...) server rather than a duplicated form (DESIGN.md §11.11).
func (h *ServersHandler) newServerForm(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	servers, err := h.Plex.GetServers(user.PlexToken)
	if err != nil {
		slog.Error("failed to get Plex servers", "error", err)
		http.Error(w, "Failed to fetch your Plex servers", http.StatusBadGateway)
		return
	}
	type option struct{ Value, Name string }
	options := make([]option, len(servers))
	for i, s := range servers {
		options[i] = option{Value: encodeServerOption(s), Name: s.Name}
	}
	h.Tmpl.RenderPartial(w, "partials/server_picker.html", map[string]any{"Servers": options})
}

// addServer is Settings' "add another server" submit (POST
// /settings/servers) - addServerFromSelection's other caller, alongside
// the first-run wizard's selectServer; re-renders the server list in
// place instead of redirecting.
func (h *ServersHandler) addServer(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	server, err := h.addServerFromSelection(r)
	if err != nil {
		if err == errServerRequired {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		slog.Error("failed to add server", "error", err)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Add Plex server", err.Error(), notifications.StatusError, nil)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Add Plex server", server.ServerName, notifications.StatusSuccess, nil)
	h.renderServerList(w, r)
}

func (h *ServersHandler) setDefaultServer(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid server id", http.StatusBadRequest)
		return
	}
	if err := db.SetDefaultServer(h.DB, user.ID, id); err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Server not found", http.StatusNotFound)
			return
		}
		slog.Error("failed to set default server", "error", err, "serverId", id)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Set default server", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to set default server", http.StatusInternalServerError)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Set default server", "Updated", notifications.StatusSuccess, nil)
	h.renderServerList(w, r)
}

// removeServer verifies ownership before deleting (not just relying on
// RemoveUserServer's own WHERE ... AND user_id = ? - CascadeCountsForServer
// below has no such check of its own, and must not run against a server
// this user doesn't own).
func (h *ServersHandler) removeServer(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid server id", http.StatusBadRequest)
		return
	}
	server, err := db.GetUserServerByID(h.DB, id)
	if err != nil || server == nil || server.UserID != user.ID {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	if err := db.RemoveUserServer(h.DB, user.ID, id); err != nil {
		slog.Error("failed to remove server", "error", err, "serverId", id)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Remove server", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to remove server", http.StatusInternalServerError)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Remove server", server.ServerName, notifications.StatusSuccess, nil)
	h.renderServerList(w, r)
}

// refreshServerConnection re-resolves BestURL() from a fresh GetServers()
// call and updates the stored connection URL - for when network topology
// changes which connection (local/remote/relay) is actually best after a
// server was originally linked (DESIGN.md §11.11). On-demand only, no
// background polling.
func (h *ServersHandler) refreshServerConnection(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid server id", http.StatusBadRequest)
		return
	}
	server, err := db.GetUserServerByID(h.DB, id)
	if err != nil || server == nil || server.UserID != user.ID {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	servers, err := h.Plex.GetServers(user.PlexToken)
	if err != nil {
		slog.Error("failed to refresh server connection: could not reach Plex", "error", err, "serverId", id)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Refresh server connection", "Failed to reach Plex", notifications.StatusError, nil)
		http.Error(w, "Failed to reach Plex", http.StatusBadGateway)
		return
	}
	found := false
	for _, s := range servers {
		if s.ClientIdentifier == server.ServerClientID {
			if err := db.UpdateServerURL(h.DB, id, s.BestURL()); err != nil {
				slog.Error("failed to update server connection", "error", err, "serverId", id)
				h.Notifications.Add(user.ID, notifications.TypeAction, "Refresh server connection", err.Error(), notifications.StatusError, nil)
				http.Error(w, "Failed to update server connection", http.StatusInternalServerError)
				return
			}
			found = true
			break
		}
	}
	if !found {
		h.Notifications.Add(user.ID, notifications.TypeAction, "Refresh server connection", server.ServerName+" no longer appears in your Plex account", notifications.StatusError, nil)
		http.Error(w, fmt.Sprintf("Server %q no longer appears in your Plex account", server.ServerName), http.StatusNotFound)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Refresh server connection", server.ServerName, notifications.StatusSuccess, nil)
	h.renderServerList(w, r)
}

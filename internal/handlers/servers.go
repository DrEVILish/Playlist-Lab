package handlers

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

type ServersHandler struct {
	DB   *sql.DB
	Plex *auth.PlexClient
	Tmpl *Templates
}

func RegisterServers(r chi.Router, mw *auth.Middleware, h *ServersHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/setup", h.setupPage)
		r.Get("/setup/libraries", h.setupLibraries)
		r.Post("/servers/select", h.selectServer)
	})
}

// serverOption is the compound "name|clientId|url|accessToken" value packed
// into the <select> so the chosen server's connection details round-trip
// through the form without server-side session state.
type serverOption struct {
	Name, ClientID, URL, AccessToken string
}

func encodeServerOption(s auth.PlexServer) string {
	return strings.Join([]string{s.Name, s.ClientIdentifier, s.BestURL(), s.AccessToken}, "|")
}

func decodeServerOption(v string) (serverOption, bool) {
	parts := strings.SplitN(v, "|", 4)
	if len(parts) != 4 {
		return serverOption{}, false
	}
	return serverOption{Name: parts[0], ClientID: parts[1], URL: parts[2], AccessToken: parts[3]}, true
}

func (h *ServersHandler) setupPage(w http.ResponseWriter, r *http.Request) {
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
	h.Tmpl.RenderPage(w, r, "setup", map[string]any{"Servers": options})
}

func (h *ServersHandler) setupLibraries(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	opt, ok := decodeServerOption(r.URL.Query().Get("server"))
	if !ok {
		w.Write(nil)
		return
	}
	client := plex.NewClient(opt.URL, plex.ResolveToken(user.PlexToken, opt.AccessToken), h.Plex.ClientID, "Playlist Lab")
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

func (h *ServersHandler) selectServer(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	opt, ok := decodeServerOption(r.FormValue("server"))
	if !ok {
		http.Error(w, "server is required", http.StatusBadRequest)
		return
	}
	libID, libName, _ := strings.Cut(r.FormValue("library"), "|")

	if _, err := db.SaveUserServer(h.DB, user.ID, opt.Name, opt.ClientID, opt.URL, libID, libName, opt.AccessToken); err != nil {
		slog.Error("failed to save server selection", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

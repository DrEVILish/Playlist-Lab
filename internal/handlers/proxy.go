// Image proxying, ported from routes/proxy.ts's GET /api/proxy/image.
//
// The React app needed this because a browser can't attach an X-Plex-Token
// header to an <img> request. Server-rendered templates could technically
// sidestep it by baking the token into the URL, and this port originally did
// exactly that - which put the user's Plex token in the page hundreds of
// times per render (once per playlist thumb), and only worked for browsers
// with their own network route to the Plex server. Remote users saw broken
// cover art, and an HTTPS deployment pointing at an http:// Plex server saw
// none at all, since browsers block that as mixed content. Relaying the
// bytes fixes all three.
//
// One deliberate difference from the Node original: that route accepted an
// arbitrary ?url= and appended the caller's Plex token to whatever host it
// named, so any logged-in user could have harvested their own token - or
// pointed the server at an internal address and used it as an SSRF probe.
// This takes only a server-relative path and pairs it with the server the
// user already has selected, so there is no attacker-controlled host to
// validate in the first place.
package handlers

import (
	"database/sql"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/plex"
)

type ProxyHandler struct {
	DB       *sql.DB
	PlexAuth *auth.PlexClient
}

func RegisterProxy(r chi.Router, mw *auth.Middleware, h *ProxyHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/proxy/image", h.image)
	})
}

// transparentPixel is served whenever the real image can't be had. Plex
// returns 404 for a playlist whose composite thumb it hasn't generated yet,
// which is routine rather than exceptional, and a broken-image icon in every
// such row would read as a bug in this app.
var transparentPixel, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

func (h *ProxyHandler) servePixel(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(transparentPixel)
}

// ImageProxyURL renders the URL a template should point an <img> at for a
// Plex-relative media path. Empty in, empty out, so callers can keep using
// `{{if .CoverURL}}` to decide whether to emit the tag at all.
func ImageProxyURL(path string) string {
	if path == "" {
		return ""
	}
	return "/proxy/image?path=" + url.QueryEscape(path)
}

func (h *ProxyHandler) image(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	path := r.URL.Query().Get("path")

	// Only a path on the user's own Plex server. "//host" would otherwise be
	// read as scheme-relative by the URL parser and point somewhere else
	// entirely, so it is rejected alongside anything not rooted at "/".
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		http.Error(w, "path must be a server-relative Plex media path", http.StatusBadRequest)
		return
	}

	dbUser, err := db.GetUserByID(h.DB, user.ID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	userServer, err := db.GetUserServer(h.DB, user.ID)
	if err != nil || userServer == nil {
		h.servePixel(w)
		return
	}

	client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(dbUser.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")
	body, contentType, err := client.FetchMedia(path)
	if err != nil {
		// Debug, not error: one of these fires per thumb on a page render, and
		// at a louder level a library with missing composites would bury real
		// errors in the log - the same reasoning proxy.ts records for its own
		// per-request logging.
		slog.Debug("image proxy: falling back to placeholder", "error", err, "userId", user.ID, "path", path)
		h.servePixel(w)
		return
	}
	defer body.Close()

	if contentType == "" {
		contentType = "image/jpeg"
	}
	w.Header().Set("Content-Type", contentType)
	// Matches proxy.ts: thumbs are effectively immutable for a given path,
	// and without this every navigation refetches every cover from Plex.
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if _, err := io.Copy(w, body); err != nil {
		slog.Debug("image proxy: copy failed", "error", err, "userId", user.ID)
	}
}

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
	w.Write(transparentPixel)
}

// ImageProxyURL renders the URL a template should point an <img> at for a
// Plex-relative media path. Empty in, empty out, so callers can keep using
// `{{if .CoverURL}}` to decide whether to emit the tag at all.
func ImageProxyURL(path string) string {
	if !plex.IsImagePath(path) {
		return ""
	}
	return "/proxy/image?path=" + url.QueryEscape(path)
}

func (h *ProxyHandler) image(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	path := r.URL.Query().Get("path")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")

	if !plex.IsImagePath(path) {
		http.Error(w, "path must identify Plex artwork", http.StatusBadRequest)
		return
	}

	userServer, err := db.GetUserMusicServer(h.DB, user.ID)
	if err != nil || userServer == nil {
		h.servePixel(w)
		return
	}

	client := plex.NewClient(userServer.ServerURL, plex.ResolveToken(user.PlexToken, userServer.AccessToken.String), h.PlexAuth.ClientID, "Playlist Lab")
	body, _, err := client.FetchMedia(path)
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

	var prefix [512]byte
	n, err := io.ReadFull(body, prefix[:])
	contentType := plex.RasterContentType(prefix[:n])
	if contentType == "" || (err != nil && err != io.ErrUnexpectedEOF) {
		h.servePixel(w)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if _, err := w.Write(prefix[:n]); err != nil {
		return
	}
	if _, err := io.Copy(w, body); err != nil {
		slog.Debug("image proxy: copy failed", "error", err, "userId", user.ID)
	}
}

// Package handlers: sessions.go is Settings > Sessions (DESIGN.md §11.4) -
// lists a user's active sessions/devices with the ability to revoke any one
// of them, the one Settings sub-feature DESIGN.md explicitly called out as
// needing real auth-layer work rather than a template/CSS change.
package handlers

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/session"
)

// sessionRow is what partials/settings_sessions.html actually renders - the
// device/browser guess is done here rather than in the template, since Go's
// html/template has no string-matching control flow worth the noise for it.
type sessionRow struct {
	DisplayID  string
	Device     string
	IP         string
	CreatedAt  int64
	LastSeenAt int64
	IsCurrent  bool
}

// guessDevice turns a raw User-Agent into a short "Browser on OS" label -
// deliberately not a real user-agent-parsing library: this only has to be
// good enough to tell a user which of their own devices a row is, not
// accurate for analytics. Order matters (Edge/Chrome both contain neither
// Firefox nor generic browser strings other engines also match).
func guessDevice(ua string) string {
	if ua == "" {
		return "Unknown device"
	}
	os := "Unknown OS"
	switch {
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Mac OS"):
		os = "macOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	browser := "Unknown browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	return browser + " on " + os
}

// sessionRows is shared by page() (settings.go, the initial full-page
// render) and sessionsSection below (the post-revoke partial refresh) so
// there's one place that builds this list, not two.
func (h *SettingsHandler) sessionRows(r *http.Request, userID int64) ([]sessionRow, error) {
	currentID := ""
	if sid := auth.SessionID(r); sid != "" {
		currentID = session.DisplayIDFor(sid)
	}
	sessions, err := h.Store.ListForUser(userID)
	if err != nil {
		return nil, err
	}
	rows := make([]sessionRow, len(sessions))
	for i, s := range sessions {
		rows[i] = sessionRow{
			DisplayID: s.DisplayID, Device: guessDevice(s.Data.UserAgent), IP: s.Data.IP,
			CreatedAt: s.Data.CreatedAt, LastSeenAt: s.Data.LastSeenAt, IsCurrent: s.DisplayID == currentID,
		}
	}
	return rows, nil
}

// sessionsSection renders just the Sessions list partial, for the
// post-revoke refresh (hx-target scoped to this section only).
func (h *SettingsHandler) sessionsSection(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	rows, err := h.sessionRows(r, user.ID)
	if err != nil {
		h.Tmpl.RenderPartial(w, "partials/settings_sessions.html", map[string]any{"Error": "Failed to load sessions."})
		return
	}
	h.Tmpl.RenderPartial(w, "partials/settings_sessions.html", map[string]any{"Sessions": rows})
}

func (h *SettingsHandler) revokeSession(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id := chi.URLParam(r, "id")
	if err := h.Store.Revoke(user.ID, id); err != nil && err != session.ErrNotFound {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.sessionsSection(w, r)
}

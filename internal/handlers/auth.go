package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/session"
)

type AuthHandler struct {
	DB     *sql.DB
	Plex   *auth.PlexClient
	Auth   *auth.Middleware
	Store  *session.Store
	Secure bool
	Tmpl   *Templates
}

func RegisterAuth(r chi.Router, h *AuthHandler) {
	r.Get("/login", h.loginPage)
	r.Post("/auth/start", h.start)
	r.Post("/auth/poll", h.poll)
	r.Post("/auth/logout", h.logout)
	r.Get("/auth/callback", h.callback)
}

// callback is where Plex redirects the popup/tab after the user approves
// sign-in (the AuthURL forwardUrl built in start/poll below) - v2 served
// this as a static apps/web/public/plex-callback.html; this route never
// existed in the Go port, so it 404'd for every real login. The actual
// login completion happens via /auth/poll on the original tab, not here -
// this page only has to tell the user that and get out of the way.
func (h *AuthHandler) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8">
<title>Signed in - Playlist Lab</title></head>
<body style="font-family:system-ui,sans-serif;background:#0a1018;color:#e8ecf1;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0">
<div style="text-align:center;max-width:22rem">
<p style="font-size:2rem">&#10003;</p>
<h1 style="font-size:1.25rem">Signed in with Plex</h1>
<p>You can close this tab and return to Playlist Lab - it will finish logging you in automatically.</p>
</div>
<script>setTimeout(function(){ window.close(); }, 1500);</script>
</body></html>`)
}

func (h *AuthHandler) loginPage(w http.ResponseWriter, r *http.Request) {
	if u := auth.CurrentUser(r); u != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.Tmpl.RenderPage(w, r, "login", nil)
}

// start begins the Plex PIN flow and returns an HTMX fragment: a link to
// open Plex sign-in in a new tab, plus a polling element that calls /poll
// every 2s until authenticated (mirrors the SPA's setInterval poll loop in
// LoginPage.tsx, but driven declaratively by hx-trigger).
func (h *AuthHandler) start(w http.ResponseWriter, r *http.Request) {
	pin, err := h.Plex.StartAuth()
	if err != nil {
		slog.Error("failed to start Plex auth", "error", err)
		http.Error(w, "Failed to start authentication", http.StatusInternalServerError)
		return
	}
	authURL := h.Plex.AuthURL(pin.Code, baseURLOf(r)+"/auth/callback")
	h.Tmpl.RenderPartial(w, "partials/auth_pending.html", map[string]any{
		"AuthURL":   authURL,
		"PinID":     pin.ID,
		"Code":      pin.Code,
		"OpenPopup": true,
	})
}

func (h *AuthHandler) poll(w http.ResponseWriter, r *http.Request) {
	pinID, _ := strconv.Atoi(r.FormValue("pinId"))
	code := r.FormValue("code")
	if pinID == 0 || code == "" {
		http.Error(w, "pinId and code are required", http.StatusBadRequest)
		return
	}

	pin, err := h.Plex.PollAuth(pinID, code)
	if err != nil {
		slog.Error("failed to poll Plex auth", "error", err)
		h.Tmpl.RenderPartial(w, "partials/auth_pending.html", map[string]any{
			"AuthURL": h.Plex.AuthURL(code, baseURLOf(r)+"/auth/callback"),
			"PinID":   pinID, "Code": code, "Error": "Failed to check authentication status.",
		})
		return
	}
	if pin == nil {
		h.Tmpl.RenderPartial(w, "partials/auth_expired.html", nil)
		return
	}
	if pin.AuthToken == "" {
		// Not yet authorized - keep polling.
		h.Tmpl.RenderPartial(w, "partials/auth_pending.html", map[string]any{
			"AuthURL": h.Plex.AuthURL(code, baseURLOf(r)+"/auth/callback"),
			"PinID":   pinID, "Code": code,
		})
		return
	}

	userInfo, err := h.Plex.GetUserInfo(pin.AuthToken)
	if err != nil {
		slog.Error("failed to get Plex user info", "error", err)
		h.Tmpl.RenderPartial(w, "partials/auth_error.html", map[string]any{"Message": "Failed to complete authentication."})
		return
	}

	user, enabled, err := h.handleLogin(userInfo, pin.AuthToken)
	if err != nil {
		slog.Error("login failed", "error", err)
		h.Tmpl.RenderPartial(w, "partials/auth_error.html", map[string]any{"Message": "Failed to complete authentication."})
		return
	}
	if !enabled {
		h.Tmpl.RenderPartial(w, "partials/auth_error.html", map[string]any{
			"Message": "Your account has not been approved. Please contact the server admin.",
		})
		return
	}

	sid, err := session.NewSessionID()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.Store.Save(sid, &session.Data{UserID: user.ID, PlexUserID: user.PlexUserID}); err != nil {
		slog.Error("failed to save session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	session.SetCookie(w, sid, h.Secure)

	// HTMX redirect: the client-side lib reads this response header and
	// navigates the whole page, replacing the SPA's window.location.href.
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (h *AuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	sid := auth.SessionID(r)
	if sid != "" {
		_ = h.Store.Destroy(sid)
	}
	session.ClearCookie(w, h.Secure)
	w.Header().Set("HX-Redirect", "/login")
	w.WriteHeader(http.StatusOK)
}

// handleLogin ports routes/auth.ts's handleUserLogin: create/update the
// user, auto-admin the first ever user, and re-verify Plex Home/friend
// membership for every non-admin login.
func (h *AuthHandler) handleLogin(userInfo *auth.PlexUser, token string) (*db.User, bool, error) {
	plexUserID := strconv.Itoa(userInfo.ID)
	username := userInfo.DisplayName()

	user, err := db.GetUserByPlexID(h.DB, plexUserID)
	isNew := errors.Is(err, db.ErrUserNotFound)
	if isNew {
		user, err = db.CreateUser(h.DB, plexUserID, username, token, userInfo.Thumb)
		if err != nil {
			return nil, false, err
		}
		slog.Info("new user created", "plexUserId", plexUserID, "username", username)
	} else if err != nil {
		return nil, false, err
	} else {
		_ = db.UpdateUserLogin(h.DB, user.ID)
		_ = db.UpdateUserToken(h.DB, user.ID, token)
		_ = db.UpdateUserProfile(h.DB, user.ID, username, userInfo.Thumb)
		slog.Info("user logged in", "plexUserId", plexUserID, "username", username)
	}

	if count, _ := db.GetUserCount(h.DB); count == 1 {
		_ = db.AddAdmin(h.DB, user.ID)
		slog.Info("first user auto-promoted to admin", "userId", user.ID)
	}

	isAdmin, _ := db.IsAdmin(h.DB, user.ID)
	if !isAdmin {
		if admin, err := db.GetFirstUser(h.DB); err == nil {
			h.reverifyMembership(admin, user, plexUserID, isNew)
		}
	}

	user, err = db.GetUserByID(h.DB, user.ID)
	if err != nil {
		return nil, false, err
	}
	isAdmin, _ = db.IsAdmin(h.DB, user.ID)
	return user, user.IsEnabled || isAdmin, nil
}

// reverifyMembership checks whether a non-admin user is still a member of
// the admin's Plex Home or friends list, disabling/enabling access to
// match. Errors are swallowed (matching the Node server): a failed lookup
// shouldn't crash login, it should just leave existing access untouched
// (except a brand-new account, which can't be verified yet and is left
// disabled for the admin to approve).
func (h *AuthHandler) reverifyMembership(admin, user *db.User, plexUserID string, isNew bool) {
	homeUsers, err := h.Plex.GetHomeUsers(admin.PlexToken)
	if err != nil {
		slog.Error("failed to verify Plex Home membership", "error", err, "userId", user.ID)
		if isNew {
			_ = db.DisableUser(h.DB, user.ID)
		}
		return
	}
	friends, err := h.Plex.GetFriends(admin.PlexToken)
	if err != nil {
		slog.Warn("failed to fetch Plex friends, falling back to Plex Home only", "error", err)
		friends = nil
	}

	approved := map[string]bool{}
	for _, id := range homeUsers {
		approved[strconv.Itoa(id)] = true
	}
	for _, id := range friends {
		approved[strconv.Itoa(id)] = true
	}

	if approved[plexUserID] {
		_ = db.EnableUser(h.DB, user.ID)
		if existing, _ := db.GetUserServer(h.DB, user.ID); existing == nil {
			_ = db.CopyServerConfig(h.DB, admin.ID, user.ID)
		}
	} else if user.IsEnabled {
		_ = db.DisableUser(h.DB, user.ID)
		slog.Info("user disabled: not in Plex Home and not a friend", "userId", user.ID, "isNewUser", isNew)
	}
}

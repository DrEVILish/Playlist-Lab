package auth

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/session"
)

type contextKey int

const (
	userContextKey contextKey = iota
	sessionIDContextKey
)

// CurrentUser returns the authenticated user attached by RequireAuth or
// OptionalAuth, or nil if the request is unauthenticated.
func CurrentUser(r *http.Request) *db.User {
	u, _ := r.Context().Value(userContextKey).(*db.User)
	return u
}

// SessionID returns the request's session cookie value, set by
// WithSession. Used by handlers that mutate the session (login, logout).
func SessionID(r *http.Request) string {
	sid, _ := r.Context().Value(sessionIDContextKey).(string)
	return sid
}

// Middleware bundles the dependencies auth checks need, replacing the
// per-request req.dbService/req.session Express relied on.
type Middleware struct {
	DB      *sql.DB
	Store   *session.Store
	Secure  bool
	DevMode bool // NODE_ENV != production
	DevAuto bool // DEV_NO_AUTH=true
}

// WithSession reads the session cookie (creating a fresh, unsaved id if
// absent) and stashes both the session id and, if present, its data's
// derived user on the request context. Must run before RequireAuth/
// RequireAdmin/OptionalAuth.
func (m *Middleware) WithSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := ""
		if c, err := r.Cookie(session.CookieName); err == nil {
			sid = c.Value
		}
		ctx := context.WithValue(r.Context(), sessionIDContextKey, sid)
		r = r.WithContext(ctx)
		next.ServeHTTP(w, r)
	})
}

func (m *Middleware) currentSessionData(r *http.Request) *session.Data {
	sid := SessionID(r)
	if sid == "" {
		return nil
	}
	data, err := m.Store.Get(sid)
	if err != nil {
		return nil
	}
	return data
}

// RequireAuth rejects unauthenticated requests, mirroring
// middleware/auth.ts's requireAuth (including its dev-only auto-login
// bypass gated on DEV_NO_AUTH + non-production).
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := m.currentSessionData(r)

		if (data == nil || data.UserID == 0) && m.DevAuto && m.DevMode {
			if first, err := db.GetFirstUser(m.DB); err == nil {
				data = &session.Data{UserID: first.ID, PlexUserID: first.PlexUserID}
			}
		}

		if data == nil || data.UserID == 0 {
			slog.Debug("rejected unauthenticated request", "method", r.Method, "path", r.URL.Path, "hadCookie", SessionID(r) != "")
			writeJSONError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "Authentication required")
			return
		}

		user, err := db.GetUserByID(m.DB, data.UserID)
		if err != nil {
			slog.Warn("session references a user that no longer exists, destroying it", "userId", data.UserID)
			_ = m.Store.Destroy(SessionID(r))
			writeJSONError(w, http.StatusUnauthorized, "AUTH_INVALID", "Invalid session")
			return
		}

		isAdmin, _ := db.IsAdmin(m.DB, user.ID)
		if !isAdmin && !user.IsEnabled {
			slog.Warn("rejected disabled user", "userId", user.ID)
			writeJSONError(w, http.StatusForbidden, "USER_DISABLED", "Your account has been disabled. Contact the server admin.")
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAdmin must run after RequireAuth.
func (m *Middleware) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := CurrentUser(r)
		if user == nil {
			writeJSONError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "Authentication required")
			return
		}
		isAdmin, err := db.IsAdmin(m.DB, user.ID)
		if err != nil || !isAdmin {
			writeJSONError(w, http.StatusForbidden, "ADMIN_REQUIRED", "Admin privileges required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// OptionalAuth attaches a user if a valid session exists, without
// rejecting the request otherwise.
func (m *Middleware) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := m.currentSessionData(r)
		if data == nil || data.UserID == 0 {
			next.ServeHTTP(w, r)
			return
		}
		if user, err := db.GetUserByID(m.DB, data.UserID); err == nil {
			ctx := context.WithValue(r.Context(), userContextKey, user)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"` + message + `","statusCode":` + strconv.Itoa(status) + `}}`))
}

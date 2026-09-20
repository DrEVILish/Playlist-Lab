package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/session"
)

func newTestMiddleware(t *testing.T) (*Middleware, *db.User) {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	user, err := db.CreateUser(sqlDB, "plex-1", "alice", "tok", "")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	store := session.NewStore(sqlDB)
	return &Middleware{DB: sqlDB, Store: store}, user
}

func withSessionCookie(t *testing.T, m *Middleware, req *http.Request, data *session.Data) *http.Request {
	t.Helper()
	sid, err := session.NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	if err := m.Store.Save(sid, data); err != nil {
		t.Fatalf("Save: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: sid})
	return req
}

func handlerChain(m *Middleware) http.Handler {
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return m.WithSession(m.RequireAuth(final))
}

// A plain top-level browser GET (no HX-Request header - htmx sets this on
// every request it makes, so its absence means a real navigation rather
// than an in-app call) must redirect to /login rather than render a raw
// JSON error body: there is no SPA shell here to fall back on, unlike the
// React app this replaces, where the catch-all route always served the same
// shell and let client-side routing decide what to show.
func TestRequireAuth_NoSessionRejected(t *testing.T) {
	m, _ := newTestMiddleware(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handlerChain(m).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("got status %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/login" {
		t.Fatalf("Location = %q, want /login", got)
	}
}

// An htmx-driven request (one already inside the loaded app, e.g. a button
// click after the session expired) must keep getting the JSON error body -
// it expects something it can react to, not a redirect it would follow into
// a login page rendered inside whatever partial it was targeting.
func TestRequireAuth_NoSession_HTMXRequestGetsJSON(t *testing.T) {
	m, _ := newTestMiddleware(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	handlerChain(m).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

// A non-GET request (POST/DELETE/etc, always AJAX in this app - there are
// no plain <form> submits) also keeps the JSON body regardless of
// HX-Request: a browser never top-level-navigates via POST here.
func TestRequireAuth_NoSession_NonGETGetsJSON(t *testing.T) {
	m, _ := newTestMiddleware(t)
	req := httptest.NewRequest(http.MethodPost, "/playlists/bulk-delete", nil)
	rec := httptest.NewRecorder()
	handlerChain(m).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", rec.Code)
	}
}

func TestRequireAuth_ValidSessionPasses(t *testing.T) {
	m, user := newTestMiddleware(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withSessionCookie(t, m, req, &session.Data{UserID: user.ID, PlexUserID: user.PlexUserID})
	rec := httptest.NewRecorder()
	handlerChain(m).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
}

// A session pointing at a user row that no longer exists must be rejected
// AND destroyed - stale sessions shouldn't linger forever if the user was
// deleted out from under them.
func TestRequireAuth_SessionForDeletedUserIsDestroyed(t *testing.T) {
	m, user := newTestMiddleware(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withSessionCookie(t, m, req, &session.Data{UserID: user.ID + 999})
	sid := req.Cookies()[0].Value

	rec := httptest.NewRecorder()
	handlerChain(m).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("got status %d, want 302", rec.Code)
	}
	if _, err := m.Store.Get(sid); err != session.ErrNotFound {
		t.Fatalf("expected session to be destroyed, got err=%v", err)
	}
}

// A disabled, non-admin user must be rejected with 403 even though their
// session is otherwise valid.
func TestRequireAuth_DisabledUserForbidden(t *testing.T) {
	m, user := newTestMiddleware(t)
	if err := db.DisableUser(m.DB, user.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withSessionCookie(t, m, req, &session.Data{UserID: user.ID})
	rec := httptest.NewRecorder()
	handlerChain(m).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want 403", rec.Code)
	}
}

// A disabled admin is exempt from the disabled check - admins must never
// be able to lock themselves out.
func TestRequireAuth_DisabledAdminStillAllowed(t *testing.T) {
	m, user := newTestMiddleware(t)
	if err := db.DisableUser(m.DB, user.ID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	if err := db.AddAdmin(m.DB, user.ID); err != nil {
		t.Fatalf("AddAdmin: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withSessionCookie(t, m, req, &session.Data{UserID: user.ID})
	rec := httptest.NewRecorder()
	handlerChain(m).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
}

// The dev auto-login bypass must only fire when both DEV_NO_AUTH and
// non-production dev mode are set - never in production, even accidentally.
func TestRequireAuth_DevAutoLoginRequiresBothFlags(t *testing.T) {
	m, _ := newTestMiddleware(t)

	// HX-Request keeps these focused on what they actually test - whether
	// the bypass fired - via the plain JSON-401 branch, rather than also
	// asserting the browser-navigation redirect covered by
	// TestRequireAuth_NoSessionRejected above.
	t.Run("neither flag set: rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		handlerChain(m).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want 401", rec.Code)
		}
	})

	t.Run("DevAuto without DevMode: rejected", func(t *testing.T) {
		m.DevAuto = true
		m.DevMode = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		handlerChain(m).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want 401", rec.Code)
		}
	})

	t.Run("both flags set: auto-logs in as the first user", func(t *testing.T) {
		m.DevAuto = true
		m.DevMode = true
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handlerChain(m).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want 200", rec.Code)
		}
	})
}

func TestRequireAdmin_NonAdminForbidden(t *testing.T) {
	m, user := newTestMiddleware(t)
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	chain := m.WithSession(m.RequireAuth(m.RequireAdmin(final)))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withSessionCookie(t, m, req, &session.Data{UserID: user.ID})
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got status %d, want 403", rec.Code)
	}
}

func TestRequireAdmin_AdminAllowed(t *testing.T) {
	m, user := newTestMiddleware(t)
	if err := db.AddAdmin(m.DB, user.ID); err != nil {
		t.Fatalf("AddAdmin: %v", err)
	}
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	chain := m.WithSession(m.RequireAuth(m.RequireAdmin(final)))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withSessionCookie(t, m, req, &session.Data{UserID: user.ID})
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
}

func TestOptionalAuth_NoSessionStillPasses(t *testing.T) {
	m, _ := newTestMiddleware(t)
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if CurrentUser(r) != nil {
			t.Fatal("expected no user attached for an unauthenticated request")
		}
		w.WriteHeader(http.StatusOK)
	})
	chain := m.WithSession(m.OptionalAuth(final))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
}

func TestOptionalAuth_ValidSessionAttachesUser(t *testing.T) {
	m, user := newTestMiddleware(t)
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := CurrentUser(r)
		if got == nil || got.ID != user.ID {
			t.Fatalf("expected user %d attached, got %+v", user.ID, got)
		}
		w.WriteHeader(http.StatusOK)
	})
	chain := m.WithSession(m.OptionalAuth(final))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withSessionCookie(t, m, req, &session.Data{UserID: user.ID})
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
}

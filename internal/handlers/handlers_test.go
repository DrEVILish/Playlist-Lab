package handlers

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/session"
	"github.com/drevilish/playlist-lab/templates"
)

// nopTemplates loads the real embedded templates so handlers under test can
// call RenderPage/RenderPartial exactly as they do in production (rather
// than a stub that would silently skip real template-parse errors).
func nopTemplates() *Templates {
	tmpl, err := LoadTemplates(templates.FS)
	if err != nil {
		panic("LoadTemplates: " + err.Error())
	}
	return tmpl
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

// newTestDB opens a fresh on-disk sqlite database (schema applied by
// db.Open) for one test, closing it on cleanup. A real file rather than
// ":memory:" is used because db.Open's SetMaxOpenConns(1) plus WAL mode is
// what the handlers under test are actually built against.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return sqlDB
}

// newTestUser creates a user row with a unique plex_user_id derived from
// the subtest name, so subtests sharing one DB don't collide.
func newTestUser(t *testing.T, sqlDB *sql.DB) *db.User {
	t.Helper()
	plexID := "plex-" + strings.ReplaceAll(t.Name(), "/", "-")
	u, err := db.CreateUser(sqlDB, plexID, "user-"+plexID, "token-"+plexID, "")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

// testRouter wires one handler method onto a chi route behind the real
// auth.Middleware chain (WithSession + RequireAuth), matching how
// RegisterX(...) wires it up in production - so chi.URLParam and
// auth.CurrentUser both behave exactly as they do at runtime instead of a
// hand-stubbed context.
func testRouter(sqlDB *sql.DB, method, pattern string, handler http.HandlerFunc) chi.Router {
	store := session.NewStore(sqlDB)
	mw := &auth.Middleware{DB: sqlDB, Store: store}
	r := chi.NewRouter()
	r.Use(mw.WithSession)
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.MethodFunc(method, pattern, handler)
	})
	return r
}

// authedRequest issues method/target as user against router (built by
// testRouter), attaching a genuine session cookie, and returns the response.
func authedRequest(t *testing.T, sqlDB *sql.DB, router chi.Router, user *db.User, method, target, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, bodyReader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	store := session.NewStore(sqlDB)
	sid, err := session.NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	if err := store.Save(sid, &session.Data{UserID: user.ID, PlexUserID: user.PlexUserID}); err != nil {
		t.Fatalf("session Save: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: sid})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

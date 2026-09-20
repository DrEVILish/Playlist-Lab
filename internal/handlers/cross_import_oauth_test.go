package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/adapters"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/session"
)

// fakeOAuthTarget is a minimal adapters.TargetAdapter + adapters.OAuthCapable
// double, just enough for oauthStart/oauthCallback's CSRF-state check - the
// only thing under test here.
type fakeOAuthTarget struct {
	handledCode   string
	handledUserID int64
}

func (f *fakeOAuthTarget) Meta() adapters.ServiceMeta {
	return adapters.ServiceMeta{ID: "fake", Name: "Fake"}
}
func (f *fakeOAuthTarget) SearchCatalog(ctx context.Context, query string, userID int64, allowLive, allowStatic bool) ([]adapters.MatchResult, error) {
	return nil, nil
}
func (f *fakeOAuthTarget) MatchTracks(ctx context.Context, tracks []adapters.TrackInfo, cfg adapters.TargetConfig, userID int64, progress func(current, total int), isCancelled func() bool) ([]adapters.MatchResult, error) {
	return nil, nil
}
func (f *fakeOAuthTarget) CreatePlaylist(ctx context.Context, name string, matches []adapters.MatchResult, cfg adapters.TargetConfig, userID int64) (string, string, int, error) {
	return "", "", 0, nil
}
func (f *fakeOAuthTarget) IsConfigured() bool { return true }
func (f *fakeOAuthTarget) GetOAuthURL(ctx context.Context, userID int64, redirectURI string) (string, error) {
	// Deliberately embeds a predictable, attacker-guessable state (the
	// userID) - exactly the real spotify/youtube adapters' behavior this
	// test is guarding against being trusted on its own.
	return "https://example.com/authorize?state=" + itoa(userID), nil
}
func (f *fakeOAuthTarget) HandleOAuthCallback(ctx context.Context, code string, userID int64, redirectURI string) error {
	f.handledCode, f.handledUserID = code, userID
	return nil
}
func (f *fakeOAuthTarget) HasValidConnection(ctx context.Context, userID int64) (bool, error) {
	return true, nil
}
func (f *fakeOAuthTarget) RevokeConnection(ctx context.Context, userID int64) error { return nil }

// sessionCookie establishes a real session row (same as authedRequest) and
// returns its cookie directly, so a test can reuse one session across
// several requests - authedRequest itself mints a fresh session every call,
// which would defeat the oauthStart-then-oauthCallback flow's whole point.
func sessionCookie(t *testing.T, sqlDB *sql.DB, store *session.Store, user *db.User) *http.Cookie {
	t.Helper()
	sid, err := session.NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	if err := store.Save(sid, &session.Data{UserID: user.ID, PlexUserID: user.PlexUserID}); err != nil {
		t.Fatalf("session Save: %v", err)
	}
	return &http.Cookie{Name: session.CookieName, Value: sid}
}

// oauthTestRouter mounts both oauthStart and oauthCallback (unlike
// testRouter, which only wires one route), matching how RegisterCrossImport
// wires them together in production - the CSRF-state check spans both.
func oauthTestRouter(sqlDB *sql.DB, h *CrossImportHandler) chi.Router {
	mw := &auth.Middleware{DB: sqlDB, Store: h.Store}
	r := chi.NewRouter()
	r.Use(mw.WithSession)
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/cross-import/oauth/{service}", h.oauthStart)
		r.Get("/cross-import/oauth/{service}/callback", h.oauthCallback)
	})
	return r
}

func TestOAuthCallback_RejectsMissingState(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	registry := adapters.NewRegistry()
	registry.RegisterTarget(&fakeOAuthTarget{})
	h := &CrossImportHandler{DB: sqlDB, Registry: registry, Store: session.NewStore(sqlDB)}
	router := oauthTestRouter(sqlDB, h)

	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/cross-import/oauth/fake/callback?code=attacker-code", "", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when the callback carries no state at all", rec.Code)
	}
}

func TestOAuthCallback_RejectsMismatchedState(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	registry := adapters.NewRegistry()
	target := &fakeOAuthTarget{}
	registry.RegisterTarget(target)
	store := session.NewStore(sqlDB)
	h := &CrossImportHandler{DB: sqlDB, Registry: registry, Store: store}
	router := oauthTestRouter(sqlDB, h)
	cookie := sessionCookie(t, sqlDB, store, user)

	// A real oauthStart call records its own nonce in the session.
	startReq := httptest.NewRequest(http.MethodGet, "/cross-import/oauth/fake", nil)
	startReq.AddCookie(cookie)
	startRec := httptest.NewRecorder()
	router.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusFound {
		t.Fatalf("oauthStart status = %d, want 302", startRec.Code)
	}

	// An attacker's own grant, replayed against the victim's session with a
	// state the session never actually issued.
	req := httptest.NewRequest(http.MethodGet, "/cross-import/oauth/fake/callback?code=attacker-code&state=attacker-supplied", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a state that doesn't match what oauthStart issued", rec.Code)
	}
	if target.handledCode != "" {
		t.Error("HandleOAuthCallback must not run when the state check fails")
	}
}

func TestOAuthStartThenCallback_AcceptsMatchingStateOnce(t *testing.T) {
	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	registry := adapters.NewRegistry()
	target := &fakeOAuthTarget{}
	registry.RegisterTarget(target)
	store := session.NewStore(sqlDB)
	h := &CrossImportHandler{DB: sqlDB, Registry: registry, Store: store}
	router := oauthTestRouter(sqlDB, h)
	cookie := sessionCookie(t, sqlDB, store, user)

	startReq := httptest.NewRequest(http.MethodGet, "/cross-import/oauth/fake", nil)
	startReq.AddCookie(cookie)
	startRec := httptest.NewRecorder()
	router.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusFound {
		t.Fatalf("oauthStart status = %d, want 302", startRec.Code)
	}
	loc, err := url.Parse(startRec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parsing Location: %v", err)
	}
	state := loc.Query().Get("state")
	if state == "" || state == itoa(user.ID) {
		t.Fatalf("state = %q, want a fresh nonce overwriting the adapter's own %q", state, itoa(user.ID))
	}

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/cross-import/oauth/fake/callback?code=real-code&state="+state, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	rec := do()
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 for the matching state; body=%s", rec.Code, rec.Body.String())
	}
	if target.handledCode != "real-code" || target.handledUserID != user.ID {
		t.Errorf("HandleOAuthCallback got (%q, %d), want (%q, %d)", target.handledCode, target.handledUserID, "real-code", user.ID)
	}

	// The nonce is single-use - replaying the same callback must fail now
	// that it's been cleared.
	replay := do()
	if replay.Code != http.StatusBadRequest {
		t.Errorf("replay status = %d, want 400 - the state nonce must not be reusable", replay.Code)
	}
}

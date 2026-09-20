// Covers Settings > Sessions (DESIGN.md §11.4) end to end through the real
// router/templates - GET /settings/sessions renders the current device,
// and revoking a *different* session removes it while the authedRequest
// helper's own session (used to issue every request in this test) survives.
package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/session"
)

func TestSettingsSessions_ListAndRevoke(t *testing.T) {
	sqlDB := newTestDB(t)
	store := session.NewStore(sqlDB)
	tmpl := nopTemplates()
	tmpl.DB = sqlDB
	h := &SettingsHandler{DB: sqlDB, Tmpl: tmpl, Store: store}
	router, user := newSettingsRouter(t, sqlDB, h)

	// A second, independent session for the same user - simulates another
	// signed-in device, distinct from the one authedRequest below uses.
	otherSID := "other-device-sid"
	if err := store.Save(otherSID, &session.Data{UserID: user.ID, UserAgent: "Mozilla/5.0 (Windows NT 10.0) Chrome/120.0"}); err != nil {
		t.Fatalf("seeding other session: %v", err)
	}

	getRec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/settings/sessions", "", "")
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /settings/sessions status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	body := getRec.Body.String()
	if !strings.Contains(body, "This device") {
		t.Fatalf("expected the requesting session to be labeled 'This device', body=%s", body)
	}
	if !strings.Contains(body, "Chrome on Windows") {
		t.Fatalf("expected the seeded session's UA to render as 'Chrome on Windows', body=%s", body)
	}

	otherDisplayID := session.DisplayIDFor(otherSID)
	postRec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/settings/sessions/"+otherDisplayID+"/revoke", "", "application/x-www-form-urlencoded")
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST revoke status = %d, want 200; body=%s", postRec.Code, postRec.Body.String())
	}
	if strings.Contains(postRec.Body.String(), "Chrome on Windows") {
		t.Fatalf("revoked session still present in re-rendered list, body=%s", postRec.Body.String())
	}
	if _, err := store.Get(otherSID); err == nil {
		t.Fatal("expected the revoked session to be gone from the store")
	}

	// authedRequest (handlers_test.go) mints a brand-new session per call, so
	// the GET and the revoke POST above each left their own session row
	// behind - only the seeded "other device" one should be gone, not those.
	sessions, err := store.ListForUser(user.ID)
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d remaining sessions, want 2 (the GET's and the revoke POST's own sessions, untouched)", len(sessions))
	}
}

func TestSettingsSessions_CannotRevokeAnotherUsersSession(t *testing.T) {
	sqlDB := newTestDB(t)
	store := session.NewStore(sqlDB)
	tmpl := nopTemplates()
	tmpl.DB = sqlDB
	h := &SettingsHandler{DB: sqlDB, Tmpl: tmpl, Store: store}
	router, attacker := newSettingsRouter(t, sqlDB, h)
	victim, err := db.CreateUser(sqlDB, "plex-victim", "victim", "tok-victim", "")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	victimSID := "victim-sid"
	if err := store.Save(victimSID, &session.Data{UserID: victim.ID}); err != nil {
		t.Fatalf("seeding victim session: %v", err)
	}
	victimDisplayID := session.DisplayIDFor(victimSID)

	// Authenticated as attacker, but targeting victim's session id.
	rec := authedRequest(t, sqlDB, router, attacker, http.MethodPost, "/settings/sessions/"+victimDisplayID+"/revoke", "", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a no-op revoke still re-renders the attacker's own list)", rec.Code)
	}
	if _, err := store.Get(victimSID); err != nil {
		t.Fatalf("victim's session was revoked by another user's request: %v", err)
	}
}

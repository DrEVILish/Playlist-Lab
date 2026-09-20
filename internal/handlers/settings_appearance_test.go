package handlers

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/session"
)

// newSettingsRouter mirrors newAdminRouter (admin_deemix_test.go) for
// SettingsHandler - also wires tmpl.DB, since pageData's TextScale lookup
// (templates.go) depends on it and nopTemplates() alone leaves it nil.
func newSettingsRouter(t *testing.T, sqlDB *sql.DB, h *SettingsHandler) (chi.Router, *db.User) {
	t.Helper()
	user := newTestUser(t, sqlDB)
	store := session.NewStore(sqlDB)
	mw := &auth.Middleware{DB: sqlDB, Store: store}
	r := chi.NewRouter()
	r.Use(mw.WithSession)
	RegisterSettings(r, mw, h)
	return r, user
}

// TestAppearanceTextScale_DefaultsRendersAndSaves is the "one runnable
// check" for DESIGN.md §14's server-side text-size preference: the page
// render must default the "medium" radio to checked with no saved row, the
// autosave POST must persist a new choice, and the base layout's own
// --text-scale inline style must reflect it on the very next render -
// exercising pageData's TextScale/User dual-injection (templates.go) end to
// end, not just the isolated db.go round-trip covered by
// internal/db/ui_preferences_test.go.
func TestAppearanceTextScale_DefaultsRendersAndSaves(t *testing.T) {
	sqlDB := newTestDB(t)
	tmpl := nopTemplates()
	tmpl.DB = sqlDB
	h := &SettingsHandler{DB: sqlDB, Tmpl: tmpl, Notifications: notifications.NewStore()}
	router, user := newSettingsRouter(t, sqlDB, h)

	getRec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/settings", "", "")
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /settings status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	body := getRec.Body.String()
	if !strings.Contains(body, `value="medium" checked`) {
		t.Fatalf("expected 'medium' checked by default with no saved preference, body=%s", body)
	}
	if !strings.Contains(body, "--text-scale: 1\"") {
		t.Fatalf("expected default --text-scale: 1 on <html>, body=%s", body)
	}

	postRec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/settings/appearance", "textScale=large", "application/x-www-form-urlencoded")
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST /settings/appearance status = %d, want 200; body=%s", postRec.Code, postRec.Body.String())
	}

	scale, err := db.GetTextScale(sqlDB, user.ID)
	if err != nil || scale != "large" {
		t.Fatalf("GetTextScale after save = (%q, %v), want (large, nil)", scale, err)
	}

	getRec2 := authedRequest(t, sqlDB, router, user, http.MethodGet, "/settings", "", "")
	body2 := getRec2.Body.String()
	if !strings.Contains(body2, `value="large" checked`) {
		t.Fatalf("expected 'large' checked after save, body=%s", body2)
	}
	if !strings.Contains(body2, "--text-scale: 1.15\"") {
		t.Fatalf("expected --text-scale: 1.15 on <html> after save, body=%s", body2)
	}
}

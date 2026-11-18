package handlers

import (
	"database/sql"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/session"
	"github.com/drevilish/playlist-lab/static"
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
// root font-size inline style must reflect it on the very next render -
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
	if !strings.Contains(body, "font-size: calc(1 * 100%)") {
		t.Fatalf("expected default root font-size calc(1 * 100%%) on <html>, body=%s", body)
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
	if !strings.Contains(body2, "font-size: calc(1.15 * 100%)") {
		t.Fatalf("expected root font-size calc(1.15 * 100%%) on <html> after save, body=%s", body2)
	}
}

// TestAppearanceVariantAndTint covers ftl-themes' palette variants and theme
// tint (CONTRACT.md): a variant renders as data-variant, a custom tint is
// stored per theme and rendered inline, picking a preset variant clears it,
// and invalid values are rejected.
func TestAppearanceVariantAndTint(t *testing.T) {
	sqlDB := newTestDB(t)
	tmpl := nopTemplates()
	tmpl.DB = sqlDB
	h := &SettingsHandler{DB: sqlDB, Tmpl: tmpl, Notifications: notifications.NewStore()}
	router, user := newSettingsRouter(t, sqlDB, h)
	post := func(form string) {
		t.Helper()
		if rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/settings/appearance", form, "application/x-www-form-urlencoded"); rec.Code != http.StatusOK {
			t.Fatalf("POST %q status = %d", form, rec.Code)
		}
	}
	page := func() string {
		return authedRequest(t, sqlDB, router, user, http.MethodGet, "/settings", "", "").Body.String()
	}

	post("theme=win7-aero")
	post("tint=%236e3ba1")
	if body := page(); !strings.Contains(body, `data-theme="win7-aero"`) || !strings.Contains(body, "--aero-tint: #6e3ba1") {
		t.Fatalf("custom tint not rendered inline on <html>")
	}

	post("tint=red%3B+background%3Aurl(x)") // not #rrggbb: ignored
	if tints, _ := db.GetThemeTints(sqlDB, user.ID); tints["win7-aero"] != "#6e3ba1" {
		t.Fatalf("invalid tint overwrote the saved one: %v", tints)
	}

	post("theme=win7-aero%2Fruby") // preset variant clears the custom colour
	body := page()
	if !strings.Contains(body, `data-variant="ruby"`) || strings.Contains(body, "--aero-tint:") {
		t.Fatalf("variant should render and clear the custom tint")
	}

	post("theme=lcars%2Fnot-a-variant") // unknown variant falls back to the base palette
	if v, _ := db.GetThemeVariant(sqlDB, user.ID); v != "" {
		t.Fatalf("unknown variant saved as %q", v)
	}
	if body := page(); strings.Contains(body[:strings.Index(body, "<head>")], "data-variant=") {
		t.Fatalf("no variant expected on <html> for lcars base palette")
	}
}

// A saved theme upstream has since removed must fall back to the default
// (blue-future), not link a stylesheet that no longer exists.
func TestRemovedThemeFallsBackToDefault(t *testing.T) {
	sqlDB := newTestDB(t)
	tmpl := nopTemplates()
	tmpl.DB = sqlDB
	h := &SettingsHandler{DB: sqlDB, Tmpl: tmpl, Notifications: notifications.NewStore()}
	router, user := newSettingsRouter(t, sqlDB, h)
	if err := db.SaveTheme(sqlDB, user.ID, "theme-that-was-removed"); err != nil {
		t.Fatal(err)
	}
	body := authedRequest(t, sqlDB, router, user, http.MethodGet, "/settings", "", "").Body.String()
	if !strings.Contains(body, `data-theme="`+defaultTheme+`"`) || strings.Contains(body, "theme-that-was-removed.css") {
		t.Fatalf("expected the %s fallback for an unknown saved theme", defaultTheme)
	}
}

func TestRemovedVariantKeepsThemeSelected(t *testing.T) {
	sqlDB := newTestDB(t)
	tmpl := nopTemplates()
	tmpl.DB = sqlDB
	h := &SettingsHandler{DB: sqlDB, Tmpl: tmpl, Notifications: notifications.NewStore()}
	router, user := newSettingsRouter(t, sqlDB, h)
	if err := db.SaveThemeSelection(sqlDB, user.ID, "lcars", "removed-variant", false); err != nil {
		t.Fatal(err)
	}
	body := authedRequest(t, sqlDB, router, user, http.MethodGet, "/settings", "", "").Body.String()
	option := regexp.MustCompile(`<option value="lcars"[^>]*selected>`)
	if !strings.Contains(body, `<html lang="en" data-theme="lcars"`) || !option.MatchString(body) {
		t.Fatal("expected LCARS base palette to be rendered and selected")
	}
}

func TestTintSaveUsesTheFormTheme(t *testing.T) {
	sqlDB := newTestDB(t)
	tmpl := nopTemplates()
	tmpl.DB = sqlDB
	h := &SettingsHandler{DB: sqlDB, Tmpl: tmpl, Notifications: notifications.NewStore()}
	router, user := newSettingsRouter(t, sqlDB, h)
	if err := db.SaveThemeSelection(sqlDB, user.ID, "lcars", "", false); err != nil {
		t.Fatal(err)
	}
	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/settings/appearance", "tintTheme=win7-aero&tint=%23006abc", "application/x-www-form-urlencoded")
	if got := rec.Header().Get("X-Playlist-Lab-Tint"); got != "win7-aero/#006abc" {
		t.Fatalf("saved tint response: got %q", got)
	}
	tints, err := db.GetThemeTints(sqlDB, user.ID)
	if err != nil || tints["win7-aero"] != "#006abc" || tints["lcars"] != "" {
		t.Fatalf("expected only the submitted theme to change, got %v (%v)", tints, err)
	}
}

func TestEmbeddedThemeCatalogue(t *testing.T) {
	if themeVersion == "" || findTheme(defaultTheme) == nil {
		t.Fatal("embedded catalogue must identify its build and the default theme")
	}
	for _, theme := range ftlThemes {
		if theme.Version != themeVersion {
			t.Errorf("%s has version %s, expected %s", theme.Slug, theme.Version, themeVersion)
		}
		info, err := fs.Stat(static.FS, "themes/"+theme.Slug+".css")
		if err != nil || info.Size() == 0 {
			t.Errorf("%s has no embedded stylesheet: %v", theme.Slug, err)
		}
	}
}

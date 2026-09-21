package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/static"
)

// The adoption promise: with no theme chosen — the default for every
// existing account — the page carries data-theme="none" and NO theme
// stylesheet, so the app renders exactly as it did before themes existed.
func TestAppearanceTheme_DefaultsToAppsOwnLook(t *testing.T) {
	sqlDB := newTestDB(t)
	tmpl := nopTemplates()
	tmpl.DB = sqlDB
	h := &SettingsHandler{DB: sqlDB, Tmpl: tmpl, Notifications: notifications.NewStore()}
	router, user := newSettingsRouter(t, sqlDB, h)

	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/settings", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-theme="none"`) {
		t.Errorf("expected data-theme=none by default, body=%s", body)
	}
	// href="" would resolve to this page and be fetched as CSS.
	if strings.Contains(body, `id="ftl-theme" rel="stylesheet" href=""`) {
		t.Error(`unthemed pages must omit href entirely, not emit href=""`)
	}

	slug, err := db.GetTheme(sqlDB, user.ID)
	if err != nil || slug != ThemeNone {
		t.Fatalf("GetTheme default = (%q, %v), want (none, nil)", slug, err)
	}
}

func TestAppearanceTheme_SavesAndLinksStylesheet(t *testing.T) {
	sqlDB := newTestDB(t)
	tmpl := nopTemplates()
	tmpl.DB = sqlDB
	h := &SettingsHandler{DB: sqlDB, Tmpl: tmpl, Notifications: notifications.NewStore()}
	router, user := newSettingsRouter(t, sqlDB, h)

	rec := authedRequest(t, sqlDB, router, user, http.MethodPost, "/settings/appearance",
		"theme=lcars", "application/x-www-form-urlencoded")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d; body=%s", rec.Code, rec.Body.String())
	}
	if slug, err := db.GetTheme(sqlDB, user.ID); err != nil || slug != "lcars" {
		t.Fatalf("GetTheme after save = (%q, %v), want (lcars, nil)", slug, err)
	}

	body := authedRequest(t, sqlDB, router, user, http.MethodGet, "/settings", "", "").Body.String()
	if !strings.Contains(body, `data-theme="lcars"`) {
		t.Error("expected data-theme=lcars after save")
	}
	if !strings.Contains(body, `href="/static/themes/lcars.css"`) {
		t.Error("expected the lcars bundle to be linked after save")
	}
}

// Each control in Settings > Appearance posts to the one endpoint, and htmx
// sends only the form that changed — so saving one must not reset the other.
func TestAppearanceTheme_DoesNotClobberTextScale(t *testing.T) {
	sqlDB := newTestDB(t)
	tmpl := nopTemplates()
	tmpl.DB = sqlDB
	h := &SettingsHandler{DB: sqlDB, Tmpl: tmpl, Notifications: notifications.NewStore()}
	router, user := newSettingsRouter(t, sqlDB, h)

	authedRequest(t, sqlDB, router, user, http.MethodPost, "/settings/appearance",
		"textScale=large", "application/x-www-form-urlencoded")
	authedRequest(t, sqlDB, router, user, http.MethodPost, "/settings/appearance",
		"theme=tron", "application/x-www-form-urlencoded")

	if scale, _ := db.GetTextScale(sqlDB, user.ID); scale != "large" {
		t.Errorf("text scale = %q after saving a theme, want large", scale)
	}
	if slug, _ := db.GetTheme(sqlDB, user.ID); slug != "tron" {
		t.Errorf("theme = %q, want tron", slug)
	}

	// And the reverse order.
	authedRequest(t, sqlDB, router, user, http.MethodPost, "/settings/appearance",
		"textScale=small", "application/x-www-form-urlencoded")
	if slug, _ := db.GetTheme(sqlDB, user.ID); slug != "tron" {
		t.Errorf("theme = %q after saving text scale, want tron", slug)
	}
}

func TestUnknownThemeFallsBackToAppsOwnLook(t *testing.T) {
	sqlDB := newTestDB(t)
	tmpl := nopTemplates()
	tmpl.DB = sqlDB
	h := &SettingsHandler{DB: sqlDB, Tmpl: tmpl, Notifications: notifications.NewStore()}
	router, user := newSettingsRouter(t, sqlDB, h)

	// A slug that isn't embedded must never be persisted or linked.
	authedRequest(t, sqlDB, router, user, http.MethodPost, "/settings/appearance",
		"theme=../../etc/passwd", "application/x-www-form-urlencoded")
	if slug, _ := db.GetTheme(sqlDB, user.ID); slug != ThemeNone {
		t.Errorf("unknown slug persisted as %q, want %q", slug, ThemeNone)
	}
	if got := ThemeStylesheet("../../etc/passwd"); got != "" {
		t.Errorf("ThemeStylesheet(traversal) = %q, want \"\"", got)
	}
	if got := ThemeStylesheet(ThemeNone); got != "" {
		t.Errorf("ThemeStylesheet(none) = %q, want \"\"", got)
	}
}

// The bundles under static/ are copies of the pinned submodule made by
// scripts/sync-themes.sh (go:embed cannot reach outside static/). If the
// submodule pin moves without re-running it, the app silently serves stale
// themes — so fail here instead.
func TestEmbeddedThemesMatchSubmodule(t *testing.T) {
	root := filepath.Join("..", "..")
	srcDir := filepath.Join(root, "third_party", "ftl-themes", "dist")
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Skipf("submodule not checked out: %v", err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		want, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got, err := static.FS.ReadFile("themes/" + e.Name())
		if err != nil {
			t.Errorf("themes/%s is not embedded — run scripts/sync-themes.sh", e.Name())
			continue
		}
		if string(got) != string(want) {
			t.Errorf("themes/%s differs from the submodule — run scripts/sync-themes.sh", e.Name())
		}
		checked++
	}
	if checked == 0 {
		t.Error("no theme bundles compared")
	}
	// Every manifest entry must have a servable bundle.
	for _, th := range AvailableThemes() {
		if th.Slug == ThemeNone {
			continue
		}
		if _, err := static.FS.ReadFile("themes/" + th.Slug + ".css"); err != nil {
			t.Errorf("manifest lists %q but themes/%s.css is not embedded", th.Slug, th.Slug)
		}
	}
}

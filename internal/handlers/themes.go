package handlers

import (
	"encoding/json"
	"sync"

	"github.com/drevilish/playlist-lab/static"
)

// ThemeNone is the slug meaning "no theme stylesheet; use the app's own
// look". It is the default for every user, so an existing install renders
// exactly as it did before themes existed.
const ThemeNone = "none"

// Theme is one selectable look, as listed in the ftl-themes manifest that
// scripts/sync-themes.sh copies into static/themes/themes.json.
type Theme struct {
	Slug        string `json:"slug"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

var (
	themesOnce sync.Once
	themes     []Theme
)

// AvailableThemes reads the embedded manifest once. The "none" entry is
// synthesised: it is this app's own look, not one of ftl-themes'.
func AvailableThemes() []Theme {
	themesOnce.Do(func() {
		themes = []Theme{{Slug: ThemeNone, Label: "Playlist Lab", Description: "The app's own look"}}
		data, err := static.FS.ReadFile("themes/themes.json")
		if err != nil {
			return
		}
		var parsed []Theme
		if err := json.Unmarshal(data, &parsed); err != nil {
			return
		}
		themes = append(themes, parsed...)
	})
	return themes
}

// IsKnownTheme reports whether a slug names a bundle that is actually
// embedded, so a stale or hand-edited value falls back to the app's look
// rather than linking a stylesheet that 404s.
func IsKnownTheme(slug string) bool {
	if slug == "" || slug == ThemeNone {
		return false
	}
	for _, t := range AvailableThemes() {
		if t.Slug == slug && t.Slug != ThemeNone {
			return true
		}
	}
	return false
}

// ThemeStylesheet returns the href for a slug, or "" when the app's own look
// applies. Callers must omit the <link>'s href attribute entirely when this
// is empty: href="" resolves to the current page, so the browser would fetch
// the HTML document and try to parse it as CSS.
func ThemeStylesheet(slug string) string {
	if !IsKnownTheme(slug) {
		return ""
	}
	return "/static/themes/" + slug + ".css"
}

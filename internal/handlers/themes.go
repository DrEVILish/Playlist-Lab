// Package handlers: themes.go backs Settings > Appearance's theme picker -
// ftl-themes (github.com/DrEVILish/ftl-themes) compiled bundles under
// static/themes/, refetched by scripts/fetch-themes.sh (run at build time,
// per that repo's own versioning - this app never edits those files
// directly, only PRs against the upstream repo do, per its own contract).
package handlers

import (
	"encoding/json"
	"regexp"

	"github.com/drevilish/playlist-lab/static"
)

// ftlTheme mirrors one entry of static/themes/themes.json (ftl-themes'
// dist/themes.json, copied verbatim by the fetch script).
type ftlTheme struct {
	Slug        string `json:"slug"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Version     string `json:"version"`
	// Variants are the theme's sub-themes, applied as <html data-variant>
	// and offered beneath the theme in the picker (CONTRACT.md "Palette
	// variants").
	Variants []ftlVariant `json:"variants"`
	// Tint, when present, is a user-chosen colour the app must expose as an
	// appearance setting (CONTRACT.md "Theme tint").
	Tint *ftlTint `json:"tint"`
}

type ftlVariant struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type ftlTint struct {
	Token   string `json:"token"`   // custom property, e.g. "--aero-tint"
	Default string `json:"default"` // "#rrggbb"
	Label   string `json:"label"`   // control label, e.g. "Window Color"
}

// defaultTheme is what a user who never picked a theme gets. The app ships
// no CSS of its own, so "no theme" isn't an option - CONTRACT.md: link
// exactly one dist/<slug>.css at a time.
const defaultTheme = "blue-future"

// themeVersion is themes.json's build hash (identical on every entry),
// appended to the theme <link> as ?v= so an upgraded bundle is never
// served stale from cache (CONTRACT.md "Cache-busting").
var themeVersion = func() string {
	if len(ftlThemes) == 0 {
		return ""
	}
	return ftlThemes[0].Version
}()

// ftlThemes is parsed once at startup rather than on every Settings page
// render - static/themes/themes.json only changes when the fetch script
// re-runs (a build-time step), never while the server is running.
var ftlThemes = loadFTLThemes()

func loadFTLThemes() []ftlTheme {
	raw, err := static.FS.ReadFile("themes/themes.json")
	if err != nil {
		return nil
	}
	var themes []ftlTheme
	if err := json.Unmarshal(raw, &themes); err != nil {
		return nil
	}
	return themes
}

// themeSelection keeps the saved picker and the rendered palette in sync
// when a vendor update removes a theme or one of its variants.
func themeSelection(slug, variant string) (string, string) {
	if findTheme(slug) == nil {
		return "", ""
	}
	if !isValidVariant(slug, variant) {
		variant = ""
	}
	return slug, variant
}

// findTheme returns a slug's themes.json entry, or nil if unknown.
func findTheme(slug string) *ftlTheme {
	for i := range ftlThemes {
		if ftlThemes[i].Slug == slug {
			return &ftlThemes[i]
		}
	}
	return nil
}

// themeLabel is a slug's display name from themes.json ("" if unknown).
func themeLabel(slug string) string {
	if t := findTheme(slug); t != nil {
		return t.Label
	}
	return ""
}

// isValidVariant reports whether id is "" (the theme's base palette) or one
// of slug's own variants.
func isValidVariant(slug, id string) bool {
	if id == "" {
		return true
	}
	if t := findTheme(slug); t != nil {
		for _, v := range t.Variants {
			if v.ID == id {
				return true
			}
		}
	}
	return false
}

// hexColor matches the only tint value accepted: <input type="color">'s
// "#rrggbb", which is also all that ever gets rendered into <html style>.
var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// cssToken guards a manifest tint token before it's rendered as a CSS
// custom-property name.
var cssToken = regexp.MustCompile(`^--[a-z0-9-]+$`)

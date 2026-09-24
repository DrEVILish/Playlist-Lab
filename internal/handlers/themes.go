// Package handlers: themes.go backs Settings > Appearance's theme picker -
// ftl-themes (github.com/DrEVILish/ftl-themes) compiled bundles under
// static/themes/, refetched by scripts/fetch-themes.sh (run at build time,
// per that repo's own versioning - this app never edits those files
// directly, only PRs against the upstream repo do, per its own contract).
package handlers

import (
	"encoding/json"

	"github.com/drevilish/playlist-lab/static"
)

// ftlTheme mirrors one entry of static/themes/themes.json (ftl-themes'
// dist/themes.json, copied verbatim by the fetch script).
type ftlTheme struct {
	Slug        string `json:"slug"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

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

// isValidTheme reports whether slug is either "" (this app's own default
// look) or a real ftl-themes slug - the same "fall back to the safe
// default on anything unrecognized" guard saveAppearance already applies
// to textScale.
func isValidTheme(slug string) bool {
	if slug == "" {
		return true
	}
	for _, t := range ftlThemes {
		if t.Slug == slug {
			return true
		}
	}
	return false
}

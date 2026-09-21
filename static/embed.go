// Package static embeds CSS/JS assets so the compiled binary serves them
// with no separate static-file deployment step.
package static

import "embed"

// Root-level assets (logo, favicons, manifest) need listing explicitly:
// "css js" alone silently excluded them, so every one of them 404'd. Same
// reason service-logos is spelled out below rather than relying on some
// broader glob to reach it.
//
// themes/ and assets/ are copied in from the ftl-themes submodule by
// scripts/sync-themes.sh (go:embed cannot reach outside this package's own
// directory, so the files have to live here). They are served as siblings —
// /static/themes/x.css resolves ../assets/fonts/... to /static/assets/fonts/
// — which is the layout ftl-themes' bundles require. A test asserts the
// copies still match the submodule, so they cannot silently drift.
//
//go:embed css js service-logos themes assets *.svg *.ico *.png *.json
var FS embed.FS

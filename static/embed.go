// Package static embeds CSS/JS assets so the compiled binary serves them
// with no separate static-file deployment step.
package static

import "embed"

// Root-level assets (logo, favicons, manifest) need listing explicitly:
// "css js" alone silently excluded them, so every one of them 404'd. Same
// reason service-logos/themes/assets are spelled out below rather than
// relying on some broader glob to reach them. themes/ (ftl-themes' compiled
// dist/<name>.css bundles, refetched by scripts/fetch-themes.sh) and
// assets/ (the fonts those bundles reference as ../assets/... - see that
// contract's "Serving the assets" section) must stay siblings under
// /static/ for those relative font URLs to resolve.
//
//go:embed js service-logos themes assets *.svg *.ico *.png *.json
var FS embed.FS

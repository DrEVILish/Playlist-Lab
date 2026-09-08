// Package static embeds CSS/JS assets so the compiled binary serves them
// with no separate static-file deployment step.
package static

import "embed"

// Root-level assets (logo, favicons, manifest) need listing explicitly:
// "css js" alone silently excluded them, so every one of them 404'd. Same
// reason service-logos is spelled out below rather than relying on some
// broader glob to reach it.
//
//go:embed css js service-logos *.svg *.ico *.png *.json
var FS embed.FS

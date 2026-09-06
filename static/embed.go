// Package static embeds CSS/JS assets so the compiled binary serves them
// with no separate static-file deployment step.
package static

import "embed"

// Root-level assets (logo, favicons, manifest) need listing explicitly:
// "css js" alone silently excluded them, so every one of them 404'd.
//
//go:embed css js *.svg *.ico *.png *.json
var FS embed.FS

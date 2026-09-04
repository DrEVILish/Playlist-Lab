// Package static embeds CSS/JS assets so the compiled binary serves them
// with no separate static-file deployment step.
package static

import "embed"

//go:embed css js
var FS embed.FS

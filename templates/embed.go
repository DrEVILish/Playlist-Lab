// Package templates embeds the html/template source files so the compiled
// binary ships with no external template files to deploy alongside it.
package templates

import "embed"

//go:embed *.html partials/*.html
var FS embed.FS

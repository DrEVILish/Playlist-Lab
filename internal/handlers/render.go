package handlers

import (
	"html/template"
	"log/slog"
	"net/http"
)

// execute runs the named template and writes it to w, logging (rather than
// panicking) on failure since a broken template shouldn't take the whole
// request handler down uncontrolled.
func execute(w http.ResponseWriter, tmpl *template.Template, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("template render failed", "template", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

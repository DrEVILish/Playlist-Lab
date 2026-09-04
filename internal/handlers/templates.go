package handlers

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Templates loads the layout + per-page templates (each combined with the
// shared "base" layout so every page gets consistent <head>/nav) and the
// fragment partials used by HTMX responses, kept in a separate set since
// they're rendered standalone rather than wrapped in the page layout.
type Templates struct {
	pages    map[string]*template.Template
	partials *template.Template
}

// LoadTemplates parses templates/layout.html as the shared base, every
// top-level templates/*.html file as its own page (cloning the base so
// each page's "content" definition doesn't clobber another page's), and
// every templates/partials/*.html file into one combined set for fragment
// responses.
func LoadTemplates(fsys fs.FS) (*Templates, error) {
	base, err := template.ParseFS(fsys, "layout.html")
	if err != nil {
		return nil, fmt.Errorf("parsing layout: %w", err)
	}

	pageFiles, err := fs.Glob(fsys, "*.html")
	if err != nil {
		return nil, err
	}
	pages := make(map[string]*template.Template, len(pageFiles))
	for _, f := range pageFiles {
		if f == "layout.html" {
			continue
		}
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("cloning base for %s: %w", f, err)
		}
		if _, err := clone.ParseFS(fsys, f); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", f, err)
		}
		name := strings.TrimSuffix(path.Base(f), ".html")
		pages[name] = clone
	}

	partials, err := template.ParseFS(fsys, "partials/*.html")
	if err != nil {
		return nil, fmt.Errorf("parsing partials: %w", err)
	}

	return &Templates{pages: pages, partials: partials}, nil
}

func (t *Templates) RenderPage(w http.ResponseWriter, name string, data any) {
	tmpl, ok := t.pages[name]
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	execute(w, tmpl, "base", data)
}

func (t *Templates) RenderPartial(w http.ResponseWriter, name string, data any) {
	execute(w, t.partials, name, data)
}

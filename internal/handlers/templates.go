package handlers

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// tmplFuncs are available to every page/partial template. "now" backs the
// footer's copyright year - the only place a template needs the current
// time - so a full FuncMap-per-caller isn't needed.
var notificationTypeLabels = map[string]string{
	"deemix":         "Deemix download",
	"lidarr":         "Lidarr search",
	"retry-match":    "Matching missing tracks",
	"schedule":       "Scheduled refresh",
	"import":         "Import",
	"track-vanished": "Tracks vanished",
	"action":         "Action",
	"mix":            "Mix generation",
}

var tmplFuncs = template.FuncMap{
	"now": time.Now,
	// notificationTypeLabel backs the bell's per-item title line, matching
	// NotificationCenter.tsx's TYPE_LABEL map (plus "mix", which this Go
	// port added and the React map never had a type for).
	"notificationTypeLabel": func(t string) string {
		if label, ok := notificationTypeLabels[t]; ok {
			return label
		}
		return t
	},
	// relativeTime backs the bell's per-item timestamp, matching
	// NotificationCenter.tsx's relativeTime().
	"relativeTime": func(t time.Time) string {
		seconds := int(time.Since(t).Seconds())
		if seconds < 0 {
			seconds = 0
		}
		if seconds < 60 {
			return "just now"
		}
		minutes := (seconds + 30) / 60
		if minutes < 60 {
			return fmt.Sprintf("%dm ago", minutes)
		}
		hours := (minutes + 30) / 60
		return fmt.Sprintf("%dh ago", hours)
	},
	// percent backs the cross-import progress bar's width (current/total*100,
	// 0 if total is 0) - the only place a template needs this ratio.
	"percent": func(current, total int) int {
		if total <= 0 {
			return 0
		}
		return current * 100 / total
	},
	// add backs 1-based row numbers in list partials (e.g. cross-import's
	// playlist list falling back to a plain index when there's no cover art).
	"add": func(a, b int) int { return a + b },
	// join/joinFloats back the mix-template edit form's comma-separated
	// inputs (genres, moods, decades, ...) - the inverse of the handler's
	// commaListToAny/commaListToFloats parsing on submit.
	"join": func(items []string) string { return strings.Join(items, ", ") },
	// dateFromUnix backs "Date Added" on the home page - unix seconds (0 for
	// playlists never tracked through Playlist Lab) to a short date string.
	"dateFromUnix": func(sec int64) string {
		if sec == 0 {
			return ""
		}
		return time.Unix(sec, 0).Format("Jan 2, 2006")
	},
	// dateFromUnixMilli is dateFromUnix for the columns stored in
	// milliseconds (e.g. playlist_shares.shared_at), matching
	// database.ts's Date.now()-based timestamps.
	"dateFromUnixMilli": func(ms int64) string {
		if ms == 0 {
			return ""
		}
		return time.UnixMilli(ms).Format("Jan 2, 2006")
	},
	// formatDuration backs the Duration column - ms to "1h 23m"/"45m",
	// matching PlaylistsPage.tsx's formatDuration.
	"formatDuration": func(ms int64) string {
		totalMinutes := ms / 60000
		hours := totalMinutes / 60
		minutes := totalMinutes % 60
		if hours > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dm", minutes)
	},
	// sortArrow shows the active sort column's direction, mirroring
	// SortableHeader's ▲/▼ in PlaylistsPage.tsx.
	"sortArrow": func(key, curSort, curDir string) string {
		if curSort != key {
			return ""
		}
		if curDir == "desc" {
			return "▼"
		}
		return "▲"
	},
	"joinFloats": func(items []float64) string {
		parts := make([]string, len(items))
		for i, f := range items {
			parts[i] = strconv.Itoa(int(f))
		}
		return strings.Join(parts, ", ")
	},
}

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
	base, err := template.New("layout.html").Funcs(tmplFuncs).ParseFS(fsys, "layout.html")
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

	partials, err := template.New("partials").Funcs(tmplFuncs).ParseFS(fsys, "partials/*.html")
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

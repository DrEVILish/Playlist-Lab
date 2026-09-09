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

	"github.com/drevilish/playlist-lab/internal/auth"
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

// serviceLogos are the ServiceMeta.Icon values with a real logo file under
// static/service-logos/ (restored from apps/web/public/service-logos/,
// deleted along with the rest of the v2 frontend) - matches ServiceIcon.tsx's
// LOGO_MAP exactly, including which services it deliberately left out
// (aria/billboard/lastfm chart sources never had a brand asset).
var serviceLogos = map[string]bool{
	"amazon": true, "apple": true, "deezer": true, "listenbrainz": true,
	"plex": true, "qobuz": true, "spotify": true, "tidal": true,
	"youtube": true, "youtube-music": true,
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
	// serviceLogo backs the import-page source picker (ServiceMeta.Icon is
	// an internal id like "deezer"/"youtube-music", not literal display
	// text - it used to render as the id string itself before this
	// existed). Mirrors ServiceIcon.tsx's LOGO_MAP: services with a real
	// logo file get its static path, everything else (aria/billboard/
	// lastfm chart sources have no brand asset, same as the original) gets
	// "" so the template falls back to a plain initials badge.
	"serviceLogo": func(icon string) string {
		if serviceLogos[icon] {
			return "/static/service-logos/" + icon + ".png"
		}
		return ""
	},
	// initials backs that same fallback badge - first two letters of the
	// service name, upper-cased, matching ServiceIcon.tsx's FallbackIcon.
	"initials": func(name string) string {
		r := []rune(strings.ToUpper(name))
		if len(r) > 2 {
			r = r[:2]
		}
		return string(r)
	},
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
	// toggleDir backs the mobile playlists sortbar's standalone direction
	// button (PlaylistsPage.tsx's setSortDir toggle) - unlike sortArrow/
	// SortHref, it flips the direction regardless of which column is
	// active, since the mobile sort key (a <select>) and direction (this
	// button) are independent controls, not one combined toggle-on-click
	// per column like the desktop table's headers.
	"toggleDir": func(curDir string) string {
		if curDir == "desc" {
			return "asc"
		}
		return "desc"
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

// RenderPage renders name inside the shared "base" layout, which reads
// .User from data to decide whether to show the header/footer chrome at
// all (see layout.html) - so every caller needs it there. Rather than
// leaving that as an easy-to-forget convention (found the hard way: editor
// and setup's RenderPage calls never set it, silently losing their header
// and footer entirely once layout.html started gating on it), RenderPage
// fills it in here from the request's own session if the caller's data
// didn't already set it, the same value every caller that does set it
// explicitly already computes via auth.CurrentUser(r). A caller passing
// nil (login's RenderPage(w, r, "login", nil) - genuinely no user) still
// gets a real, possibly-nil User key rather than a template lookup on a
// bare nil interface.
func (t *Templates) RenderPage(w http.ResponseWriter, r *http.Request, name string, data any) {
	tmpl, ok := t.pages[name]
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	execute(w, tmpl, "base", pageData(r, data))
}

// RenderModal renders name's own "content" block (the same template a full
// page load would use, skipping layout.html's header/footer chrome) wrapped
// in the app's standard modal chrome, for a header button that opens a page
// as a modal instead of navigating to it - see IsModalRequest, which the
// page's own handler checks to decide which of RenderPage/RenderModal to
// call. Reuses the page's existing template and data-building code
// unchanged; only the final render call branches.
func (t *Templates) RenderModal(w http.ResponseWriter, r *http.Request, name, title string, data any) {
	tmpl, ok := t.pages[name]
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "content", pageData(r, data)); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	t.RenderPartial(w, "partials/page_modal.html", map[string]any{
		"Title": title,
		"Body":  template.HTML(buf.String()),
	})
}

// IsModalRequest reports whether r should be answered with RenderModal
// instead of RenderPage - true for the htmx-driven header buttons (which
// send the standard HX-Request header), false for a plain browser
// navigation/refresh, so a bookmarked or directly-visited URL still gets
// the full page rather than a bare modal fragment with no chrome around it.
func IsModalRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// pageData ensures data (RenderPage/RenderModal's map[string]any, or nil)
// has a "User" key, filling it in from the request's session if the caller
// didn't already set one - see RenderPage's original doc comment for why
// this can't just be left to each caller.
func pageData(r *http.Request, data any) map[string]any {
	m, ok := data.(map[string]any)
	if !ok {
		m = map[string]any{}
	}
	if _, exists := m["User"]; !exists {
		m["User"] = auth.CurrentUser(r)
	}
	return m
}

func (t *Templates) RenderPartial(w http.ResponseWriter, name string, data any) {
	execute(w, t.partials, name, data)
}

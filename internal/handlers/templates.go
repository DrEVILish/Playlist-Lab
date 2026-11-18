package handlers

import (
	"database/sql"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
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

// serviceLogos maps a ServiceMeta.Icon value to the static/service-logos/
// file (without extension) that backs it - originally a 1:1 map.Icon==
// filename, now also handling two icons sharing one file: youtube-music
// uses the plain YouTube mark rather than a separate one, since they're the
// same service to a user picking an import source. CSS filters (see
// .source-tab-icon/.playlist-source-logo) theme every mark to the app's
// SciFi palette rather than rendering each one's fixed brand colors.
var serviceLogos = map[string]string{
	"amazon": "amazon", "apple": "apple", "aria": "aria", "billboard": "billboard",
	"deezer": "deezer", "lastfm": "lastfm", "listenbrainz": "listenbrainz",
	"plex": "plex", "qobuz": "qobuz", "spotify": "spotify", "tidal": "tidal",
	"youtube": "youtube", "youtube-music": "youtube",
}

var sourceLabels = map[string]string{
	"amazon": "Amazon Music", "apple": "Apple Music", "aria": "ARIA Charts",
	"billboard": "Billboard", "deezer": "Deezer", "lastfm": "Last.fm",
	"listenbrainz": "ListenBrainz", "plex": "Plex", "qobuz": "Qobuz",
	"spotify": "Spotify", "tidal": "Tidal", "youtube": "YouTube",
	"youtube-music": "YouTube Music", "ai": "AI", "file": "File",
}

// facetExampleWords backs the "facetExample" template func above - one
// illustrative value per Dynamic Collection facet type (handlers/
// dynamic_collections.go's facetTypeLabels), kept as its own map rather
// than piggybacking on that one since the two serve different purposes
// (a <select> label vs. a live preview value).
var facetExampleWords = map[string]string{
	"genre":          "Action",
	"decade":         "1990s",
	"year":           "1999",
	"content_rating": "PG-13",
	"studio":         "A24",
	"actor":          "Tom Hanks",
	"director":       "Christopher Nolan",
	"writer":         "Aaron Sorkin",
	"mood":           "Chill",
	"style":          "Acoustic",
}

var tmplFuncs = template.FuncMap{
	"now":   time.Now,
	"lower": strings.ToLower,
	// hasString backs the Collections page's multi-select Server/Library
	// filter popovers (a checkbox-style option is "checked" when its value
	// is anywhere in the active filter's slice, not just an exact single
	// match) - html/template has no built-in slice-membership test.
	"hasString": func(list []string, val string) bool {
		for _, v := range list {
			if v == val {
				return true
			}
		}
		return false
	},
	// replace backs the Dynamic Collections title format field's live
	// example preview (a real "<<key_name>>" substitution shown server-side
	// on first render, kept in sync client-side by dynamic-collection-
	// form.js as the user types) - a thin wrapper since html/template
	// doesn't expose strings.ReplaceAll to templates by default.
	"replace": strings.ReplaceAll,
	// facetExample backs that same preview's example value - a genre set
	// previewing as "Top Action" reads very differently (and far more
	// usefully) than a decade set previewing as the same word, so each
	// facet type gets its own illustrative example rather than one generic
	// word for every type.
	"facetExample": func(facetType string) string {
		if example, ok := facetExampleWords[facetType]; ok {
			return example
		}
		return "Action"
	},
	// notificationTypeLabel backs the bell's per-item title line, matching
	// NotificationCenter.tsx's TYPE_LABEL map (plus "mix", which this Go
	// port added and the React map never had a type for).
	"notificationTypeLabel": func(t string) string {
		if label, ok := notificationTypeLabels[t]; ok {
			return label
		}
		return t
	},
	// lampState maps a "success"/"failed"/"running"/"" status string (the
	// shape playlists.go/collections.go's LastRunStatus and admin
	// enabled/disabled flags already use) onto ftl-themes' .lamp modifier
	// class - "" (never run) deliberately maps to "", since a bare .lamp
	// with no is-* class is already the lamp's off/unlit state.
	"lampState": func(status string) string {
		switch status {
		case "success":
			return "is-on"
		case "failed":
			return "is-error"
		case "running":
			return "is-active"
		default:
			return ""
		}
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
	// existed). A source with no entry in serviceLogos gets "" so the
	// template falls back to a plain initials badge.
	"serviceLogo": func(icon string) string {
		if file, ok := serviceLogos[icon]; ok {
			return "/static/service-logos/" + file + ".svg"
		}
		return ""
	},
	// sourceLabel is a playlist source id as people say it ("apple" ->
	// "Apple Music"): text stays readable in every theme, where brand
	// wordmarks drawn for light or dark backgrounds don't.
	"sourceLabel": func(id string) string {
		if l, ok := sourceLabels[id]; ok {
			return l
		}
		if id == "" {
			return ""
		}
		return strings.ToUpper(id[:1]) + id[1:]
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
	// matching PlaylistsPage.tsx's formatDuration. Playlist/sync *total*
	// duration per DESIGN.md §7 - never used for a single track.
	"formatDuration": func(ms int64) string {
		totalMinutes := ms / 60000
		hours := totalMinutes / 60
		minutes := totalMinutes % 60
		if hours > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dm", minutes)
	},
	// formatTrackDuration backs a single track's duration (m:ss, DESIGN.md
	// §7) - distinct from formatDuration's h:mm playlist-total form.
	"formatTrackDuration": func(ms int64) string {
		seconds := ms / 1000
		return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
	},
	// relativeUnix backs "Date Added"/"Last Run" (DESIGN.md §7): a coarse
	// relative string ("2 days ago"), with the exact timestamp left for the
	// caller to put in a title="" tooltip via dateFromUnix. Day/month/year
	// buckets are approximate (30/365-day months/years) - fine for a UI
	// label that's already deliberately imprecise below the day level.
	"relativeUnix": func(sec int64) string {
		if sec == 0 {
			return ""
		}
		d := time.Since(time.Unix(sec, 0))
		if d < 0 {
			d = 0
		}
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return fmt.Sprintf("%dm ago", int(d.Minutes()))
		case d < 24*time.Hour:
			return fmt.Sprintf("%dh ago", int(d.Hours()))
		case d < 30*24*time.Hour:
			days := int(d.Hours() / 24)
			if days == 1 {
				return "1 day ago"
			}
			return fmt.Sprintf("%d days ago", days)
		case d < 365*24*time.Hour:
			months := int(d.Hours() / 24 / 30)
			if months <= 1 {
				return "1 month ago"
			}
			return fmt.Sprintf("%d months ago", months)
		default:
			years := int(d.Hours() / 24 / 365)
			if years <= 1 {
				return "1 year ago"
			}
			return fmt.Sprintf("%d years ago", years)
		}
	},
	// ariaSort is a sortable <th>'s aria-sort value: "ascending"/
	// "descending" on the active sort column, "none" (sortable, not sorted)
	// elsewhere. ftl-themes draws the arrow and the clickable look from the
	// attribute (CONTRACT.md "Tables"), and it's what a screen reader reads.
	"ariaSort": func(key, curSort, curDir string) string {
		if curSort != key {
			return "none"
		}
		if curDir == "desc" {
			return "descending"
		}
		return "ascending"
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
	// DB is optional - set by main.go after construction (LoadTemplates
	// itself needs no DB access to parse files off disk). nil in tests that
	// call LoadTemplates directly without wiring it up; pageData treats a
	// nil DB the same as "no saved preference yet" rather than panicking.
	DB *sql.DB
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
// pageTitles names each RenderPage template for the <title> tag - every
// page rendered "Playlist Lab" with no per-page distinction, so every
// browser tab/history entry looked identical no matter which page was
// open. "editor" is set per-request instead (the playlist's own name is
// more useful there than a generic "Playlist Editor").
var pageTitles = map[string]string{
	"backup_restore":      "Backup & Restore",
	"cross_import":        "Export to YouTube",
	"dynamic_collections": "Dynamic Collections",
	"home":                "Playlists",
	"import":              "Import a Playlist",
	"missing":             "Missing Tracks",
	"mixes":               "Mixes",
	"mix_templates":       "Saved Mixes",
	"settings":            "Settings",
	"setup":               "Plex Server",
	"status":              "Status & Reports",
}

func (t *Templates) RenderPage(w http.ResponseWriter, r *http.Request, name string, data any) {
	tmpl, ok := t.pages[name]
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	m := t.pageData(r, data)
	if _, exists := m["PageTitle"]; !exists {
		if name == "editor" {
			if playlistName, ok := m["Name"].(string); ok && playlistName != "" {
				m["PageTitle"] = playlistName
			}
		} else if title, ok := pageTitles[name]; ok {
			m["PageTitle"] = title
		}
	}
	execute(w, tmpl, "base", m)
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
	m := t.pageData(r, data)
	m["InModal"] = true // the modal header carries the title; pages skip their own
	if err := tmpl.ExecuteTemplate(&buf, "content", m); err != nil {
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

// textScaleMultiplier maps a Settings > Appearance choice to the CSS
// multiplier templates/layout.html's <html> sets --text-scale to. Kept next
// to pageData rather than in JS, now that the value is rendered server-side
// (DESIGN.md §14) instead of read from localStorage on every page load.
var textScaleMultiplier = map[string]string{
	"small":  "0.9",
	"medium": "1",
	"large":  "1.15",
}

// pageData ensures data (RenderPage/RenderModal's map[string]any, or nil)
// has "User", "TextScale", "Theme", and "IsPlexOwner" keys, filling them in
// from the request's session/DB if the caller didn't already set one - see
// RenderPage's original doc comment for why this can't just be left to each
// caller. IsPlexOwner drives the Collections nav link (DESIGN.md §11.11)
// across every page via the shared layout.html base, so it's centralized
// here rather than requiring each handler to set it individually.
func (t *Templates) pageData(r *http.Request, data any) map[string]any {
	m, ok := data.(map[string]any)
	if !ok {
		m = map[string]any{}
	}
	if _, exists := m["User"]; !exists {
		m["User"] = auth.CurrentUser(r)
	}
	if _, exists := m["TextScale"]; !exists {
		m["TextScale"] = "1"
		if t.DB != nil {
			if user := auth.CurrentUser(r); user != nil {
				if scale, err := db.GetTextScale(t.DB, user.ID); err == nil {
					if mult, ok := textScaleMultiplier[scale]; ok {
						m["TextScale"] = mult
					}
				}
			}
		}
	}
	// Theme: the ftl-themes slug for <html data-theme="..."> and the one
	// bundle layout.html links. The app has no CSS of its own, so a user
	// who never picked one ("" in user_settings) gets defaultTheme rather
	// than no theme. Server-rendered like TextScale above, for the same
	// reason - set before first paint, no flash of the wrong theme.
	if _, exists := m["Theme"]; !exists {
		m["Theme"] = defaultTheme
		if t.DB != nil {
			if user := auth.CurrentUser(r); user != nil {
				// findTheme: a slug upstream has since removed would link a
				// stylesheet that no longer exists - an unstyled page.
				if theme, err := db.GetTheme(t.DB, user.ID); err == nil && findTheme(theme) != nil {
					m["Theme"] = theme
				}
			}
		}
	}
	// The theme's palette variant (<html data-variant>) and, for a theme
	// that declares a tint, the user's saved colour for it (rendered as the
	// tint token inline on <html>, CONTRACT.md "Theme tint"). Both are
	// dropped when they don't belong to the theme being rendered.
	if _, exists := m["ThemeVariant"]; !exists {
		m["ThemeVariant"] = ""
		m["ThemeTint"] = nil
		if user := auth.CurrentUser(r); user != nil && t.DB != nil {
			slug, _ := m["Theme"].(string)
			if v, err := db.GetThemeVariant(t.DB, user.ID); err == nil && isValidVariant(slug, v) {
				m["ThemeVariant"] = v
			}
			if th := findTheme(slug); th != nil && th.Tint != nil {
				if tints, err := db.GetThemeTints(t.DB, user.ID); err == nil && hexColor.MatchString(tints[slug]) && cssToken.MatchString(th.Tint.Token) {
					// html/template won't interpolate a custom-property
					// *name* into a style attribute, so the declaration is
					// built here - safe: both parts are regex-validated.
					m["ThemeTint"] = template.CSS("; " + th.Tint.Token + ": " + tints[slug])
				}
			}
		}
	}
	m["ThemeVersion"] = themeVersion
	m["Path"] = r.URL.Path
	m["Section"] = navSection(r.URL.Path) // marks the current .nav-item in layout.html
	// IsPlexOwner gates Collections; the status footer names the server
	// and library every music feature works against.
	if t.DB != nil {
		if user := auth.CurrentUser(r); user != nil {
			if servers, err := db.GetUserServers(t.DB, user.ID); err == nil {
				owner := false
				for _, s := range servers {
					owner = owner || s.IsOwner
					if s.IsDefault || len(servers) == 1 {
						m["DefaultServer"] = s
					}
				}
				if _, exists := m["IsPlexOwner"]; !exists {
					m["IsPlexOwner"] = owner
				}
			}
		}
	}
	if _, exists := m["IsPlexOwner"]; !exists {
		m["IsPlexOwner"] = false
	}
	// The nav's Missing tracks count, so unresolved tracks are visible
	// from every page rather than only on the Playlists stats row.
	if _, exists := m["NavMissingCount"]; !exists && t.DB != nil {
		if user := auth.CurrentUser(r); user != nil {
			m["NavMissingCount"], _ = db.CountUserMissingTracks(t.DB, user.ID)
		}
	}
	return m
}

// navSection maps a request path to the main-nav entry it belongs under,
// so sub-pages (an editor, Mix templates, Dynamic collections) keep their
// section highlighted.
func navSection(path string) string {
	switch {
	case path == "/" || strings.HasPrefix(path, "/playlists"):
		return "playlists"
	case strings.HasPrefix(path, "/missing"):
		return "missing"
	case strings.HasPrefix(path, "/import"):
		return "import"
	case strings.HasPrefix(path, "/mix"):
		return "mixes"
	case strings.HasPrefix(path, "/collections"), strings.HasPrefix(path, "/dynamic-collections"):
		return "collections"
	case strings.HasPrefix(path, "/cross-import"):
		return "export"
	}
	return ""
}

func (t *Templates) RenderPartial(w http.ResponseWriter, name string, data any) {
	execute(w, t.partials, name, data)
}

// RenderErrorPage renders templates/error.html inside the shared "base"
// layout (DESIGN.md §10: 404/500 keep the app's shell instead of a plain
// http.Error() body) at the given HTTP status. Not routed through RenderPage
// since that always writes an implicit 200 - the status has to be set before
// the body is written, so this sets it directly rather than layering on top.
func (t *Templates) RenderErrorPage(w http.ResponseWriter, r *http.Request, code int, title, message string) {
	tmpl, ok := t.pages["error"]
	if !ok {
		http.Error(w, message, code)
		return
	}
	m := t.pageData(r, map[string]any{
		"PageTitle":    title,
		"ErrorCode":    code,
		"ErrorTitle":   title,
		"ErrorMessage": message,
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := tmpl.ExecuteTemplate(w, "base", m); err != nil {
		slog.Error("error page render failed", "code", code, "error", err)
	}
}

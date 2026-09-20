// Package handlers: admin.go ports the user-management + stats slice of
// routes/admin.ts to HTMX, plus the Deemix ARL and Lidarr URL/API-key admin
// config (both stored via internal/db/admin_config.go, same as Node's
// configService did). The log viewer/level control, background-job status,
// and the all-users schedule list are still TS-only - each is its own
// separate service integration with its own admin UI surface; they're a
// natural follow-up pass each, wired through the existing
// deemixService/lidarrService/scheduler already constructed in
// cmd/server/main.go.
//
// There is no standalone /admin page - its tabs live inline on /settings
// (settings.go/settings.html), shown only to admins, so "settings" isn't
// split across two pages with two different tab-bar conventions. Every
// handler here renders just its own panel's fragment (this file's
// render*Panel helpers), never a full page, so a save/action can swap that
// one panel back in without disturbing whichever tab the admin is on.
package handlers

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	youtubetarget "github.com/drevilish/playlist-lab/internal/adapters/youtube"
	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	deemixsvc "github.com/drevilish/playlist-lab/internal/services/deemix"
	lidarrsvc "github.com/drevilish/playlist-lab/internal/services/lidarr"
	"github.com/drevilish/playlist-lab/internal/services/matching"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/tmdb"
	"github.com/drevilish/playlist-lab/internal/services/trakt"
)

// Admin config keys, persisted via internal/db/admin_config.go - read once at
// startup (cmd/server/main.go) to seed the Deemix/Lidarr services' Config,
// and written here whenever the admin saves the form.
const (
	configKeyDeemixArl       = "deemix_arl"
	configKeyLidarrURL       = "lidarr_url"
	configKeyLidarrAPIKey    = "lidarr_api_key"
	configKeyYouTubeClientID = "youtube_client_id"
	configKeyYouTubeSecret   = "youtube_client_secret"
	configKeyYouTubeRedirect = "youtube_redirect_uri"
	// configKeyTMDbAPIKey/configKeyTVDbAPIKey/configKeyTraktAPIKey must
	// match scheduler.go's tmdbAdminConfigKey/tvdbAdminConfigKey/
	// traktAdminConfigKey - duplicated rather than imported to avoid a
	// handlers<->scheduler import cycle (handlers already imports scheduler
	// for RefreshCollection). IMDb/Letterboxd need no key (scraping only),
	// so there's no admin config for them.
	configKeyTMDbAPIKey  = "tmdb_api_key"
	configKeyTVDbAPIKey  = "tvdb_api_key"
	configKeyTraktAPIKey = "trakt_api_key"
)

type AdminHandler struct {
	DB            *sql.DB
	Tmpl          *Templates
	Notifications *notifications.Store
	Queue         *actionqueue.Queue
	Deemix        *deemixsvc.Service
	Lidarr        *lidarrsvc.Service
	YouTube       *youtubetarget.Target
}

func RegisterAdmin(r chi.Router, mw *auth.Middleware, h *AdminHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Use(mw.RequireAdmin)
		r.Get("/admin/stats", h.statsPanel)
		r.Get("/admin/users-list", h.usersPanel)
		r.Get("/admin/missing-list", h.missingList)
		r.Get("/admin/deemix", h.deemixPanel)
		r.Get("/admin/lidarr", h.lidarrPanel)
		r.Get("/admin/youtube", h.youtubePanel)
		r.Get("/admin/tmdb", h.tmdbPanel)
		r.Get("/admin/tvdb", h.tvdbPanel)
		r.Get("/admin/trakt", h.traktPanel)
		r.Post("/admin/users/{userID}/enable", h.enableUser)
		r.Post("/admin/users/{userID}/disable", h.disableUser)
		r.Post("/admin/users/{userID}/promote", h.promoteUser)
		r.Post("/admin/users/{userID}/demote", h.demoteUser)
		r.Post("/admin/users/{userID}/delete", h.deleteUser)
		r.Post("/admin/missing/deemix-download", h.deemixDownload)
		r.Post("/admin/missing/deemix-all", h.deemixAll)
		r.Post("/admin/deemix/arl", h.saveDeemixArl)
		r.Post("/admin/deemix/arl/check", h.checkDeemixArl)
		r.Post("/admin/deemix/settings", h.saveDeemixSettings)
		r.Post("/admin/lidarr/config", h.saveLidarrConfig)
		r.Post("/admin/youtube/config", h.saveYouTubeConfig)
		r.Post("/admin/tmdb/config", h.saveTMDbConfig)
		r.Post("/admin/tvdb/config", h.saveTVDbConfig)
		r.Post("/admin/trakt/config", h.saveTraktConfig)
	})
}

// statsPanel backs the Statistics tab's lazy-loaded (hx-trigger="revealed")
// fragment - see missingList's comment for why these all load on reveal
// rather than with the page.
//
// Each stat tile also gets a small inline-SVG sparkline, plus one larger
// trend chart lower on the panel (DESIGN.md §11.5, §8.10). Both are built
// from daily buckets of timestamp columns this app already writes for its
// own reasons (internal/db/admin_stats.go) - no new metrics tracking was
// added just to feed a chart.
func (h *AdminHandler) statsPanel(w http.ResponseWriter, r *http.Request) {
	userCount, _ := db.GetUserCount(h.DB)
	playlistCount, _ := db.GetPlaylistCount(h.DB)
	missingCount, _ := db.GetMissingTrackCount(h.DB)
	users, _ := db.GetAllUsers(h.DB)

	thirtyDaysAgo := time.Now().AddDate(0, 0, -30).UnixMilli()
	activeUsers := 0
	for _, u := range users {
		if u.LastLogin >= thirtyDaysAgo {
			activeUsers++
		}
	}

	const sparklineDays = 14
	userSignups, _ := db.GetUserSignupsByDay(h.DB, sparklineDays)
	activeByDay, _ := db.GetActiveUsersByDay(h.DB, sparklineDays)
	playlistsByDay, _ := db.GetPlaylistsCreatedByDay(h.DB, sparklineDays)
	missingByDay, _ := db.GetMissingTracksAddedByDay(h.DB, sparklineDays)
	execTrend, _ := db.GetScheduleExecutionTrend(h.DB, sparklineDays)

	h.Tmpl.RenderPartial(w, "partials/admin_stats.html", map[string]any{
		"Stats": map[string]int{
			"TotalUsers":     userCount,
			"ActiveUsers":    activeUsers,
			"TotalPlaylists": playlistCount,
			"TotalMissing":   missingCount,
		},
		"UserSparkline":     sparklinePoints(userSignups),
		"ActiveSparkline":   sparklinePoints(activeByDay),
		"PlaylistSparkline": sparklinePoints(playlistsByDay),
		"MissingSparkline":  sparklinePoints(missingByDay),
		"SparklineWidth":    sparklineWidth,
		"SparklineHeight":   sparklineHeight,
		"TrendChart":        buildScheduleTrendChart(execTrend),
		"TrendChartWidth":   trendChartWidth,
		"TrendChartHeight":  trendChartViewHeight,
	})
}

// Sparklines and the one larger trend chart on the Statistics tab are plain
// inline SVG, not a charting library. DESIGN.md §8.10 specifies TanStack
// Charts, but this app is server-rendered Go templates + htmx with no JS
// framework or bundler - TanStack Charts ships only framework adapters
// (React/Solid/Vue), so adopting it here would mean introducing a full JS
// framework + build step for one page. A sparkline is a handful of points
// on an SVG <polyline>, so that's what this renders instead, styled to the
// same tokens TanStack Charts would have used (--accent-teal/--error series,
// --border gridlines, --font-mono labels). Revisit as its own scoped effort
// - see docs/DESIGN.md §17.
const (
	sparklineWidth  = 100
	sparklineHeight = 28

	trendChartWidth      = 640
	trendPlotHeight      = 120
	trendChartViewHeight = 156 // plot area plus room for the day-label row
)

// sparklinePoints turns a daily count series into an SVG <polyline> points
// attribute, scaled to sparklineWidth x sparklineHeight with the series max
// pinned to the top edge.
func sparklinePoints(series []db.DayCount) string {
	counts := make([]int, len(series))
	for i, s := range series {
		counts[i] = s.Count
	}
	return polylinePoints(counts, sparklineWidth, sparklineHeight)
}

// polylinePoints scales a series of non-negative counts into "x,y x,y ..."
// SVG polyline points within width x height, oldest point at x=0.
func polylinePoints(counts []int, width, height float64) string {
	n := len(counts)
	if n == 0 {
		return ""
	}
	max := 1
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	pts := make([]string, n)
	for i, c := range counts {
		x := 0.0
		if n > 1 {
			x = float64(i) / float64(n-1) * width
		}
		y := height - (float64(c)/float64(max))*height
		pts[i] = fmt.Sprintf("%.1f,%.1f", x, y)
	}
	return strings.Join(pts, " ")
}

// trendChartData is the precomputed SVG geometry for the Statistics tab's
// one larger chart - everything arithmetic happens here rather than in the
// template, matching this codebase's "templates stay markup-only" pattern
// (see admin_logs.go's parseEntry comment).
type trendChartData struct {
	SucceededPoints string
	FailedPoints    string
	Max             int
	HalfY           float64
	LabelY          float64
	DayLabels       []trendDayLabel
}

type trendDayLabel struct {
	X      float64
	Label  string
	Anchor string // SVG text-anchor - "start"/"end" for the edge labels so they don't clip past the viewBox, "middle" otherwise
}

// buildScheduleTrendChart renders schedule_executions' daily succeeded/
// failed counts as the two series of the dashboard's sync-activity chart -
// the most directly useful single trend for an admin here, and the closest
// existing proxy for the "active syncs"/"error count" example metrics in
// DESIGN.md §11.5.
func buildScheduleTrendChart(trend []db.ScheduleExecutionTrend) trendChartData {
	n := len(trend)
	succeeded := make([]int, n)
	failed := make([]int, n)
	max := 1
	for i, t := range trend {
		succeeded[i] = t.Succeeded
		failed[i] = t.Failed
		if t.Succeeded > max {
			max = t.Succeeded
		}
		if t.Failed > max {
			max = t.Failed
		}
	}

	data := trendChartData{
		SucceededPoints: polylinePoints(succeeded, trendChartWidth, trendPlotHeight),
		FailedPoints:    polylinePoints(failed, trendChartWidth, trendPlotHeight),
		Max:             max,
		HalfY:           trendPlotHeight / 2,
		LabelY:          trendPlotHeight + 18,
	}

	if n == 0 {
		return data
	}
	labelIdxs := []int{0}
	if n > 2 {
		labelIdxs = append(labelIdxs, n/2)
	}
	if n > 1 {
		labelIdxs = append(labelIdxs, n-1)
	}
	for _, i := range labelIdxs {
		x := 0.0
		anchor := "middle"
		if n > 1 {
			x = float64(i) / float64(n-1) * trendChartWidth
			if i == 0 {
				anchor = "start"
			} else if i == n-1 {
				anchor = "end"
			}
		}
		label := trend[i].Day
		if d, err := time.Parse("2006-01-02", trend[i].Day); err == nil {
			label = d.Format("Jan 2")
		}
		data.DayLabels = append(data.DayLabels, trendDayLabel{X: x, Label: label, Anchor: anchor})
	}
	return data
}

// missingList backs the Missing Tracks tab's lazy-loaded (hx-trigger=
// "revealed") fragment - split out of the main page render so hundreds of
// rows aren't built into every /settings load regardless of which tab is
// active, same pattern Schedules/Logs already use in this file.
func (h *AdminHandler) missingList(w http.ResponseWriter, r *http.Request) {
	missingStats, _ := db.GetMissingTrackStats(h.DB)
	h.Tmpl.RenderPartial(w, "partials/admin_missing.html", map[string]any{"MissingStats": missingStats})
}

func (h *AdminHandler) usersPanel(w http.ResponseWriter, r *http.Request) {
	h.renderUsersPanel(w, r, "")
}

// renderUsersPanel loads the current users list and renders just the Users
// tab's own panel, optionally with an error banner from a just-failed
// action - every user-management handler below funnels back through here
// so the list is always freshly queried rather than patched in place, and
// only that one panel swaps rather than the whole settings page.
//
// The Users tab is cards with filters (DESIGN.md §11.5's one deliberate
// table->card exception, since a user record carries more per-item context
// than a row wants): status (enabled/disabled), role (admin/member) and a
// username search, all read from the request's query/form values so both
// the toolbar's hx-get and a plain page load can drive them. A row action
// (enable/disable/promote/...) re-renders through this same function but
// without re-reading the toolbar (those buttons aren't inside it), so
// filters reset to "all" after an action - acceptable since the list is
// short enough that isn't a real cost, and simpler than threading filter
// state through every action button.
func (h *AdminHandler) renderUsersPanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	users, err := db.GetAllUsers(h.DB)
	if err != nil && errMsg == "" {
		errMsg = err.Error()
	}

	status := r.URL.Query().Get("status")
	role := r.URL.Query().Get("role")
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	filtered := make([]db.AdminUserRow, 0, len(users))
	for _, u := range users {
		if status == "enabled" && !u.IsEnabled {
			continue
		}
		if status == "disabled" && u.IsEnabled {
			continue
		}
		if role == "admin" && !u.IsAdmin {
			continue
		}
		if role == "member" && u.IsAdmin {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(u.PlexUsername), q) {
			continue
		}
		filtered = append(filtered, u)
	}

	h.Tmpl.RenderPartial(w, "partials/admin_users_panel.html", map[string]any{
		"Error":        errMsg,
		"Users":        filtered,
		"TotalUsers":   len(users),
		"FilterQ":      r.URL.Query().Get("q"),
		"FilterStatus": status,
		"FilterRole":   role,
	})
}

func (h *AdminHandler) deemixPanel(w http.ResponseWriter, r *http.Request) {
	h.renderDeemixPanel(w, r, "")
}

func (h *AdminHandler) renderDeemixPanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.renderDeemixPanelWithNotice(w, r, errMsg, "")
}

func (h *AdminHandler) renderDeemixPanelWithNotice(w http.ResponseWriter, r *http.Request, errMsg, notice string) {
	h.Tmpl.RenderPartial(w, "partials/admin_deemix_panel.html", map[string]any{
		"Error":     errMsg,
		"Notice":    notice,
		"DeemixArl": h.Deemix.ARL(),
		"ArlStatus": h.Deemix.LastArlCheck(),
		"Settings":  h.Deemix.Settings(),
	})
}

func (h *AdminHandler) lidarrPanel(w http.ResponseWriter, r *http.Request) {
	h.renderLidarrPanel(w, r, "")
}

func (h *AdminHandler) renderLidarrPanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	lidarrCfg := h.Lidarr.GetConfig()
	h.Tmpl.RenderPartial(w, "partials/admin_lidarr_panel.html", map[string]any{
		"Error":        errMsg,
		"LidarrURL":    lidarrCfg.URL,
		"LidarrAPIKey": lidarrCfg.APIKey,
	})
}

// tmdbPanel/saveTMDbConfig and tvdbPanel/saveTVDbConfig manage the single
// API key each of Collections' TMDb/TVDb external-list builder providers
// needs (DESIGN.md §11.11) - unlike Lidarr/YouTube, there's no long-lived
// service object to update in place, since scheduler.go's
// resolveExternalListTargets just reads admin_config fresh on every
// refresh (a client is cheap to construct per-call, no persistent
// connection to manage). IMDb and Letterboxd need no key at all (scraping
// only), so they have no admin panel here.
func (h *AdminHandler) tmdbPanel(w http.ResponseWriter, r *http.Request) {
	h.renderTMDbPanel(w, r, "")
}

func (h *AdminHandler) renderTMDbPanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.renderTMDbPanelWithNotice(w, r, errMsg, "")
}

func (h *AdminHandler) renderTMDbPanelWithNotice(w http.ResponseWriter, r *http.Request, errMsg, notice string) {
	apiKey, _, _ := db.GetAdminConfig(h.DB, configKeyTMDbAPIKey)
	h.Tmpl.RenderPartial(w, "partials/admin_tmdb_panel.html", map[string]any{
		"Error":  errMsg,
		"Notice": notice,
		"APIKey": apiKey,
	})
}

// saveTMDbConfig persists the key and immediately tests it against TMDb's
// own auth-check endpoint (tmdb.Client.ValidateKey), so an admin finds out
// right away if a key is stale/mistyped instead of only discovering it
// later when a scheduled collection refresh silently fails.
func (h *AdminHandler) saveTMDbConfig(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	_ = r.ParseForm()
	apiKey := strings.TrimSpace(r.FormValue("apiKey"))
	if err := db.SetAdminConfig(h.DB, configKeyTMDbAPIKey, apiKey); err != nil {
		slog.Error("failed to save TMDb config", "error", err, "adminId", admin.ID)
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save TMDb API key", err.Error(), notifications.StatusError, nil)
		h.renderTMDbPanel(w, r, err.Error())
		return
	}
	if apiKey == "" {
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save TMDb API key", "Cleared", notifications.StatusSuccess, nil)
		h.renderTMDbPanelWithNotice(w, r, "", "API key cleared.")
		return
	}
	if err := tmdb.NewClient(apiKey).ValidateKey(); err != nil {
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save TMDb API key", "Saved, but key failed validation: "+err.Error(), notifications.StatusError, nil)
		h.renderTMDbPanelWithNotice(w, r, "Saved, but this key doesn't look valid: "+err.Error(), "")
		return
	}
	h.Notifications.Add(admin.ID, notifications.TypeAction, "Save TMDb API key", "Saved and validated", notifications.StatusSuccess, nil)
	h.renderTMDbPanelWithNotice(w, r, "", "API key saved and validated - it's working.")
}

func (h *AdminHandler) tvdbPanel(w http.ResponseWriter, r *http.Request) {
	h.renderTVDbPanel(w, r, "")
}

func (h *AdminHandler) renderTVDbPanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	apiKey, _, _ := db.GetAdminConfig(h.DB, configKeyTVDbAPIKey)
	h.Tmpl.RenderPartial(w, "partials/admin_tvdb_panel.html", map[string]any{
		"Error":  errMsg,
		"APIKey": apiKey,
	})
}

func (h *AdminHandler) saveTVDbConfig(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	_ = r.ParseForm()
	apiKey := strings.TrimSpace(r.FormValue("apiKey"))
	if err := db.SetAdminConfig(h.DB, configKeyTVDbAPIKey, apiKey); err != nil {
		slog.Error("failed to save TVDb config", "error", err, "adminId", admin.ID)
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save TVDb API key", err.Error(), notifications.StatusError, nil)
		h.renderTVDbPanel(w, r, err.Error())
		return
	}
	h.Notifications.Add(admin.ID, notifications.TypeAction, "Save TVDb API key", "Saved", notifications.StatusSuccess, nil)
	h.renderTVDbPanel(w, r, "")
}

func (h *AdminHandler) traktPanel(w http.ResponseWriter, r *http.Request) {
	h.renderTraktPanel(w, r, "")
}

func (h *AdminHandler) renderTraktPanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.renderTraktPanelWithNotice(w, r, errMsg, "")
}

func (h *AdminHandler) renderTraktPanelWithNotice(w http.ResponseWriter, r *http.Request, errMsg, notice string) {
	apiKey, _, _ := db.GetAdminConfig(h.DB, configKeyTraktAPIKey)
	h.Tmpl.RenderPartial(w, "partials/admin_trakt_panel.html", map[string]any{
		"Error":  errMsg,
		"Notice": notice,
		"APIKey": apiKey,
	})
}

// saveTraktConfig mirrors saveTMDbConfig: persist, then immediately test the
// Client ID against Trakt's own API (trakt.Client.ValidateKey) so a bad/
// mistyped id is caught at save time.
func (h *AdminHandler) saveTraktConfig(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	_ = r.ParseForm()
	apiKey := strings.TrimSpace(r.FormValue("apiKey"))
	if err := db.SetAdminConfig(h.DB, configKeyTraktAPIKey, apiKey); err != nil {
		slog.Error("failed to save Trakt config", "error", err, "adminId", admin.ID)
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save Trakt Client ID", err.Error(), notifications.StatusError, nil)
		h.renderTraktPanel(w, r, err.Error())
		return
	}
	if apiKey == "" {
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save Trakt Client ID", "Cleared", notifications.StatusSuccess, nil)
		h.renderTraktPanelWithNotice(w, r, "", "Client ID cleared.")
		return
	}
	if err := trakt.NewClient(apiKey).ValidateKey(); err != nil {
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save Trakt Client ID", "Saved, but failed validation: "+err.Error(), notifications.StatusError, nil)
		h.renderTraktPanelWithNotice(w, r, "Saved, but this Client ID doesn't look valid: "+err.Error(), "")
		return
	}
	h.Notifications.Add(admin.ID, notifications.TypeAction, "Save Trakt Client ID", "Saved and validated", notifications.StatusSuccess, nil)
	h.renderTraktPanelWithNotice(w, r, "", "Client ID saved and validated - it's working.")
}

func (h *AdminHandler) youtubePanel(w http.ResponseWriter, r *http.Request) {
	h.renderYouTubePanel(w, r, "")
}

func (h *AdminHandler) renderYouTubePanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	oauth := h.YouTube.OAuthConfig()
	h.Tmpl.RenderPartial(w, "partials/admin_youtube_panel.html", map[string]any{
		"Error":               errMsg,
		"YouTubeClientID":     oauth.ClientID,
		"YouTubeClientSecret": oauth.ClientSecret,
		"YouTubeRedirectURI":  oauth.RedirectURI,
		"YouTubeConfigured":   oauth.IsConfigured(),
	})
}

func userIDParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "userID"), 10, 64)
}

// targetUsername looks up userID's Plex username for a notification's
// detail line, falling back to a bare id if the lookup fails (e.g. the
// user row is already gone by the time deleteUser notifies).
func (h *AdminHandler) targetUsername(userID int64) string {
	if u, err := db.GetUserByID(h.DB, userID); err == nil && u != nil {
		return u.PlexUsername
	}
	return fmt.Sprintf("user #%d", userID)
}

// notifyUserAction posts a bell entry for one of the five user-management
// actions below, success or failure - these are instant DB writes with no
// other on-screen confirmation once the panel re-renders, so this is the
// only record an admin (or a teammate checking the bell later) has of who
// did what to which account.
func (h *AdminHandler) notifyUserAction(adminID int64, title, username string, err error) {
	status, detail := notifications.StatusSuccess, username
	if err != nil {
		status, detail = notifications.StatusError, err.Error()
		slog.Error("admin user action failed", "action", title, "adminId", adminID, "error", err)
	}
	h.Notifications.Add(adminID, notifications.TypeAction, title, detail, status, nil)
}

func (h *AdminHandler) enableUser(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	userID, err := userIDParam(r)
	if err != nil {
		h.renderUsersPanel(w, r, "Invalid user ID")
		return
	}
	username := h.targetUsername(userID)
	if err := db.EnableUser(h.DB, userID); err != nil {
		h.notifyUserAction(admin.ID, "Enable user", username, err)
		h.renderUsersPanel(w, r, err.Error())
		return
	}
	// Auto-assign the admin's own server config if the user doesn't have one
	// yet, same as login-time auto-approval (auth.go's reverifyMembership) -
	// ports admin.ts's enable route so a manually-enabled user doesn't have
	// to run the server/library picker themselves.
	if admin != nil {
		if servers, _ := db.GetUserServers(h.DB, userID); len(servers) == 0 {
			_ = db.CopyServerConfig(h.DB, admin.ID, userID)
		}
	}
	h.notifyUserAction(admin.ID, "Enable user", username, nil)
	h.renderUsersPanel(w, r, "")
}

func (h *AdminHandler) disableUser(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	userID, err := userIDParam(r)
	if err != nil {
		h.renderUsersPanel(w, r, "Invalid user ID")
		return
	}
	if admin != nil && admin.ID == userID {
		h.renderUsersPanel(w, r, "Cannot disable your own account")
		return
	}
	username := h.targetUsername(userID)
	if err := db.DisableUser(h.DB, userID); err != nil {
		h.notifyUserAction(admin.ID, "Disable user", username, err)
		h.renderUsersPanel(w, r, err.Error())
		return
	}
	h.notifyUserAction(admin.ID, "Disable user", username, nil)
	h.renderUsersPanel(w, r, "")
}

func (h *AdminHandler) promoteUser(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	userID, err := userIDParam(r)
	if err != nil {
		h.renderUsersPanel(w, r, "Invalid user ID")
		return
	}
	username := h.targetUsername(userID)
	if err := db.AddAdmin(h.DB, userID); err != nil {
		h.notifyUserAction(admin.ID, "Promote to admin", username, err)
		h.renderUsersPanel(w, r, err.Error())
		return
	}
	h.notifyUserAction(admin.ID, "Promote to admin", username, nil)
	h.renderUsersPanel(w, r, "")
}

func (h *AdminHandler) demoteUser(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	userID, err := userIDParam(r)
	if err != nil {
		h.renderUsersPanel(w, r, "Invalid user ID")
		return
	}
	if admin != nil && admin.ID == userID {
		h.renderUsersPanel(w, r, "Cannot revoke your own admin access")
		return
	}
	username := h.targetUsername(userID)
	if err := db.RemoveAdmin(h.DB, userID); err != nil {
		h.notifyUserAction(admin.ID, "Revoke admin", username, err)
		h.renderUsersPanel(w, r, err.Error())
		return
	}
	h.notifyUserAction(admin.ID, "Revoke admin", username, nil)
	h.renderUsersPanel(w, r, "")
}

func (h *AdminHandler) deleteUser(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	userID, err := userIDParam(r)
	if err != nil {
		h.renderUsersPanel(w, r, "Invalid user ID")
		return
	}
	if admin != nil && admin.ID == userID {
		h.renderUsersPanel(w, r, "Cannot delete your own account")
		return
	}
	username := h.targetUsername(userID)
	if err := db.DeleteUser(h.DB, userID); err != nil {
		h.notifyUserAction(admin.ID, "Delete user", username, err)
		h.renderUsersPanel(w, r, err.Error())
		return
	}
	h.notifyUserAction(admin.ID, "Delete user", username, nil)
	h.renderUsersPanel(w, r, "")
}

// queueDeemixForStat ports admin.ts's queueDeemixForStat: search deemix for
// one aggregated "most common missing track" row and queue everything that
// clears the match gate. "Various Artists" is a compilation placeholder,
// not a real performer, so those rows are searched by title alone and the
// top few matches are all queued, since one of several artists who
// actually recorded a same-titled track is far more likely to be right
// than a single guess.
func (h *AdminHandler) queueDeemixForStat(adminID int64, title, artist string) int {
	isVariousArtists := strings.EqualFold(strings.TrimSpace(artist), "various artists")
	limit := 1
	searchArtist := artist
	if isVariousArtists {
		limit = 5
		searchArtist = ""
	}
	settings := h.matchingSettings(adminID)
	matches, err := h.Deemix.FindBestMatches(title, searchArtist, settings, limit)
	if err != nil || len(matches) == 0 {
		return 0
	}
	queued := 0
	for _, m := range matches {
		downloadURL := h.Deemix.ResolveDownloadURL(m.Match)
		isFullAlbum := downloadURL != m.Match.Link
		q, err := h.Deemix.QueueDownload(downloadURL, m.Match.Link)
		if err != nil {
			continue
		}
		detail := m.Match.Artist.Name
		if isFullAlbum {
			detail += " · full album"
		}
		detail += fmt.Sprintf(" · %d%% match", int(m.Score+0.5))
		h.Deemix.StartDownload(deemixsvc.DownloadRequest{
			UserID: adminID, Title: title, Detail: detail, UUID: q.UUID, AlreadyQueued: q.AlreadyQueued,
		})
		queued++
	}
	return queued
}

func (h *AdminHandler) matchingSettings(userID int64) matching.Settings {
	raw, _ := db.GetMatchingSettingsJSON(h.DB, userID)
	return matching.SettingsFromJSON(raw)
}

// deemixDownload ports POST /api/admin/missing/deemix-download: search
// deemix for one aggregated missing-track row (identified by title/artist,
// not a per-user missing_track id, since this stat is aggregated across
// every user) and queue it for download.
func (h *AdminHandler) deemixDownload(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	_ = r.ParseForm()
	title := strings.TrimSpace(r.FormValue("title"))
	artist := r.FormValue("artist")
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}

	h.Queue.Enqueue(admin.ID, "Deemix search: "+title, notifications.TypeDeemix, func(notificationID string) error {
		queued := h.queueDeemixForStat(admin.ID, title, artist)
		if queued == 0 {
			status, detail := notifications.StatusError, fmt.Sprintf("No deemix match good enough for %q", title)
			h.Notifications.Update(admin.ID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
			return nil
		}
		status := notifications.StatusSuccess
		detail := fmt.Sprintf("Queued %d download(s)", queued)
		h.Notifications.Update(admin.ID, notificationID, notifications.Patch{Status: &status, Detail: &detail})
		return nil
	})

	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{"Notifications": h.Notifications.List(admin.ID)})
}

// deemixAll ports POST /api/admin/missing/deemix-all: queues a whole list
// of "most common missing track" rows as one job, so the notification bell
// stays "in progress" until every search really is done instead of one
// misleadingly-instant "queued N" toast.
func (h *AdminHandler) deemixAll(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	stats, err := db.GetMissingTrackStats(h.DB)
	if err != nil || len(stats) == 0 {
		http.Error(w, "no missing tracks to queue", http.StatusBadRequest)
		return
	}

	h.Queue.Enqueue(admin.ID, fmt.Sprintf("Deemix search: %d track(s)", len(stats)), notifications.TypeDeemix, func(notificationID string) error {
		queued, unmatched := 0, 0
		for i, s := range stats {
			pct := i * 100 / len(stats)
			detail := fmt.Sprintf("Searching deemix - %d of %d: %s - %s", i+1, len(stats), s.Artist, s.Title)
			h.Notifications.Update(admin.ID, notificationID, notifications.Patch{Progress: &pct, Detail: &detail})

			count := h.queueDeemixForStat(admin.ID, s.Title, s.Artist)
			if count > 0 {
				queued += count
			} else {
				unmatched++
			}
		}
		status := notifications.StatusSuccess
		detail := fmt.Sprintf("Queued %d download(s) from %d track(s), %d unmatched", queued, len(stats), unmatched)
		pct := 100
		h.Notifications.Update(admin.ID, notificationID, notifications.Patch{Status: &status, Progress: &pct, Detail: &detail})
		return nil
	})

	h.Tmpl.RenderPartial(w, "partials/notifications.html", map[string]any{"Notifications": h.Notifications.List(admin.ID)})
}

// saveDeemixArl ports PUT /api/admin/deemix-arl: persists the Deezer ARL
// deemix-server logs in with, drops the cached session so the next download
// uses it, and - same as the original - tests it immediately so a mistyped
// or already-expired ARL is caught here instead of by the next user who
// clicks Deemix.
func (h *AdminHandler) saveDeemixArl(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	_ = r.ParseForm()
	arl := strings.TrimSpace(r.FormValue("arl"))
	if err := db.SetAdminConfig(h.DB, configKeyDeemixArl, arl); err != nil {
		slog.Error("failed to save deemix ARL", "error", err, "adminId", admin.ID)
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save Deezer ARL", err.Error(), notifications.StatusError, nil)
		h.renderDeemixPanel(w, r, err.Error())
		return
	}
	h.Deemix.SetARL(arl)
	h.notifyArlCheck(admin.ID, "Save Deezer ARL", h.Deemix.CheckArl())
	h.renderDeemixPanel(w, r, "")
}

// checkDeemixArl ports POST /api/admin/deemix-arl/check: re-tests the
// configured ARL against deemix-server right now, rather than waiting for
// the daily job.
func (h *AdminHandler) checkDeemixArl(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	h.notifyArlCheck(admin.ID, "Check Deezer ARL", h.Deemix.CheckArl())
	h.renderDeemixPanel(w, r, "")
}

// notifyArlCheck posts the outcome of a Deezer ARL test (deemix.CheckArl
// already ran, synchronously) as a bell entry.
func (h *AdminHandler) notifyArlCheck(adminID int64, title string, result deemixsvc.ArlCheckResult) {
	status, detail := notifications.StatusSuccess, "ARL is valid"
	if !result.OK {
		status, detail = notifications.StatusError, result.Error
	}
	h.Notifications.Add(adminID, notifications.TypeAction, title, detail, status, nil)
}

// formBool reports whether name was submitted at all - every checkbox in
// admin_deemix_panel.html has value="true" when checked and is simply
// absent from the POST body when unchecked, the standard HTML forms
// behavior, so presence (not the value) is what matters.
func formBool(r *http.Request, name string) bool {
	return r.PostFormValue(name) != ""
}

func formInt(r *http.Request, name string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue(name)))
	if err != nil {
		return fallback
	}
	return v
}

// parseDeemixSettingsForm reads every field admin_deemix_panel.html's
// settings form submits into a deemix.Settings - field-for-field, not
// reflection, so a typo'd `name=` attribute fails obviously (the field
// keeps its old value) rather than silently.
func parseDeemixSettingsForm(r *http.Request) deemixsvc.Settings {
	f := r.PostFormValue
	return deemixsvc.Settings{
		DownloadLocation:          strings.TrimSpace(f("downloadLocation")),
		TracknameTemplate:         f("tracknameTemplate"),
		AlbumTracknameTemplate:    f("albumTracknameTemplate"),
		PlaylistTracknameTemplate: f("playlistTracknameTemplate"),
		CreatePlaylistFolder:      formBool(r, "createPlaylistFolder"),
		PlaylistNameTemplate:      f("playlistNameTemplate"),
		CreateArtistFolder:        formBool(r, "createArtistFolder"),
		ArtistNameTemplate:        f("artistNameTemplate"),
		CreateAlbumFolder:         formBool(r, "createAlbumFolder"),
		AlbumNameTemplate:         f("albumNameTemplate"),
		CreateCDFolder:            formBool(r, "createCDFolder"),
		CreateStructurePlaylist:   formBool(r, "createStructurePlaylist"),
		CreateSingleFolder:        formBool(r, "createSingleFolder"),
		PadTracks:                 formBool(r, "padTracks"),
		PaddingSize:               f("paddingSize"),
		IllegalCharacterReplacer:  f("illegalCharacterReplacer"),
		QueueConcurrency:          formInt(r, "queueConcurrency", 3),
		MaxBitrate:                f("maxBitrate"),
		FeelingLucky:              formBool(r, "feelingLucky"),
		FallbackBitrate:           formBool(r, "fallbackBitrate"),
		FallbackSearch:            formBool(r, "fallbackSearch"),
		FallbackISRC:              formBool(r, "fallbackISRC"),
		LogErrors:                 formBool(r, "logErrors"),
		LogSearched:               formBool(r, "logSearched"),
		OverwriteFile:             f("overwriteFile"),
		CreateM3U8File:            formBool(r, "createM3U8File"),
		PlaylistFilenameTemplate:  f("playlistFilenameTemplate"),
		EmbeddedArtworkSize:       formInt(r, "embeddedArtworkSize", 800),
		EmbeddedArtworkPNG:        formBool(r, "embeddedArtworkPNG"),
		LocalArtworkSize:          formInt(r, "localArtworkSize", 1200),
		LocalArtworkFormat:        f("localArtworkFormat"),
		SaveArtwork:               formBool(r, "saveArtwork"),
		CoverImageTemplate:        f("coverImageTemplate"),
		SaveArtworkArtist:         formBool(r, "saveArtworkArtist"),
		ArtistImageTemplate:       f("artistImageTemplate"),
		JpegImageQuality:          formInt(r, "jpegImageQuality", 90),
		DateFormat:                f("dateFormat"),
		AlbumVariousArtists:       formBool(r, "albumVariousArtists"),
		RemoveAlbumVersion:        formBool(r, "removeAlbumVersion"),
		RemoveDuplicateArtists:    formBool(r, "removeDuplicateArtists"),
		FeaturedToTitle:           f("featuredToTitle"),
		TitleCasing:               f("titleCasing"),
		ArtistCasing:              f("artistCasing"),
		ExecuteCommand:            f("executeCommand"),
		Tags: deemixsvc.TagSettings{
			Title:                     formBool(r, "tags.title"),
			Artist:                    formBool(r, "tags.artist"),
			Artists:                   formBool(r, "tags.artists"),
			Album:                     formBool(r, "tags.album"),
			Cover:                     formBool(r, "tags.cover"),
			TrackNumber:               formBool(r, "tags.trackNumber"),
			TrackTotal:                formBool(r, "tags.trackTotal"),
			DiscNumber:                formBool(r, "tags.discNumber"),
			DiscTotal:                 formBool(r, "tags.discTotal"),
			AlbumArtist:               formBool(r, "tags.albumArtist"),
			Genre:                     formBool(r, "tags.genre"),
			Year:                      formBool(r, "tags.year"),
			Date:                      formBool(r, "tags.date"),
			Explicit:                  formBool(r, "tags.explicit"),
			ISRC:                      formBool(r, "tags.isrc"),
			Length:                    formBool(r, "tags.length"),
			Barcode:                   formBool(r, "tags.barcode"),
			BPM:                       formBool(r, "tags.bpm"),
			ReplayGain:                formBool(r, "tags.replayGain"),
			Label:                     formBool(r, "tags.label"),
			Lyrics:                    formBool(r, "tags.lyrics"),
			SyncedLyrics:              formBool(r, "tags.syncedLyrics"),
			Copyright:                 formBool(r, "tags.copyright"),
			Composer:                  formBool(r, "tags.composer"),
			InvolvedPeople:            formBool(r, "tags.involvedPeople"),
			Source:                    formBool(r, "tags.source"),
			Rating:                    formBool(r, "tags.rating"),
			SavePlaylistAsCompilation: formBool(r, "tags.savePlaylistAsCompilation"),
			SaveID3v1:                 formBool(r, "tags.saveID3v1"),
			MultiArtistSeparator:      f("tags.multiArtistSeparator"),
			SingleAlbumArtist:         formBool(r, "tags.singleAlbumArtist"),
			CoverDescriptionUTF8:      formBool(r, "tags.coverDescriptionUTF8"),
		},
	}
}

// saveDeemixSettings ports deemix-gui's Settings page save (POST
// /api/admin/deemix-settings didn't exist as a separate Node route - the
// original app's settings lived entirely in deemix-server's own config.json,
// edited through its own webui) into this app's admin panel: the whole
// settings form is one blob, persisted via admin_config same as the ARL,
// and applied immediately via ReloadSettings so a running download picks up
// the very next queued track's new config with no restart.
func (h *AdminHandler) saveDeemixSettings(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	_ = r.ParseForm()
	settings := parseDeemixSettingsForm(r)
	if err := deemixsvc.SaveSettings(h.DB, settings); err != nil {
		slog.Error("failed to save deemix settings", "error", err, "adminId", admin.ID)
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save deemix settings", err.Error(), notifications.StatusError, nil)
		h.renderDeemixPanel(w, r, err.Error())
		return
	}
	h.Deemix.ReloadSettings()
	h.Notifications.Add(admin.ID, notifications.TypeAction, "Save deemix settings", "Saved", notifications.StatusSuccess, nil)
	h.renderDeemixPanelWithNotice(w, r, "", "Settings saved.")
}

// saveLidarrConfig ports PUT /api/admin/lidarr-config: persists the Lidarr
// URL and API key used to find/monitor artists and trigger searches.
func (h *AdminHandler) saveLidarrConfig(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	_ = r.ParseForm()
	lidarrURL := strings.TrimSuffix(strings.TrimSpace(r.FormValue("url")), "/")
	apiKey := strings.TrimSpace(r.FormValue("apiKey"))
	if err := db.SetAdminConfig(h.DB, configKeyLidarrURL, lidarrURL); err != nil {
		slog.Error("failed to save Lidarr config", "error", err, "adminId", admin.ID)
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save Lidarr config", err.Error(), notifications.StatusError, nil)
		h.renderLidarrPanel(w, r, err.Error())
		return
	}
	if err := db.SetAdminConfig(h.DB, configKeyLidarrAPIKey, apiKey); err != nil {
		slog.Error("failed to save Lidarr config", "error", err, "adminId", admin.ID)
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save Lidarr config", err.Error(), notifications.StatusError, nil)
		h.renderLidarrPanel(w, r, err.Error())
		return
	}
	h.Lidarr.SetConfig(lidarrURL, apiKey)
	h.Notifications.Add(admin.ID, notifications.TypeAction, "Save Lidarr config", lidarrURL, notifications.StatusSuccess, nil)
	h.renderLidarrPanel(w, r, "")
}

// saveYouTubeConfig ports routes/youtube-config.ts's POST /credentials:
// saves the Google OAuth client id/secret/redirect URI YouTube import
// matching needs. Unlike the Node version (which wrote these into .env and
// required a restart), they're persisted via admin_config like Deemix/
// Lidarr above and applied to the already-constructed youtube.Target
// in-place, so the change takes effect on the very next OAuth attempt.
func (h *AdminHandler) saveYouTubeConfig(w http.ResponseWriter, r *http.Request) {
	admin := auth.CurrentUser(r)
	_ = r.ParseForm()
	clientID := strings.TrimSpace(r.FormValue("clientId"))
	clientSecret := strings.TrimSpace(r.FormValue("clientSecret"))
	redirectURI := strings.TrimSpace(r.FormValue("redirectUri"))
	if clientID == "" || clientSecret == "" || redirectURI == "" {
		h.renderYouTubePanel(w, r, "Missing required fields: Client ID, Client Secret, Redirect URI")
		return
	}
	if !strings.Contains(clientID, ".apps.googleusercontent.com") {
		h.renderYouTubePanel(w, r, "Invalid Client ID format. Should end with .apps.googleusercontent.com")
		return
	}
	if !strings.Contains(redirectURI, "/cross-import/oauth/youtube/callback") {
		h.renderYouTubePanel(w, r, "Invalid Redirect URI. Should end with /cross-import/oauth/youtube/callback")
		return
	}
	if err := db.SetAdminConfig(h.DB, configKeyYouTubeClientID, clientID); err != nil {
		slog.Error("failed to save YouTube config", "error", err, "adminId", admin.ID)
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save YouTube OAuth config", err.Error(), notifications.StatusError, nil)
		h.renderYouTubePanel(w, r, err.Error())
		return
	}
	if err := db.SetAdminConfig(h.DB, configKeyYouTubeSecret, clientSecret); err != nil {
		slog.Error("failed to save YouTube config", "error", err, "adminId", admin.ID)
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save YouTube OAuth config", err.Error(), notifications.StatusError, nil)
		h.renderYouTubePanel(w, r, err.Error())
		return
	}
	if err := db.SetAdminConfig(h.DB, configKeyYouTubeRedirect, redirectURI); err != nil {
		slog.Error("failed to save YouTube config", "error", err, "adminId", admin.ID)
		h.Notifications.Add(admin.ID, notifications.TypeAction, "Save YouTube OAuth config", err.Error(), notifications.StatusError, nil)
		h.renderYouTubePanel(w, r, err.Error())
		return
	}
	h.YouTube.SetOAuth(youtubetarget.OAuthConfig{ClientID: clientID, ClientSecret: clientSecret, RedirectURI: redirectURI})
	h.Notifications.Add(admin.ID, notifications.TypeAction, "Save YouTube OAuth config", "Saved", notifications.StatusSuccess, nil)
	h.renderYouTubePanel(w, r, "")
}

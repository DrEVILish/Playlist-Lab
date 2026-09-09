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
		r.Post("/admin/users/{userID}/enable", h.enableUser)
		r.Post("/admin/users/{userID}/disable", h.disableUser)
		r.Post("/admin/users/{userID}/promote", h.promoteUser)
		r.Post("/admin/users/{userID}/demote", h.demoteUser)
		r.Post("/admin/users/{userID}/delete", h.deleteUser)
		r.Post("/admin/missing/deemix-download", h.deemixDownload)
		r.Post("/admin/missing/deemix-all", h.deemixAll)
		r.Post("/admin/deemix/arl", h.saveDeemixArl)
		r.Post("/admin/deemix/arl/check", h.checkDeemixArl)
		r.Post("/admin/lidarr/config", h.saveLidarrConfig)
		r.Post("/admin/youtube/config", h.saveYouTubeConfig)
	})
}

// statsPanel backs the Statistics tab's lazy-loaded (hx-trigger="revealed")
// fragment - see missingList's comment for why these all load on reveal
// rather than with the page.
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

	h.Tmpl.RenderPartial(w, "partials/admin_stats.html", map[string]any{
		"Stats": map[string]int{
			"TotalUsers":     userCount,
			"ActiveUsers":    activeUsers,
			"TotalPlaylists": playlistCount,
			"TotalMissing":   missingCount,
		},
	})
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
// so the table is always freshly queried rather than patched in place, and
// only that one panel swaps rather than the whole settings page.
func (h *AdminHandler) renderUsersPanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	users, err := db.GetAllUsers(h.DB)
	if err != nil && errMsg == "" {
		errMsg = err.Error()
	}
	h.Tmpl.RenderPartial(w, "partials/admin_users_panel.html", map[string]any{
		"Error": errMsg,
		"Users": users,
	})
}

func (h *AdminHandler) deemixPanel(w http.ResponseWriter, r *http.Request) {
	h.renderDeemixPanel(w, r, "")
}

func (h *AdminHandler) renderDeemixPanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.Tmpl.RenderPartial(w, "partials/admin_deemix_panel.html", map[string]any{
		"Error":     errMsg,
		"DeemixArl": h.Deemix.ARL(),
		"ArlStatus": h.Deemix.LastArlCheck(),
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

func (h *AdminHandler) youtubePanel(w http.ResponseWriter, r *http.Request) {
	h.renderYouTubePanel(w, r, "")
}

func (h *AdminHandler) renderYouTubePanel(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.Tmpl.RenderPartial(w, "partials/admin_youtube_panel.html", map[string]any{
		"Error":               errMsg,
		"YouTubeClientID":     h.YouTube.OAuth.ClientID,
		"YouTubeClientSecret": h.YouTube.OAuth.ClientSecret,
		"YouTubeRedirectURI":  h.YouTube.OAuth.RedirectURI,
		"YouTubeConfigured":   h.YouTube.IsConfigured(),
	})
}

func userIDParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "userID"), 10, 64)
}

func (h *AdminHandler) enableUser(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDParam(r)
	if err != nil {
		h.renderUsersPanel(w, r, "Invalid user ID")
		return
	}
	if err := db.EnableUser(h.DB, userID); err != nil {
		h.renderUsersPanel(w, r, err.Error())
		return
	}
	// Auto-assign the admin's own server config if the user doesn't have one
	// yet, same as login-time auto-approval (auth.go's reverifyMembership) -
	// ports admin.ts's enable route so a manually-enabled user doesn't have
	// to run the server/library picker themselves.
	if admin := auth.CurrentUser(r); admin != nil {
		if existing, _ := db.GetUserServer(h.DB, userID); existing == nil {
			_ = db.CopyServerConfig(h.DB, admin.ID, userID)
		}
	}
	h.renderUsersPanel(w, r, "")
}

func (h *AdminHandler) disableUser(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDParam(r)
	if err != nil {
		h.renderUsersPanel(w, r, "Invalid user ID")
		return
	}
	if admin := auth.CurrentUser(r); admin != nil && admin.ID == userID {
		h.renderUsersPanel(w, r, "Cannot disable your own account")
		return
	}
	if err := db.DisableUser(h.DB, userID); err != nil {
		h.renderUsersPanel(w, r, err.Error())
		return
	}
	h.renderUsersPanel(w, r, "")
}

func (h *AdminHandler) promoteUser(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDParam(r)
	if err != nil {
		h.renderUsersPanel(w, r, "Invalid user ID")
		return
	}
	if err := db.AddAdmin(h.DB, userID); err != nil {
		h.renderUsersPanel(w, r, err.Error())
		return
	}
	h.renderUsersPanel(w, r, "")
}

func (h *AdminHandler) demoteUser(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDParam(r)
	if err != nil {
		h.renderUsersPanel(w, r, "Invalid user ID")
		return
	}
	if admin := auth.CurrentUser(r); admin != nil && admin.ID == userID {
		h.renderUsersPanel(w, r, "Cannot revoke your own admin access")
		return
	}
	if err := db.RemoveAdmin(h.DB, userID); err != nil {
		h.renderUsersPanel(w, r, err.Error())
		return
	}
	h.renderUsersPanel(w, r, "")
}

func (h *AdminHandler) deleteUser(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDParam(r)
	if err != nil {
		h.renderUsersPanel(w, r, "Invalid user ID")
		return
	}
	if admin := auth.CurrentUser(r); admin != nil && admin.ID == userID {
		h.renderUsersPanel(w, r, "Cannot delete your own account")
		return
	}
	if err := db.DeleteUser(h.DB, userID); err != nil {
		h.renderUsersPanel(w, r, err.Error())
		return
	}
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
	_ = r.ParseForm()
	arl := strings.TrimSpace(r.FormValue("arl"))
	if err := db.SetAdminConfig(h.DB, configKeyDeemixArl, arl); err != nil {
		h.renderDeemixPanel(w, r, err.Error())
		return
	}
	h.Deemix.SetARL(arl)
	h.Deemix.CheckArl()
	h.renderDeemixPanel(w, r, "")
}

// checkDeemixArl ports POST /api/admin/deemix-arl/check: re-tests the
// configured ARL against deemix-server right now, rather than waiting for
// the daily job.
func (h *AdminHandler) checkDeemixArl(w http.ResponseWriter, r *http.Request) {
	h.Deemix.CheckArl()
	h.renderDeemixPanel(w, r, "")
}

// saveLidarrConfig ports PUT /api/admin/lidarr-config: persists the Lidarr
// URL and API key used to find/monitor artists and trigger searches.
func (h *AdminHandler) saveLidarrConfig(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	lidarrURL := strings.TrimSuffix(strings.TrimSpace(r.FormValue("url")), "/")
	apiKey := strings.TrimSpace(r.FormValue("apiKey"))
	if err := db.SetAdminConfig(h.DB, configKeyLidarrURL, lidarrURL); err != nil {
		h.renderLidarrPanel(w, r, err.Error())
		return
	}
	if err := db.SetAdminConfig(h.DB, configKeyLidarrAPIKey, apiKey); err != nil {
		h.renderLidarrPanel(w, r, err.Error())
		return
	}
	h.Lidarr.SetConfig(lidarrURL, apiKey)
	h.renderLidarrPanel(w, r, "")
}

// saveYouTubeConfig ports routes/youtube-config.ts's POST /credentials:
// saves the Google OAuth client id/secret/redirect URI YouTube import
// matching needs. Unlike the Node version (which wrote these into .env and
// required a restart), they're persisted via admin_config like Deemix/
// Lidarr above and applied to the already-constructed youtube.Target
// in-place, so the change takes effect on the very next OAuth attempt.
func (h *AdminHandler) saveYouTubeConfig(w http.ResponseWriter, r *http.Request) {
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
		h.renderYouTubePanel(w, r, err.Error())
		return
	}
	if err := db.SetAdminConfig(h.DB, configKeyYouTubeSecret, clientSecret); err != nil {
		h.renderYouTubePanel(w, r, err.Error())
		return
	}
	if err := db.SetAdminConfig(h.DB, configKeyYouTubeRedirect, redirectURI); err != nil {
		h.renderYouTubePanel(w, r, err.Error())
		return
	}
	h.YouTube.OAuth = youtubetarget.OAuthConfig{ClientID: clientID, ClientSecret: clientSecret, RedirectURI: redirectURI}
	h.renderYouTubePanel(w, r, "")
}

// Package handlers: admin.go ports the user-management + stats slice of
// routes/admin.ts to HTMX. Deemix/Lidarr server-wide config, the log
// viewer/level control, background-job status, and the all-users schedule
// list are all still TS-only - each is its own separate service integration
// with its own admin UI surface, and porting all of them alongside Settings
// in one pass would have ballooned this well past the "user management +
// server-wide settings" ask; they're a natural follow-up pass each, wired
// through the existing deemixService/lidarrService/scheduler already
// constructed in cmd/server/main.go.
package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/actionqueue"
	deemixsvc "github.com/drevilish/playlist-lab/internal/services/deemix"
	"github.com/drevilish/playlist-lab/internal/services/matching"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
)

type AdminHandler struct {
	DB            *sql.DB
	Tmpl          *Templates
	Notifications *notifications.Store
	Queue         *actionqueue.Queue
	Deemix        *deemixsvc.Service
}

func RegisterAdmin(r chi.Router, mw *auth.Middleware, h *AdminHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Use(mw.RequireAdmin)
		r.Get("/admin", h.page)
		r.Post("/admin/users/{userID}/enable", h.enableUser)
		r.Post("/admin/users/{userID}/disable", h.disableUser)
		r.Post("/admin/users/{userID}/promote", h.promoteUser)
		r.Post("/admin/users/{userID}/demote", h.demoteUser)
		r.Post("/admin/users/{userID}/delete", h.deleteUser)
		r.Post("/admin/missing/deemix-download", h.deemixDownload)
		r.Post("/admin/missing/deemix-all", h.deemixAll)
	})
}

func (h *AdminHandler) page(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "")
}

// render loads the current stats/users list and re-renders the admin page,
// optionally with an error banner from a just-failed action - every action
// handler below funnels back through here so the users table it shows is
// always freshly queried rather than patched in place.
func (h *AdminHandler) render(w http.ResponseWriter, r *http.Request, errMsg string) {
	user := auth.CurrentUser(r)

	userCount, _ := db.GetUserCount(h.DB)
	playlistCount, _ := db.GetPlaylistCount(h.DB)
	missingCount, _ := db.GetMissingTrackCount(h.DB)
	missingStats, _ := db.GetMissingTrackStats(h.DB)
	users, err := db.GetAllUsers(h.DB)
	if err != nil && errMsg == "" {
		errMsg = err.Error()
	}

	thirtyDaysAgo := time.Now().AddDate(0, 0, -30).UnixMilli()
	activeUsers := 0
	for _, u := range users {
		if u.LastLogin >= thirtyDaysAgo {
			activeUsers++
		}
	}

	h.Tmpl.RenderPage(w, "admin", map[string]any{
		"User":  user,
		"Error": errMsg,
		"Stats": map[string]int{
			"TotalUsers":     userCount,
			"ActiveUsers":    activeUsers,
			"TotalPlaylists": playlistCount,
			"TotalMissing":   missingCount,
		},
		"Users":        users,
		"MissingStats": missingStats,
	})
}

func userIDParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "userID"), 10, 64)
}

func (h *AdminHandler) enableUser(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDParam(r)
	if err != nil {
		h.render(w, r, "Invalid user ID")
		return
	}
	if err := db.EnableUser(h.DB, userID); err != nil {
		h.render(w, r, err.Error())
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
	h.render(w, r, "")
}

func (h *AdminHandler) disableUser(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDParam(r)
	if err != nil {
		h.render(w, r, "Invalid user ID")
		return
	}
	if admin := auth.CurrentUser(r); admin != nil && admin.ID == userID {
		h.render(w, r, "Cannot disable your own account")
		return
	}
	if err := db.DisableUser(h.DB, userID); err != nil {
		h.render(w, r, err.Error())
		return
	}
	h.render(w, r, "")
}

func (h *AdminHandler) promoteUser(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDParam(r)
	if err != nil {
		h.render(w, r, "Invalid user ID")
		return
	}
	if err := db.AddAdmin(h.DB, userID); err != nil {
		h.render(w, r, err.Error())
		return
	}
	h.render(w, r, "")
}

func (h *AdminHandler) demoteUser(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDParam(r)
	if err != nil {
		h.render(w, r, "Invalid user ID")
		return
	}
	if admin := auth.CurrentUser(r); admin != nil && admin.ID == userID {
		h.render(w, r, "Cannot revoke your own admin access")
		return
	}
	if err := db.RemoveAdmin(h.DB, userID); err != nil {
		h.render(w, r, err.Error())
		return
	}
	h.render(w, r, "")
}

func (h *AdminHandler) deleteUser(w http.ResponseWriter, r *http.Request) {
	userID, err := userIDParam(r)
	if err != nil {
		h.render(w, r, "Invalid user ID")
		return
	}
	if admin := auth.CurrentUser(r); admin != nil && admin.ID == userID {
		h.render(w, r, "Cannot delete your own account")
		return
	}
	if err := db.DeleteUser(h.DB, userID); err != nil {
		h.render(w, r, err.Error())
		return
	}
	h.render(w, r, "")
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

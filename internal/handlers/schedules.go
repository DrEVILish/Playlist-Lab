// Package handlers: schedules.go ports routes/schedules.ts to HTMX, scoped
// to the ScheduleModal.tsx workflow (one schedule per playlist, created/
// managed from the playlist row) - the former standalone Schedules page and
// its bulk history views (GET /executions/recent, /executions/running,
// DELETE /executions/:id) aren't ported since nothing in this server's UI
// links to them anymore (see ScheduleModal.tsx's own doc comment: mix-
// generation schedules aren't creatable from this UI either). Actually
// running a schedule (due or manual) is scheduler.Run/RunDue, wired into the
// cron scheduler in cmd/server/main.go - this file only owns CRUD + the
// manual "Run Now"/"Run All" triggers, which call scheduler.Run in the
// background (fire-and-forget) same as the TS route, but - unlike an
// earlier version of this file - also wrap it with a notification bell
// entry (runSingleSchedule's addNotification/updateNotification in
// schedule-checker-job.ts), so a manual run's success/failure is visible
// instead of only ever reaching the server log. scheduler.Run itself stays
// notification-free since RunDue (the unattended cron path) reuses it too,
// and the original never notifies for those automatic runs either -
// runSingleSchedule is a manual-only wrapper in the TS version, not
// something the cron job also goes through.
package handlers

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/services/notifications"
	"github.com/drevilish/playlist-lab/internal/services/scheduler"
)

type SchedulesHandler struct {
	DB            *sql.DB
	Tmpl          *Templates
	Deps          scheduler.Deps
	Notifications *notifications.Store
}

// scheduleDisplayName is a best-effort notification title for a schedule -
// ports scheduleDisplayName() in schedule-checker-job.ts. Doesn't need to
// match the executor's own playlist-name resolution exactly, just be
// recognizable to the user who triggered it.
func (h *SchedulesHandler) scheduleDisplayName(s db.Schedule) string {
	if s.PlaylistID.Valid {
		if playlist, err := db.GetPlaylistByID(h.DB, s.PlaylistID.Int64); err == nil && playlist != nil {
			return playlist.Name
		}
	}
	if s.CollectionID.Valid {
		if coll, err := db.GetCollectionByID(h.DB, s.CollectionID.Int64); err == nil && coll != nil {
			return coll.Name
		}
	}
	if name := s.ParsedConfig().PlaylistName; name != "" {
		return name
	}
	if s.ScheduleType == "mix_generation" {
		return "Scheduled mix"
	}
	if s.ScheduleType == "collection_refresh" {
		return "Scheduled collection refresh"
	}
	return "Scheduled playlist refresh"
}

// runScheduleWithNotification wraps one scheduler.Run call with a
// notification bell entry, ponytail: start/success/error only, not the
// original's per-track "Fetching tracks.../Matching..." live progress -
// that needs a progress callback threaded through scheduler.Run and
// executePlaylistRefresh, which RunDue's unattended cron callers have no use
// for; add it if a manual run's silence mid-refresh becomes a real complaint.
func (h *SchedulesHandler) runScheduleWithNotification(s db.Schedule) {
	startDetail := "Starting..."
	if s.ScheduleType == "mix_generation" {
		startDetail = "Generating mix"
	}
	notification := h.Notifications.Add(s.UserID, notifications.TypeSchedule, h.scheduleDisplayName(s), startDetail, notifications.StatusInProgress, nil)

	if err := scheduler.Run(h.Deps, s); err != nil {
		slog.Error("manual schedule run failed", "scheduleId", s.ID, "error", err)
		status := notifications.StatusError
		detail := err.Error()
		h.Notifications.Update(s.UserID, notification.ID, notifications.Patch{Status: &status, Detail: &detail})
		return
	}
	status, progress := notifications.StatusSuccess, 100
	detail := "Refreshed"
	if s.ScheduleType == "mix_generation" {
		detail = "Generated"
	}
	h.Notifications.Update(s.UserID, notification.ID, notifications.Patch{Status: &status, Progress: &progress, Detail: &detail})
}

func RegisterSchedules(r chi.Router, mw *auth.Middleware, h *SchedulesHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/playlists/{plexId}/schedule", h.form)
		// DESIGN.md §11.11: Collections reuses this same modal/handler set
		// (create/update/delete/run are already schedule-type-agnostic) -
		// only the "open the form" step needs a collection-specific loader,
		// since a collection has no plexId to look up by. No RequirePlexOwner
		// here: a schedule can only ever point at a collection_id that
		// belongs to an owner-created collection in the first place, so the
		// ownedCollection/ownedSchedule checks below are already sufficient.
		r.Get("/collections/{id}/schedule", h.formForCollection)
		r.Post("/schedules", h.create)
		r.Put("/schedules/{id}", h.update)
		r.Delete("/schedules/{id}", h.delete)
		r.Post("/schedules/{id}/run", h.runOne)
		r.Post("/schedules/run-all", h.runAll)
	})
}

// scheduleView is what schedule_form.html renders - Schedule plus the bits
// the template needs but Schedule itself doesn't carry (parsed config,
// execution history, the playlist it's attached to).
type scheduleView struct {
	*db.Schedule
	Config     db.ScheduleConfig
	Executions []db.ScheduleExecution
}

// loadPlaylist resolves plexId (from the URL) to the current user's tracked
// playlist row - schedules only ever attach to a row already created by
// import/mix generation (see scheduler package doc), so an untracked Plex
// playlist has nothing to schedule against yet.
func (h *SchedulesHandler) loadPlaylist(r *http.Request) (*db.Playlist, error) {
	user := auth.CurrentUser(r)
	plexID := chi.URLParam(r, "plexId")
	return db.GetPlaylistByPlexID(h.DB, user.ID, plexID)
}

// loadCollection resolves the {id} URL param to the current user's own
// collection - mirrors loadPlaylist for the Collections page's Schedule
// button (DESIGN.md §11.11).
func (h *SchedulesHandler) loadCollection(r *http.Request) (*db.Collection, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return nil, nil
	}
	coll, err := db.GetCollectionByID(h.DB, id)
	if err != nil || coll == nil {
		return nil, err
	}
	if coll.UserID != auth.CurrentUser(r).ID {
		return nil, nil
	}
	return coll, nil
}

// form renders the create/manage modal (ScheduleModal.tsx port) for one
// playlist - "Create Schedule" if none exists yet for it, "Manage Schedule"
// otherwise.
func (h *SchedulesHandler) form(w http.ResponseWriter, r *http.Request) {
	playlist, err := h.loadPlaylist(r)
	if err != nil || playlist == nil {
		http.Error(w, "Playlist must be imported/tracked before it can be scheduled.", http.StatusBadRequest)
		return
	}

	data := map[string]any{"PlaylistID": playlist.ID, "PlaylistName": playlist.Name}
	sched, err := db.GetScheduleByPlaylistID(h.DB, playlist.ID)
	if err != nil {
		slog.Error("failed to load schedule", "error", err)
	}
	if sched != nil {
		executions, _ := db.GetScheduleExecutions(h.DB, sched.ID, 10)
		data["Schedule"] = scheduleView{Schedule: sched, Config: sched.ParsedConfig(), Executions: executions}
	}
	h.Tmpl.RenderPartial(w, "partials/schedule_form.html", data)
}

// formForCollection is form's Collections-page sibling - a collection has
// no plexId to look up an existing schedule by, so it goes via
// schedule.collection_id instead. Collections has no GetScheduleByPlaylistID
// analog since a collection can have at most one schedule the same way a
// playlist can; reusing db.GetUserSchedules and filtering is overkill for
// that, so this queries directly.
func (h *SchedulesHandler) formForCollection(w http.ResponseWriter, r *http.Request) {
	coll, err := h.loadCollection(r)
	if err != nil || coll == nil {
		http.Error(w, "Collection not found", http.StatusNotFound)
		return
	}
	data := map[string]any{"CollectionID": coll.ID, "CollectionName": coll.Name}
	sched, err := db.GetScheduleByCollectionID(h.DB, coll.ID)
	if err != nil {
		slog.Error("failed to load schedule", "error", err)
	}
	if sched != nil {
		executions, _ := db.GetScheduleExecutions(h.DB, sched.ID, 10)
		data["Schedule"] = scheduleView{Schedule: sched, Config: sched.ParsedConfig(), Executions: executions}
	}
	h.Tmpl.RenderPartial(w, "partials/schedule_form.html", data)
}

// closeModalAndRefresh answers a schedule mutation for a playlist-linked
// schedule - HX-Redirect fully reloads the Playlists page, which is already
// how every other playlist-row mutation in this app refreshes the table.
func closeModalAndRefresh(w http.ResponseWriter) {
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

// closeModalAndNotifyCollections is closeModalAndRefresh's Collections-page
// counterpart: a full-page HX-Redirect would navigate away from the
// Collections page entirely (its URL is /collections, not /), so instead
// this fires an HX-Trigger event the page's own #collections-list panel
// (hx-trigger="load, collections-changed from:body") is already listening
// for, refreshing just that panel in place. The modal itself closes via
// schedule_form.html's own hx-on::after-request handler either way.
func closeModalAndNotifyCollections(w http.ResponseWriter) {
	w.Header().Set("HX-Trigger", "collections-changed")
	w.WriteHeader(http.StatusOK)
}

func configJSON(r *http.Request) string {
	cfg := db.ScheduleConfig{
		RunTime:    r.FormValue("runTime"),
		UpdateMode: r.FormValue("updateMode"),
	}
	if cfg.UpdateMode == "" {
		cfg.UpdateMode = "replace"
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

// create ports POST /api/schedules. Originally scoped to playlist_refresh
// (the only type ScheduleModal.tsx could produce); now also accepts a
// collectionId form field for collection_refresh schedules (DESIGN.md
// §11.11), sharing this same handler since everything past "which id was
// submitted" is already identical between the two.
func (h *SchedulesHandler) create(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	playlistID, _ := strconv.ParseInt(r.FormValue("playlistId"), 10, 64)
	collectionID, _ := strconv.ParseInt(r.FormValue("collectionId"), 10, 64)
	frequency := r.FormValue("frequency")
	startDate := r.FormValue("startDate")
	if (playlistID == 0 && collectionID == 0) || frequency == "" || startDate == "" {
		http.Error(w, "playlistId (or collectionId), frequency and startDate are required", http.StatusBadRequest)
		return
	}

	scheduleType := "playlist_refresh"
	if collectionID != 0 {
		scheduleType = "collection_refresh"
	}
	if _, err := db.CreateSchedule(h.DB, user.ID, playlistID, collectionID, scheduleType, frequency, startDate, configJSON(r)); err != nil {
		slog.Error("failed to create schedule", "error", err)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Create schedule", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to create schedule", http.StatusInternalServerError)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Create schedule", frequency, notifications.StatusSuccess, nil)
	if collectionID != 0 {
		closeModalAndNotifyCollections(w)
		return
	}
	closeModalAndRefresh(w)
}

func (h *SchedulesHandler) update(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	sched, err := h.ownedSchedule(r, id)
	if err != nil || sched == nil {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	frequency := r.FormValue("frequency")
	startDate := r.FormValue("startDate")
	cfg := configJSON(r)
	u := db.ScheduleUpdate{Config: &cfg}
	if frequency != "" {
		u.Frequency = &frequency
	}
	if startDate != "" {
		u.StartDate = &startDate
	}
	if err := db.UpdateSchedule(h.DB, id, u); err != nil {
		slog.Error("failed to update schedule", "error", err)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Update schedule", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to update schedule", http.StatusInternalServerError)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Update schedule", frequency, notifications.StatusSuccess, nil)
	if sched.CollectionID.Valid {
		closeModalAndNotifyCollections(w)
		return
	}
	closeModalAndRefresh(w)
}

func (h *SchedulesHandler) delete(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	sched, err := h.ownedSchedule(r, id)
	if err != nil || sched == nil {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return
	}
	if err := db.DeleteSchedule(h.DB, id); err != nil {
		slog.Error("failed to delete schedule", "error", err)
		h.Notifications.Add(user.ID, notifications.TypeAction, "Delete schedule", err.Error(), notifications.StatusError, nil)
		http.Error(w, "Failed to delete schedule", http.StatusInternalServerError)
		return
	}
	h.Notifications.Add(user.ID, notifications.TypeAction, "Delete schedule", "Deleted", notifications.StatusSuccess, nil)
	if sched.CollectionID.Valid {
		closeModalAndNotifyCollections(w)
		return
	}
	closeModalAndRefresh(w)
}

// ownedSchedule loads a schedule and verifies it belongs to the current
// user, mirroring the 403-on-mismatch check every mutating TS route did.
func (h *SchedulesHandler) ownedSchedule(r *http.Request, id int64) (*db.Schedule, error) {
	sched, err := db.GetScheduleByID(h.DB, id)
	if err != nil || sched == nil {
		return nil, err
	}
	if sched.UserID != auth.CurrentUser(r).ID {
		return nil, nil
	}
	return sched, nil
}

// runOne ports POST /api/schedules/:id/run: fires the schedule now,
// regardless of due-ness, in the background - same fire-and-forget shape as
// the TS route.
func (h *SchedulesHandler) runOne(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid schedule id", http.StatusBadRequest)
		return
	}
	sched, err := h.ownedSchedule(r, id)
	if err != nil || sched == nil {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return
	}
	go h.runScheduleWithNotification(*sched)
	w.WriteHeader(http.StatusAccepted)
}

// runAll ports POST /api/schedules/run-all with the same bounded-concurrency
// worker pool the TS route added after unbounded concurrency froze the
// server (see schedule-checker-job.ts / routes/schedules.ts's
// RUN_ALL_CONCURRENCY comment) - each run does real sequential Plex/matching
// work, so firing every schedule at once still isn't safe here either.
const runAllConcurrency = 3

func (h *SchedulesHandler) runAll(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	schedules, err := db.GetUserSchedules(h.DB, user.ID)
	if err != nil {
		http.Error(w, "failed to load schedules", http.StatusInternalServerError)
		return
	}
	go func() {
		sem := make(chan struct{}, runAllConcurrency)
		for _, s := range schedules {
			sem <- struct{}{}
			go func(s db.Schedule) {
				defer func() { <-sem }()
				h.runScheduleWithNotification(s)
			}(s)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

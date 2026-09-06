// Package handlers: schedules.go ports routes/schedules.ts to HTMX, scoped
// to the ScheduleModal.tsx workflow (one schedule per playlist, created/
// managed from the playlist row) - the former standalone Schedules page and
// its bulk history views (GET /executions/recent, /executions/running,
// DELETE /executions/:id) aren't ported since nothing in this server's UI
// links to them anymore (see ScheduleModal.tsx's own doc comment: mix-
// generation schedules aren't creatable from this UI either). Actually
// running a schedule (due or manual) is scheduler.Run/RunDue, wired into the
// cron scheduler in cmd/server/main.go - this file only owns CRUD + the
// manual "Run Now"/"Run All" triggers, which just call scheduler.Run in the
// background the same way the TS route did (fire-and-forget, log on error).
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
	"github.com/drevilish/playlist-lab/internal/services/scheduler"
)

type SchedulesHandler struct {
	DB   *sql.DB
	Tmpl *Templates
	Deps scheduler.Deps
}

func RegisterSchedules(r chi.Router, mw *auth.Middleware, h *SchedulesHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Get("/playlists/{plexId}/schedule", h.form)
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

func closeModalAndRefresh(w http.ResponseWriter) {
	w.Header().Set("HX-Redirect", "/")
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

// create ports POST /api/schedules, scoped to playlist_refresh (the only
// type ScheduleModal.tsx can produce - see package doc).
func (h *SchedulesHandler) create(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	playlistID, _ := strconv.ParseInt(r.FormValue("playlistId"), 10, 64)
	frequency := r.FormValue("frequency")
	startDate := r.FormValue("startDate")
	if playlistID == 0 || frequency == "" || startDate == "" {
		http.Error(w, "playlistId, frequency and startDate are required", http.StatusBadRequest)
		return
	}

	if _, err := db.CreateSchedule(h.DB, user.ID, playlistID, "playlist_refresh", frequency, startDate, configJSON(r)); err != nil {
		slog.Error("failed to create schedule", "error", err)
		http.Error(w, "Failed to create schedule", http.StatusInternalServerError)
		return
	}
	closeModalAndRefresh(w)
}

func (h *SchedulesHandler) update(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "Failed to update schedule", http.StatusInternalServerError)
		return
	}
	closeModalAndRefresh(w)
}

func (h *SchedulesHandler) delete(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "Failed to delete schedule", http.StatusInternalServerError)
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
	go func(s db.Schedule) {
		if err := scheduler.Run(h.Deps, s); err != nil {
			slog.Error("manual schedule run failed", "scheduleId", s.ID, "error", err)
		}
	}(*sched)
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
				if err := scheduler.Run(h.Deps, s); err != nil {
					slog.Error("run-all schedule failed", "scheduleId", s.ID, "error", err)
				}
			}(s)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

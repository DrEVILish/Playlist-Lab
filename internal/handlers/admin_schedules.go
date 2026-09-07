// Admin Schedules tab, ported from routes/admin.ts's GET /jobs and
// GET /schedules. Until now this panel rendered only "Not yet ported:
// background-job status and the all-users schedule list".
package handlers

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/robfig/cron/v3"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
)

type AdminSchedulesHandler struct {
	DB   *sql.DB
	Tmpl *Templates
	Jobs []BackgroundJob
}

// BackgroundJob describes one registered cron job for display. main.go
// builds this list from the same jobs.Config values it registers with
// jobs.Scheduler, since Scheduler itself exposes no status/introspection
// API - jobs.ts's own /jobs route was the same kind of static summary
// (lastRun/currently-running state was never wired there either, per that
// route's own comment), so this matches rather than exceeds what existed.
type BackgroundJob struct {
	Name    string
	Spec    string
	Enabled bool
}

func RegisterAdminSchedules(r chi.Router, mw *auth.Middleware, h *AdminSchedulesHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Use(mw.RequireAdmin)
		r.Get("/admin/schedules", h.list)
	})
}

type jobStatus struct {
	Name    string
	Spec    string
	Enabled bool
	NextRun string
}

// nextRun computes the next fire time for a standard 5-field cron
// expression. cron.ParseStandard understands the same fields v2's
// hand-rolled nextCronRun stepped through minute-by-minute; there is no
// reason to re-implement that search when the scheduler already links a
// production-grade cron parser.
func nextRun(spec string) string {
	sched, err := cron.ParseStandard(spec)
	if err != nil {
		return ""
	}
	return sched.Next(time.Now()).Format("2006-01-02 15:04:05")
}

func (h *AdminSchedulesHandler) list(w http.ResponseWriter, r *http.Request) {
	jobs := make([]jobStatus, len(h.Jobs))
	for i, j := range h.Jobs {
		js := jobStatus{Name: j.Name, Spec: j.Spec, Enabled: j.Enabled}
		if j.Enabled {
			js.NextRun = nextRun(j.Spec)
		}
		jobs[i] = js
	}

	rows, err := db.GetAllSchedules(h.DB)
	if err != nil {
		http.Error(w, "failed to load schedules", http.StatusInternalServerError)
		return
	}
	// Rendered separately from db.AdminSchedule rather than adding a
	// time.Time method there: LastRun is a raw unix column like every other
	// timestamp in this package (Playlist.CreatedAt, User.CreatedAt, ...),
	// and templates' relativeTime helper wants a time.Time - the same
	// conversion admin.html already does inline for ArlStatus.At.
	type scheduleRow struct {
		db.AdminSchedule
		LastRunTime time.Time
	}
	schedules := make([]scheduleRow, len(rows))
	for i, s := range rows {
		row := scheduleRow{AdminSchedule: s}
		if s.LastRun.Valid {
			row.LastRunTime = time.Unix(s.LastRun.Int64, 0)
		}
		schedules[i] = row
	}

	h.Tmpl.RenderPartial(w, "partials/admin_schedules.html", map[string]any{
		"Jobs":      jobs,
		"Schedules": schedules,
	})
}

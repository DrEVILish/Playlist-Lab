// Admin Schedules tab, ported from routes/admin.ts's GET /jobs and
// GET /schedules. Until now this panel rendered only "Not yet ported:
// background-job status and the all-users schedule list".
package handlers

import (
	"database/sql"
	"net/http"
	"sort"
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

	h.Tmpl.RenderPartial(w, "partials/admin_schedules.html", map[string]any{
		"Jobs":     jobs,
		"Timeline": buildScheduleTimeline(rows),
	})
}

// scheduleRow is db.AdminSchedule plus the display-ready fields the
// calendar/timeline view needs (DESIGN.md §11.5: "calendar/timeline view
// rather than a plain next-run list"). Rendered separately from
// db.AdminSchedule rather than adding methods there: LastRun is a raw unix
// column like every other timestamp in this package, and templates stay
// markup-only (see admin_logs.go's parseEntry comment) - all of the
// date arithmetic below happens here, not in the template.
type scheduleRow struct {
	db.AdminSchedule
	LastRunTime  time.Time
	NextRunTime  time.Time // zero if the schedule has never run or has an unrecognized frequency
	NextRunLabel string    // formatted NextRunTime, "" if zero - relativeTime() is past-only, so a future run gets its own label rather than misusing it
}

// scheduleTimelineGroup is one bucket of the timeline (e.g. "Overdue",
// "Today") with the schedules that fall in it, in NextRunTime order. Key is
// a CSS-safe slug of Label (Label itself can contain a space, e.g. "This
// week", which isn't a valid single class token).
type scheduleTimelineGroup struct {
	Label string
	Key   string
	Rows  []scheduleRow
}

// nextScheduleRun projects a schedule's next run from its last run plus its
// own stated frequency - both already-stored fields, not a new tracked
// value. This app's four frequencies are fixed-interval, so this is plain
// date arithmetic; it isn't a general cron parser like nextRun() above
// (background jobs use real cron specs, user schedules don't).
func nextScheduleRun(lastRun time.Time, frequency string) time.Time {
	if lastRun.IsZero() {
		return time.Time{}
	}
	switch frequency {
	case "daily":
		return lastRun.AddDate(0, 0, 1)
	case "weekly":
		return lastRun.AddDate(0, 0, 7)
	case "fortnightly":
		return lastRun.AddDate(0, 0, 14)
	case "monthly":
		return lastRun.AddDate(0, 1, 0)
	default:
		return time.Time{}
	}
}

// buildScheduleTimeline groups every schedule into the timeline buckets the
// Schedules tab renders, ordered soonest-due bucket first and by next run
// within a bucket. Schedules that have never run, or whose frequency this
// app doesn't recognize, land in their own trailing bucket rather than
// being guessed at.
func buildScheduleTimeline(rows []db.AdminSchedule) []scheduleTimelineGroup {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	weekEnd := today.AddDate(0, 0, 7)

	buckets := []string{"Overdue", "Today", "This week", "Later", "Not yet run"}
	bucketKeys := map[string]string{
		"Overdue": "overdue", "Today": "today", "This week": "this-week",
		"Later": "later", "Not yet run": "not-yet-run",
	}
	byBucket := make(map[string][]scheduleRow, len(buckets))

	for _, s := range rows {
		row := scheduleRow{AdminSchedule: s}
		if s.LastRun.Valid {
			row.LastRunTime = time.Unix(s.LastRun.Int64, 0)
		}
		row.NextRunTime = nextScheduleRun(row.LastRunTime, s.Frequency)
		if !row.NextRunTime.IsZero() {
			row.NextRunLabel = row.NextRunTime.Format("2006-01-02 15:04")
		}

		var bucket string
		switch {
		case row.NextRunTime.IsZero():
			bucket = "Not yet run"
		case row.NextRunTime.Before(now):
			bucket = "Overdue"
		case row.NextRunTime.Before(today.AddDate(0, 0, 1)):
			bucket = "Today"
		case row.NextRunTime.Before(weekEnd):
			bucket = "This week"
		default:
			bucket = "Later"
		}
		byBucket[bucket] = append(byBucket[bucket], row)
	}

	groups := make([]scheduleTimelineGroup, 0, len(buckets))
	for _, b := range buckets {
		rows := byBucket[b]
		if len(rows) == 0 {
			continue
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].NextRunTime.Before(rows[j].NextRunTime) })
		groups = append(groups, scheduleTimelineGroup{Label: b, Key: bucketKeys[b], Rows: rows})
	}
	return groups
}

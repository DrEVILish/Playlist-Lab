package jobs

import "github.com/drevilish/playlist-lab/internal/services/scheduler"

// RunScheduleChecker ports schedule-checker-job.ts's runScheduleCheckerJob:
// finds every due schedule and executes it. This is the job that was
// deferred through Phase 4/5 (see main.go's now-removed "isn't ported yet"
// comment) since it needed the import + mix-generation pipelines built in
// Phase 6.
func RunScheduleChecker(d scheduler.Deps) error {
	scheduler.RunDue(d)
	return nil
}

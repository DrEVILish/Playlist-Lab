// Package jobs ports services/jobs.ts's JobScheduler to Go, using
// github.com/robfig/cron/v3 in place of node-cron - cron-expression parsing
// has real edge cases (day-of-week/day-of-month interaction, DST, ...) that
// aren't worth hand-rolling when a mature, widely-used library already
// solves it.
package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

type Config struct {
	Name    string
	Spec    string // standard 5-field cron expression, e.g. "0 2 * * *"
	Handler func() error
	Enabled bool
}

type Scheduler struct {
	cron *cron.Cron
	mu   sync.Mutex
	// isShuttingDown gates handlers rather than merely stopping the
	// underlying cron.Cron, mirroring jobs.ts's own early-return guard -
	// a job already mid-run when Stop() is called is allowed to finish,
	// but no new run starts once shutdown begins.
	isShuttingDown bool
	registered     []Config
}

func NewScheduler() *Scheduler {
	// SkipIfStillRunning is applied per registered job (robfig/cron wraps
	// each AddFunc call independently, so each job's overlap-tracking is
	// its own): without it, a job whose handler runs longer than its own
	// interval (e.g. schedule-checker running past its 10-minute cadence
	// under load) gets a second overlapping invocation fired on top of the
	// first, which for e.g. a first-ever mix-generation schedule race can
	// create the same playlist twice.
	return &Scheduler{cron: cron.New(cron.WithChain(cron.SkipIfStillRunning(slogCronLogger{})))}
}

// slogCronLogger adapts this app's log/slog use to cron.Logger, so a
// skipped overlapping run logs consistently with everything else instead
// of going to a separate stdlib-log stream via cron.DefaultLogger.
type slogCronLogger struct{}

func (slogCronLogger) Info(msg string, keysAndValues ...interface{}) {
	slog.Info("cron: "+msg, keysAndValues...)
}

func (slogCronLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	slog.Error("cron: "+msg, append([]interface{}{"error", err}, keysAndValues...)...)
}

// Register adds a job. Unlike jobs.ts's registerJob (which builds the
// underlying scheduled task immediately), registration here just records
// the config - the actual cron.Cron entries are created in Start(), since
// robfig/cron has no equivalent of node-cron v4's "register now, start
// later" split otherwise.
func (s *Scheduler) Register(cfg Config) {
	if !cfg.Enabled {
		slog.Info("job disabled, skipping registration", "job", cfg.Name)
		return
	}
	s.registered = append(s.registered, cfg)
	slog.Info("registered job", "job", cfg.Name, "schedule", cfg.Spec)
}

func (s *Scheduler) Start() {
	s.mu.Lock()
	s.isShuttingDown = false
	s.mu.Unlock()

	for _, cfg := range s.registered {
		cfg := cfg
		_, err := s.cron.AddFunc(cfg.Spec, func() {
			s.mu.Lock()
			shuttingDown := s.isShuttingDown
			s.mu.Unlock()
			if shuttingDown {
				slog.Info("skipping job - shutdown in progress", "job", cfg.Name)
				return
			}

			start := time.Now()
			slog.Debug("starting job", "job", cfg.Name)
			if err := cfg.Handler(); err != nil {
				slog.Error("job failed", "job", cfg.Name, "error", err)
				return // continue with next scheduled run despite the error
			}
			slog.Debug("job completed", "job", cfg.Name, "duration", time.Since(start))
		})
		if err != nil {
			slog.Error("failed to register job", "job", cfg.Name, "error", err)
			continue
		}
		slog.Info("started job", "job", cfg.Name)
	}
	s.cron.Start()
}

func (s *Scheduler) Stop() context.Context {
	s.mu.Lock()
	s.isShuttingDown = true
	s.mu.Unlock()
	return s.cron.Stop()
}

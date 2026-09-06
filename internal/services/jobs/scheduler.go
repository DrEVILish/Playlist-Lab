// Package jobs ports services/jobs.ts's JobScheduler to Go, using
// github.com/robfig/cron/v3 in place of node-cron - cron-expression parsing
// has real edge cases (day-of-week/day-of-month interaction, DST, ...) that
// aren't worth hand-rolling when a mature, widely-used library already
// solves it.
package jobs

import (
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
	return &Scheduler{cron: cron.New()}
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

func (s *Scheduler) Stop() {
	s.mu.Lock()
	s.isShuttingDown = true
	s.mu.Unlock()
	<-s.cron.Stop().Done() // waits for any in-flight run to finish
	slog.Info("all jobs stopped")
}

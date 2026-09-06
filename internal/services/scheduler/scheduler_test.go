package scheduler

import (
	"database/sql"
	"testing"
	"time"

	"github.com/drevilish/playlist-lab/internal/db"
)

func TestIsDue(t *testing.T) {
	now := time.Date(2026, 9, 5, 14, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		s    db.Schedule
		want bool
	}{
		{
			name: "first run, start date in future",
			s:    db.Schedule{StartDate: "2026-09-10", Frequency: "daily"},
			want: false,
		},
		{
			name: "first run, start date today, no run_time",
			s:    db.Schedule{StartDate: "2026-09-05", Frequency: "daily"},
			want: true,
		},
		{
			name: "first run, run_time already passed today",
			s:    db.Schedule{StartDate: "2026-09-01", Frequency: "daily", Config: sql.NullString{String: `{"run_time":"10:00"}`, Valid: true}},
			want: true,
		},
		{
			name: "first run, run_time later today",
			s:    db.Schedule{StartDate: "2026-09-01", Frequency: "daily", Config: sql.NullString{String: `{"run_time":"18:00"}`, Valid: true}},
			want: false,
		},
		{
			name: "daily frequency not yet met",
			s: db.Schedule{
				Frequency: "daily",
				LastRun:   sql.NullInt64{Int64: now.Add(-12 * time.Hour).Unix(), Valid: true},
			},
			want: false,
		},
		{
			name: "daily frequency met, no run_time",
			s: db.Schedule{
				Frequency: "daily",
				LastRun:   sql.NullInt64{Int64: now.Add(-25 * time.Hour).Unix(), Valid: true},
			},
			want: true,
		},
		{
			name: "weekly frequency met but run_time not reached yet today",
			s: db.Schedule{
				Frequency: "weekly",
				LastRun:   sql.NullInt64{Int64: now.Add(-8 * 24 * time.Hour).Unix(), Valid: true},
				Config:    sql.NullString{String: `{"run_time":"20:00"}`, Valid: true},
			},
			want: false,
		},
		{
			name: "unknown frequency",
			s: db.Schedule{
				Frequency: "hourly",
				LastRun:   sql.NullInt64{Int64: now.Add(-25 * time.Hour).Unix(), Valid: true},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDue(tt.s, now); got != tt.want {
				t.Errorf("IsDue() = %v, want %v", got, tt.want)
			}
		})
	}
}

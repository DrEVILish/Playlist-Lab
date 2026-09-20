package handlers

import (
	"testing"
	"time"
)

func TestNextRun_ComputesUpcomingFireTime(t *testing.T) {
	got := nextRun("0 3 * * 0") // 3:00 AM every Sunday
	if got == "" {
		t.Fatal("nextRun returned empty for a valid spec")
	}
	parsed, err := time.Parse("2006-01-02 15:04:05", got)
	if err != nil {
		t.Fatalf("nextRun result %q did not parse: %v", got, err)
	}
	if parsed.Weekday() != time.Sunday || parsed.Hour() != 3 || parsed.Minute() != 0 {
		t.Errorf("nextRun = %v, want the next Sunday at 03:00", parsed)
	}
	if !parsed.After(time.Now()) {
		t.Errorf("nextRun = %v, want a time in the future", parsed)
	}
}

func TestNextRun_InvalidSpecReturnsEmpty(t *testing.T) {
	if got := nextRun("not a cron spec"); got != "" {
		t.Errorf("nextRun(invalid) = %q, want empty rather than a bogus time", got)
	}
}

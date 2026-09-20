package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"log/slog"
)

func TestSetup_WritesCombinedAndErrorLogs(t *testing.T) {
	dir := t.TempDir()

	closeLogs, err := Setup(dir, "info")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer closeLogs()

	slog.Info("informational line", "k", "v")
	slog.Error("boom", "k", "v2")

	combined, err := os.ReadFile(filepath.Join(dir, combinedLog))
	if err != nil {
		t.Fatalf("reading combined.log: %v", err)
	}
	if !strings.Contains(string(combined), "informational line") {
		t.Error("combined.log missing info-level line")
	}
	if !strings.Contains(string(combined), "boom") {
		t.Error("combined.log missing error-level line")
	}

	errOut, err := os.ReadFile(filepath.Join(dir, errorLog))
	if err != nil {
		t.Fatalf("reading error.log: %v", err)
	}
	if strings.Contains(string(errOut), "informational line") {
		t.Error("error.log should not contain an info-level line")
	}
	if !strings.Contains(string(errOut), "boom") {
		t.Error("error.log missing the error-level line")
	}
}

func TestSetup_LevelFiltersBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	closeLogs, err := Setup(dir, "warn")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer closeLogs()

	slog.Debug("should not appear")
	slog.Info("should not appear either")
	slog.Warn("should appear")

	combined, err := os.ReadFile(filepath.Join(dir, combinedLog))
	if err != nil {
		t.Fatalf("reading combined.log: %v", err)
	}
	if strings.Contains(string(combined), "should not appear") {
		t.Error("combined.log contains a line below the configured level")
	}
	if !strings.Contains(string(combined), "should appear") {
		t.Error("combined.log missing a line at the configured level")
	}
}

func TestParseLevel(t *testing.T) {
	tests := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"":      slog.LevelInfo,
		"bogus": slog.LevelInfo,
	}
	for in, want := range tests {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/drevilish/playlist-lab/internal/logging"
)

// writeLog writes JSON lines in the shape slog's JSONHandler emits.
func writeLog(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func entry(ts, level, msg string) string {
	return `{"time":"` + ts + `","level":"` + level + `","msg":"` + msg + `"}`
}

func TestReadEntries_NewestFirstAcrossRotations(t *testing.T) {
	dir := t.TempDir()
	// Rotated file holds the older entries, live file the newer ones.
	writeLog(t, filepath.Join(dir, "combined-2026-01-01.log"),
		entry("2026-01-01T10:00:00Z", "INFO", "oldest"))
	writeLog(t, filepath.Join(dir, "combined.log"),
		entry("2026-01-02T10:00:00Z", "INFO", "middle"),
		entry("2026-01-03T10:00:00Z", "INFO", "newest"))

	restore := setLevelForTest(t, "debug")
	defer restore()

	entries, truncated := readEntries(dir, "", 10)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 (both files read)", len(entries))
	}
	if entries[0].Message != "newest" || entries[2].Message != "oldest" {
		t.Errorf("order = %q..%q, want newest first", entries[0].Message, entries[2].Message)
	}
	if truncated {
		t.Error("truncated = true, want false when everything fit")
	}
}

// The limit must stop the scan, not just slice the result - that is the
// whole point of scanning newest-first.
func TestReadEntries_LimitTruncates(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, filepath.Join(dir, "combined.log"),
		entry("2026-01-01T10:00:00Z", "INFO", "a"),
		entry("2026-01-02T10:00:00Z", "INFO", "b"),
		entry("2026-01-03T10:00:00Z", "INFO", "c"))

	restore := setLevelForTest(t, "debug")
	defer restore()

	entries, truncated := readEntries(dir, "", 2)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Message != "c" || entries[1].Message != "b" {
		t.Errorf("got %q,%q - want the two newest", entries[0].Message, entries[1].Message)
	}
	if !truncated {
		t.Error("truncated = false, want true when the scan stopped early")
	}
}

// The configured level is a floor for this view, not just for what gets
// written: history already in the file from a chattier period must not
// reappear after an admin turns the level down. This is the bug the Node
// route's own comment records.
func TestReadEntries_ConfiguredLevelIsAFloor(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, filepath.Join(dir, "combined.log"),
		entry("2026-01-01T10:00:00Z", "DEBUG", "chatty"),
		entry("2026-01-02T10:00:00Z", "INFO", "routine"),
		entry("2026-01-03T10:00:00Z", "ERROR", "boom"))

	restore := setLevelForTest(t, "error")
	defer restore()

	entries, _ := readEntries(dir, "", 10)
	if len(entries) != 1 || entries[0].Message != "boom" {
		t.Fatalf("got %d entries (%v), want only the error entry", len(entries), messages(entries))
	}
}

// An explicit filter narrows further, and must find sparse matches that a
// fixed tail of recent lines would miss.
func TestReadEntries_ExplicitLevelFilter(t *testing.T) {
	dir := t.TempDir()
	lines := []string{entry("2026-01-01T10:00:00Z", "ERROR", "rare")}
	for i := 0; i < 50; i++ {
		lines = append(lines, entry("2026-01-02T10:00:00Z", "INFO", "noise"))
	}
	writeLog(t, filepath.Join(dir, "combined.log"), lines...)

	restore := setLevelForTest(t, "debug")
	defer restore()

	entries, _ := readEntries(dir, "error", 10)
	if len(entries) != 1 || entries[0].Message != "rare" {
		t.Fatalf("got %v, want just the one error buried behind 50 info lines", messages(entries))
	}
}

// A line still being written when the file was read won't parse; it should
// surface rather than vanish.
func TestParseEntry_UnparseableLineSurfaces(t *testing.T) {
	e := parseEntry(`{"time":"2026-01-01T10:00:00Z","level":"INFO","msg":"trunca`)
	if e.Message == "" {
		t.Fatal("dropped an unparseable line, want it surfaced as-is")
	}
	if e.Level != "info" {
		t.Errorf("level = %q, want info as the fallback", e.Level)
	}
}

func TestLogLimit_ClampsToRange(t *testing.T) {
	cases := map[string]int{"": defaultLogLimit, "abc": defaultLogLimit, "0": defaultLogLimit, "-5": defaultLogLimit, "50": 50, "99999": maxLogLimit}
	for in, want := range cases {
		if got := logLimit(in); got != want {
			t.Errorf("logLimit(%q) = %d, want %d", in, got, want)
		}
	}
}

func setLevelForTest(t *testing.T, level string) func() {
	t.Helper()
	previous := logging.CurrentLevel()
	if err := logging.SetLevel(level); err != nil {
		t.Fatalf("SetLevel(%q): %v", level, err)
	}
	return func() { logging.SetLevel(previous) } //nolint:errcheck // previous came from CurrentLevel, always valid
}

func messages(entries []logEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Message
	}
	return out
}

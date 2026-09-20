package handlers

import (
	"bufio"
	"encoding/csv"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drevilish/playlist-lab/internal/logging"
	"github.com/drevilish/playlist-lab/internal/session"
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

// Extra key-value attrs a call logged beyond time/level/msg must survive
// into Attrs (used to render the entry's expand disclosure) rather than
// being silently dropped, which previously made every entry's <details>
// expand to nothing.
func TestParseEntry_CapturesExtraAttrs(t *testing.T) {
	e := parseEntry(`{"time":"2026-01-01T10:00:00Z","level":"INFO","msg":"started job","job":"daily-scraper"}`)
	if len(e.Attrs) != 1 || e.Attrs[0].Key != "job" || e.Attrs[0].Value != "daily-scraper" {
		t.Fatalf("Attrs = %+v, want [{job daily-scraper}]", e.Attrs)
	}
}

func TestParseEntry_NoExtraAttrsWhenNoneLogged(t *testing.T) {
	e := parseEntry(`{"time":"2026-01-01T10:00:00Z","level":"INFO","msg":"background jobs started"}`)
	if len(e.Attrs) != 0 {
		t.Fatalf("Attrs = %+v, want none - the template must not render an empty <details> for this", e.Attrs)
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

// TestAdminLogsExportCSV_WritesEntries covers GET /admin/logs/export
// (DESIGN.md §8.6/§11.5, follow-up to the Playlists/Missing Tracks exports)
// end to end through the real handler/router, not just readEntries in
// isolation - the "one runnable check" that the CSV header actually lines
// up with what gets written per row.
func TestAdminLogsExportCSV_WritesEntries(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, filepath.Join(dir, "combined.log"),
		entry("2026-01-03T10:00:00Z", "ERROR", "sync failed"))
	restore := setLevelForTest(t, "debug")
	defer restore()

	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &AdminLogsHandler{DB: sqlDB, Tmpl: nopTemplates(), LogDir: dir}
	router := testRouter(sqlDB, http.MethodGet, "/admin/logs/export", h.exportCSV)
	rec := authedRequest(t, sqlDB, router, user, http.MethodGet, "/admin/logs/export", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parsing response as CSV: %v", err)
	}
	wantHeader := []string{"Timestamp", "Level", "Message", "Attributes"}
	if len(rows) != 2 || len(rows[0]) != len(wantHeader) {
		t.Fatalf("rows = %v, want a header row of %v plus exactly 1 data row", rows, wantHeader)
	}
	for i, want := range wantHeader {
		if rows[0][i] != want {
			t.Errorf("header[%d] = %q, want %q", i, rows[0][i], want)
		}
	}
	if rows[1][1] != "error" || rows[1][2] != "sync failed" {
		t.Errorf("data row = %v, want level=error message=%q", rows[1], "sync failed")
	}
}

// TestAdminLogsStream_PushesNewEntriesFilteredByLevel is the "one runnable
// check" for the SSE live-tail (DESIGN.md §11.5/§17): a real HTTP
// connection to GET /admin/logs/stream, a real slog call through
// logging.Setup's handler chain, and confirmation that the pushed fragment
// both arrives and is dropped when it's below the configured level floor -
// exercising internal/logging's broadcaster, admin_logs.go's stream
// handler, and the adminLogEntry template together, not each in isolation.
func TestAdminLogsStream_PushesNewEntriesFilteredByLevel(t *testing.T) {
	dir := t.TempDir()
	closeLogs, err := logging.Setup(dir, "info")
	if err != nil {
		t.Fatalf("logging.Setup: %v", err)
	}
	defer closeLogs()
	restore := setLevelForTest(t, "info")
	defer restore()

	sqlDB := newTestDB(t)
	user := newTestUser(t, sqlDB)
	h := &AdminLogsHandler{DB: sqlDB, Tmpl: nopTemplates(), LogDir: dir}
	router := testRouter(sqlDB, http.MethodGet, "/admin/logs/stream", h.stream)

	srv := httptest.NewServer(router)
	defer srv.Close()

	store := session.NewStore(sqlDB)
	sid, err := session.NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	if err := store.Save(sid, &session.Data{UserID: user.ID, PlexUserID: user.PlexUserID}); err != nil {
		t.Fatalf("seeding session: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/admin/logs/stream", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: session.CookieName, Value: sid})
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("connecting to stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// A subscriber isn't guaranteed to be registered the instant the
	// handler returns headers - give it a moment before logging, same
	// requirement any real pub/sub client-connects-then-publishes flow has.
	time.Sleep(50 * time.Millisecond)

	fragments := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		var data strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if data.Len() > 0 {
					fragments <- data.String()
					data.Reset()
				}
				continue
			}
			data.WriteString(strings.TrimPrefix(line, "data: ") + "\n")
		}
	}()

	// Below the "info" floor set above - stream() must filter this out.
	slog.Debug("should not stream", "k", "v")
	// At the configured floor - stream() must deliver this one.
	slog.Info("sync completed", "playlistId", 42)

	select {
	case fragment := <-fragments:
		if !strings.Contains(fragment, "sync completed") {
			t.Fatalf("streamed fragment = %q, want it to contain the logged message", fragment)
		}
		if !strings.Contains(fragment, "admin-log-level-info") {
			t.Fatalf("streamed fragment = %q, want the info level class", fragment)
		}
		if strings.Contains(fragment, "should not stream") {
			t.Fatalf("streamed fragment = %q, the sub-info debug line should have been filtered out", fragment)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for the SSE stream to push the new log entry")
	}

	// Confirm the debug line really was filtered, not just slower to
	// arrive - nothing else should show up on the channel.
	select {
	case extra := <-fragments:
		t.Fatalf("got an unexpected second fragment (the filtered debug line got through?): %q", extra)
	case <-time.After(200 * time.Millisecond):
	}
}

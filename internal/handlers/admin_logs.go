// Admin Logs tab, ported from routes/admin.ts's /logs, /log-level and
// DELETE /logs. Until now this panel rendered a "not yet ported" note.
//
// The reading strategy is the Node route's, for the reason its own comment
// gives: scan newest line first and stop once enough matches are found,
// rather than parsing all ~20,000 lines of every 5MB file to return a couple
// of hundred of them. The level filter has to be applied during the scan
// rather than to a fixed tail afterwards - errors are sparse, so the last N
// lines can easily contain none at all while plenty exist further back.
package handlers

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drevilish/playlist-lab/internal/auth"
	"github.com/drevilish/playlist-lab/internal/db"
	"github.com/drevilish/playlist-lab/internal/logging"
)

// adminLogConfigKey is the admin_config row the chosen level persists in, so
// it survives a restart the way configService.updateConfig did. Same
// table/pattern as deemix_arl and lidarr_url.
const adminLogConfigKey = "log_level"

const (
	defaultLogLimit = 200
	maxLogLimit     = 1000
)

type AdminLogsHandler struct {
	DB     *sql.DB
	Tmpl   *Templates
	LogDir string
}

func RegisterAdminLogs(r chi.Router, mw *auth.Middleware, h *AdminLogsHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Use(mw.RequireAdmin)
		r.Get("/admin/logs", h.list)
		r.Post("/admin/logs/clear", h.clear)
		r.Post("/admin/log-level", h.setLevel)
	})
}

type logEntry struct {
	Level     string
	Message   string
	Timestamp string
	Service   string
	ts        time.Time
}

// severity orders the levels so a configured floor can be applied to
// already-written history. Mirrors isAtLeastAsSevere() in the Node route:
// without it, setting the server to error-only still showed info entries
// here, because combined.log keeps whatever was written before the change.
var severity = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// combinedLogPaths lists combined.log and its rotations, newest first.
// lumberjack names rotations combined-<timestamp>.log, so they sort
// lexically oldest-first and are reversed here.
func combinedLogPaths(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "combined*.log"))
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	// The live file is always read first regardless of how rotations sort.
	live := filepath.Join(dir, "combined.log")
	out := []string{}
	if _, err := os.Stat(live); err == nil {
		out = append(out, live)
	}
	for _, m := range matches {
		if m != live {
			out = append(out, m)
		}
	}
	return out
}

// readEntries returns up to limit entries at or above both the configured
// floor and any explicit level filter, newest first, plus whether it stopped
// early (so the UI can say the view is truncated).
func readEntries(dir, levelFilter string, limit int) ([]logEntry, bool) {
	floor := severity[logging.CurrentLevel()]
	var entries []logEntry
	scannedAll := true

	for _, path := range combinedLogPaths(dir) {
		if len(entries) >= limit {
			scannedAll = false
			break
		}
		lines, err := readLines(path)
		if err != nil {
			slog.Warn("admin log viewer: unreadable log file", "error", err, "path", path)
			continue
		}
		for i := len(lines) - 1; i >= 0; i-- {
			if len(entries) >= limit {
				scannedAll = false
				break
			}
			line := strings.TrimSpace(lines[i])
			if line == "" {
				continue
			}
			e := parseEntry(line)
			if levelFilter != "" && e.Level != levelFilter {
				continue
			}
			if severity[e.Level] < floor {
				continue
			}
			entries = append(entries, e)
		}
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ts.After(entries[j].ts) })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, !scannedAll
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	// slog writes one JSON object per line, but a long message (a stack
	// trace, a Plex error body) can exceed bufio's 64KB default and would
	// otherwise abort the scan mid-file.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

// parseEntry reads one slog JSON line. A line still being written when the
// file was read won't parse; it is surfaced as-is rather than dropped,
// matching the Node route's handling of partial winston lines.
func parseEntry(line string) logEntry {
	var raw struct {
		Time    time.Time `json:"time"`
		Level   string    `json:"level"`
		Msg     string    `json:"msg"`
		Service string    `json:"service"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return logEntry{Level: "info", Message: line}
	}
	lvl := strings.ToLower(raw.Level)
	if _, ok := severity[lvl]; !ok {
		lvl = "info"
	}
	return logEntry{
		Level:     lvl,
		Message:   raw.Msg,
		Timestamp: raw.Time.Format("2006-01-02 15:04:05"),
		Service:   raw.Service,
		ts:        raw.Time,
	}
}

func (h *AdminLogsHandler) render(w http.ResponseWriter, levelFilter string, limit int, notice string) {
	entries, truncated := readEntries(h.LogDir, levelFilter, limit)
	h.Tmpl.RenderPartial(w, "partials/admin_logs.html", map[string]any{
		"Entries":      entries,
		"Truncated":    truncated,
		"Level":        logging.CurrentLevel(),
		"Levels":       logging.Levels,
		"Filter":       levelFilter,
		"Limit":        limit,
		"Notice":       notice,
		"LimitOptions": []int{50, 200, 500, 1000},
	})
}

func (h *AdminLogsHandler) list(w http.ResponseWriter, r *http.Request) {
	h.render(w, r.URL.Query().Get("filter"), logLimit(r.URL.Query().Get("limit")), "")
}

func logLimit(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return defaultLogLimit
	}
	if n > maxLogLimit {
		return maxLogLimit
	}
	return n
}

func (h *AdminLogsHandler) setLevel(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	requested := r.FormValue("level")
	if err := logging.SetLevel(requested); err != nil {
		h.render(w, r.FormValue("filter"), logLimit(r.FormValue("limit")), err.Error())
		return
	}
	if err := db.SetAdminConfig(h.DB, adminLogConfigKey, requested); err != nil {
		slog.Error("failed to persist log level", "error", err)
	}
	user := auth.CurrentUser(r)
	slog.Info("Log level changed by admin", "adminId", user.ID, "level", requested)
	h.render(w, r.FormValue("filter"), logLimit(r.FormValue("limit")), "Log level set to "+requested+".")
}

// clear truncates the log files in place rather than deleting them: the
// running process holds them open through lumberjack, so unlinking would
// leave it writing to a file nobody can read any more.
func (h *AdminLogsHandler) clear(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	for _, path := range combinedLogPaths(h.LogDir) {
		if path == filepath.Join(h.LogDir, "combined.log") {
			if err := os.Truncate(path, 0); err != nil {
				slog.Error("failed to clear log file", "error", err, "path", path)
			}
			continue
		}
		// Rotations are not held open, so they can just go.
		if err := os.Remove(path); err != nil {
			slog.Error("failed to remove rotated log file", "error", err, "path", path)
		}
	}
	user := auth.CurrentUser(r)
	slog.Info("Logs cleared by admin", "adminId", user.ID)
	h.render(w, r.FormValue("filter"), logLimit(r.FormValue("limit")), "Logs cleared.")
}

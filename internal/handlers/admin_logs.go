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
	"encoding/csv"
	"encoding/json"
	"fmt"
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
	"github.com/drevilish/playlist-lab/internal/services/notifications"
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
	DB            *sql.DB
	Tmpl          *Templates
	LogDir        string
	Notifications *notifications.Store
}

func RegisterAdminLogs(r chi.Router, mw *auth.Middleware, h *AdminLogsHandler) {
	r.Group(func(r chi.Router) {
		r.Use(mw.RequireAuth)
		r.Use(mw.RequireAdmin)
		r.Get("/admin/logs", h.list)
		r.Get("/admin/logs/stream", h.stream)
		r.Get("/admin/logs/export", h.exportCSV)
		r.Post("/admin/logs/clear", h.clear)
		r.Post("/admin/log-level", h.setLevel)
	})
}

// logAttr is one slog key-value pair beyond the three every entry always
// has (time/level/msg), shown in the entry's expandable detail. Previously
// only a "service" field was pulled out by name, but nothing in this
// codebase ever logs a "service" attr - it was dead weight ported from the
// Node app's winston setup, and every entry's disclosure triangle expanded
// to reveal nothing. Capturing whatever attrs a call actually logged
// (error, path, userId, ...) makes that triangle mean something.
type logAttr struct {
	Key   string
	Value string
}

type logEntry struct {
	Level     string
	Message   string
	Timestamp string
	Attrs     []logAttr
	// Key is a stable per-entry identifier for the "adminLogEntry" template
	// (admin_logs.html)'s data-log-key - content-derived rather than a loop
	// index, since the SSE live-tail (stream, below) renders one entry at a
	// time with no list position to key off of.
	Key string
	ts  time.Time
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
//
// Unmarshals into a generic map rather than a fixed struct so that whatever
// extra key-value attrs a particular slog call logged (error, path,
// userId, ...) survive into Attrs instead of being silently discarded -
// there's no fixed schema for them, they vary per call site.
func parseEntry(line string) logEntry {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return logEntry{Level: "info", Message: line}
	}
	lvl := "info"
	if v, ok := raw["level"].(string); ok {
		lvl = strings.ToLower(v)
	}
	if _, ok := severity[lvl]; !ok {
		lvl = "info"
	}
	msg, _ := raw["msg"].(string)
	var ts time.Time
	var formatted, rawTime string
	if v, ok := raw["time"].(string); ok {
		rawTime = v
		if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
			ts = parsed
			formatted = parsed.Format("2006-01-02 15:04:05")
		}
	}
	delete(raw, "time")
	delete(raw, "level")
	delete(raw, "msg")

	attrs := make([]logAttr, 0, len(raw))
	for k, v := range raw {
		attrs = append(attrs, logAttr{Key: k, Value: fmt.Sprint(v)})
	}
	sort.Slice(attrs, func(i, j int) bool { return attrs[i].Key < attrs[j].Key })

	return logEntry{
		Level:     lvl,
		Message:   msg,
		Timestamp: formatted,
		Attrs:     attrs,
		// rawTime carries nanosecond precision (formatted above only goes to
		// the second), so two entries logged within the same second still
		// get distinct keys.
		Key: rawTime + "|" + msg,
		ts:  ts,
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

// stream is GET /admin/logs/stream - an SSE endpoint (DESIGN.md §11.5/§17)
// pushing one rendered "adminLogEntry" fragment per new line the
// internal/logging broadcaster publishes, filtered by the same severity
// floor and optional ?filter= level readEntries/list already apply, so a
// live-tailed entry never shows up here that the historical GET /admin/logs
// view would have hidden. Mirrors notifications.go's stream handler.
func (h *AdminLogsHandler) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	levelFilter := r.URL.Query().Get("filter")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	// Unlike notifications.go's stream, there's no initial payload worth
	// sending (the historical batch is GET /admin/logs's job) - but the
	// client's response headers otherwise never actually go out until the
	// first Write/Flush, which without this would only happen at the first
	// log line or the 25s keep-alive, leaving every connection looking
	// hung until then.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	lines, unsubscribe := logging.Subscribe()
	defer unsubscribe()

	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return
			}
			e := parseEntry(line)
			if levelFilter != "" && e.Level != levelFilter {
				continue
			}
			if severity[e.Level] < severity[logging.CurrentLevel()] {
				continue
			}
			var buf strings.Builder
			if err := h.Tmpl.partials.ExecuteTemplate(&buf, "adminLogEntry", e); err != nil {
				continue
			}
			writeSSEEvent(w, buf.String())
			flusher.Flush()
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// exportCSV is GET /admin/logs/export - same stdlib encoding/csv approach as
// PlaylistsHandler.exportCSV/MissingHandler.exportCSV (DESIGN.md §8.6),
// reusing readEntries with the same ?filter=/?limit= the log viewer itself
// is currently showing, so the download matches what's on screen.
func (h *AdminLogsHandler) exportCSV(w http.ResponseWriter, r *http.Request) {
	entries, _ := readEntries(h.LogDir, r.URL.Query().Get("filter"), logLimit(r.URL.Query().Get("limit")))

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="admin-logs.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"Timestamp", "Level", "Message", "Attributes"})
	for _, e := range entries {
		attrs := make([]string, len(e.Attrs))
		for i, a := range e.Attrs {
			attrs[i] = a.Key + "=" + a.Value
		}
		cw.Write([]string{e.Timestamp, e.Level, e.Message, strings.Join(attrs, "; ")})
	}
	cw.Flush()
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
	user := auth.CurrentUser(r)
	_ = r.ParseForm()
	requested := r.FormValue("level")
	if err := logging.SetLevel(requested); err != nil {
		h.Notifications.Add(user.ID, notifications.TypeAction, "Set log level", err.Error(), notifications.StatusError, nil)
		h.render(w, r.FormValue("filter"), logLimit(r.FormValue("limit")), err.Error())
		return
	}
	if err := db.SetAdminConfig(h.DB, adminLogConfigKey, requested); err != nil {
		slog.Error("failed to persist log level", "error", err)
	}
	slog.Info("Log level changed by admin", "adminId", user.ID, "level", requested)
	h.Notifications.Add(user.ID, notifications.TypeAction, "Set log level", requested, notifications.StatusSuccess, nil)
	h.render(w, r.FormValue("filter"), logLimit(r.FormValue("limit")), "Log level set to "+requested+".")
}

// clear truncates the log files in place rather than deleting them: the
// running process holds them open through lumberjack, so unlinking would
// leave it writing to a file nobody can read any more.
func (h *AdminLogsHandler) clear(w http.ResponseWriter, r *http.Request) {
	user := auth.CurrentUser(r)
	_ = r.ParseForm()
	failed := 0
	for _, path := range combinedLogPaths(h.LogDir) {
		if path == filepath.Join(h.LogDir, "combined.log") {
			if err := os.Truncate(path, 0); err != nil {
				slog.Error("failed to clear log file", "error", err, "path", path)
				failed++
			}
			continue
		}
		// Rotations are not held open, so they can just go.
		if err := os.Remove(path); err != nil {
			slog.Error("failed to remove rotated log file", "error", err, "path", path)
			failed++
		}
	}
	slog.Info("Logs cleared by admin", "adminId", user.ID)
	if failed > 0 {
		h.Notifications.Add(user.ID, notifications.TypeAction, "Clear logs", fmt.Sprintf("%d file(s) failed to clear - see server logs", failed), notifications.StatusError, nil)
	} else {
		h.Notifications.Add(user.ID, notifications.TypeAction, "Clear logs", "Logs cleared", notifications.StatusSuccess, nil)
	}
	h.render(w, r.FormValue("filter"), logLimit(r.FormValue("limit")), "Logs cleared.")
}

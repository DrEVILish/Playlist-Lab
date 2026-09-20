package logging

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// maxSizeMB/maxBackups match utils/logger.ts's winston File transports
// exactly (maxsize: 5242880 bytes = 5MB, maxFiles: 5).
const (
	maxSizeMB   = 5
	maxBackups  = 5
	combinedLog = "combined.log"
	errorLog    = "error.log"
)

// ParseLevel maps the LOG_LEVEL env var (error/warn/info/debug, matching
// utils/logger.ts's LOG_LEVELS) to an slog.Level. "warn" isn't a distinct
// slog level - it maps to slog.LevelWarn same as the rest.
func ParseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Levels are the selectable log levels, ordered least to most severe -
// the same set utils/logger.ts exposes as LOG_LEVELS, in the same order, so
// the admin Logs tab offers exactly the choices the Node app did.
var Levels = []string{"debug", "info", "warn", "error"}

// level is shared by the stdout and combined.log handlers so an admin
// changing the level at runtime (PUT /api/admin/log-level in the Node app)
// takes effect immediately for both, without rebuilding the logger or
// restarting. error.log is deliberately not wired to it - it only ever
// carries errors regardless of what the rest of the app is set to.
var level = new(slog.LevelVar)

// SetLevel changes the active log level. Unknown names are rejected rather
// than silently falling back to info, so a typo in the admin UI can't
// quietly turn a debug session back down.
func SetLevel(name string) error {
	for _, l := range Levels {
		if l == name {
			level.Set(ParseLevel(name))
			return nil
		}
	}
	return fmt.Errorf("unknown log level %q, want one of %v", name, Levels)
}

// CurrentLevel is the active level's name, for rendering the admin UI's
// current selection.
func CurrentLevel() string {
	switch level.Level() {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "info"
	}
}

// Setup wires slog's default logger to write to stdout (text, for the
// systemd journal - unchanged from before) plus size-rotated combined.log
// (all levels at or above the configured one) and error.log (errors only)
// under logDir, mirroring utils/logger.ts's three transports. The returned
// close func flushes/closes the log files; call it before the process
// exits normally (a crash/kill won't run it, same as Node's process never
// explicitly closing its winston transports either).
func Setup(logDir, level string) (close func() error, err error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	SetLevel(level) //nolint:errcheck // an unknown LOG_LEVEL keeps the info default, as before

	combined := &lumberjack.Logger{Filename: filepath.Join(logDir, combinedLog), MaxSize: maxSizeMB, MaxBackups: maxBackups}
	errFile := &lumberjack.Logger{Filename: filepath.Join(logDir, errorLog), MaxSize: maxSizeMB, MaxBackups: maxBackups}

	handler := newMultiHandler(
		slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar()}),
		slog.NewJSONHandler(combined, &slog.HandlerOptions{Level: levelVar()}),
		slog.NewJSONHandler(errFile, &slog.HandlerOptions{Level: slog.LevelError}),
		// live's JSON shape/level floor matches combined.log's handler
		// exactly (parseEntry, admin_logs.go, parses either one the same
		// way) - it just publishes to Subscribe()'s channels instead of a
		// file, for the admin Logs tab's SSE live-tail (DESIGN.md §17).
		slog.NewJSONHandler(live, &slog.HandlerOptions{Level: levelVar()}),
	)
	slog.SetDefault(slog.New(handler))

	return func() error {
		return errors.Join(combined.Close(), errFile.Close())
	}, nil
}

// levelVar exposes the shared LevelVar as an slog.Leveler for handler
// options; naming it apart from the Setup parameter keeps that shadowing
// obvious rather than accidental.
func levelVar() slog.Leveler { return level }

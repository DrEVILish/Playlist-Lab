package logging

import (
	"errors"
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
	lvl := ParseLevel(level)

	combined := &lumberjack.Logger{Filename: filepath.Join(logDir, combinedLog), MaxSize: maxSizeMB, MaxBackups: maxBackups}
	errFile := &lumberjack.Logger{Filename: filepath.Join(logDir, errorLog), MaxSize: maxSizeMB, MaxBackups: maxBackups}

	handler := newMultiHandler(
		slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}),
		slog.NewJSONHandler(combined, &slog.HandlerOptions{Level: lvl}),
		slog.NewJSONHandler(errFile, &slog.HandlerOptions{Level: slog.LevelError}),
	)
	slog.SetDefault(slog.New(handler))

	return func() error {
		return errors.Join(combined.Close(), errFile.Close())
	}, nil
}

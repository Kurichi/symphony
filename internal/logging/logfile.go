package logging

import (
	"io"
	"log/slog"
	"os"

	"gopkg.in/lumberjack.v2"
)

// Config holds logging configuration.
type Config struct {
	// FilePath is the log file path. Empty means no file logging.
	FilePath string
	// MaxSizeMB is the maximum size of a log file before rotation (default 100).
	MaxSizeMB int
	// MaxBackups is the number of old log files to keep (default 3).
	MaxBackups int
	// MaxAgeDays is the maximum days to retain old log files (default 28).
	MaxAgeDays int
	// JSON enables JSON output format (default true).
	JSON bool
}

// Setup configures the global slog logger with the given config.
// Returns a closer function for the log file.
func Setup(cfg Config) (io.Closer, error) {
	var writers []io.Writer
	var closer io.Closer

	// Always log to stderr
	writers = append(writers, os.Stderr)

	// Optionally log to a file with rotation
	if cfg.FilePath != "" {
		maxSize := cfg.MaxSizeMB
		if maxSize <= 0 {
			maxSize = 100
		}
		maxBackups := cfg.MaxBackups
		if maxBackups <= 0 {
			maxBackups = 3
		}
		maxAge := cfg.MaxAgeDays
		if maxAge <= 0 {
			maxAge = 28
		}
		lj := &lumberjack.Logger{
			Filename:   cfg.FilePath,
			MaxSize:    maxSize,
			MaxBackups: maxBackups,
			MaxAge:     maxAge,
			Compress:   true,
		}
		writers = append(writers, lj)
		closer = lj
	}

	w := io.MultiWriter(writers...)

	var handler slog.Handler
	if cfg.JSON {
		handler = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})
	} else {
		handler = slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})
	}

	slog.SetDefault(slog.New(handler))
	return closer, nil
}

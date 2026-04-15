// Package logging sets up the slog JSON handler shared by every binary.
//
// Usage:
//
//	log, err := logging.New(logging.Options{Level: "info"})
//	if err != nil { ... }
//	logging.SetDefault(log)
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Options controls logger construction.
type Options struct {
	// Level is one of "debug", "info", "warn", "error". Empty defaults to "info".
	Level string
	// Writer defaults to os.Stderr.
	Writer io.Writer
}

// New returns a JSON-handled *slog.Logger.
func New(opts Options) (*slog.Logger, error) {
	lvl, err := parseLevel(opts.Level)
	if err != nil {
		return nil, err
	}
	w := opts.Writer
	if w == nil {
		w = os.Stderr
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(h), nil
}

// SetDefault installs log as the process-wide default slog logger.
func SetDefault(log *slog.Logger) {
	slog.SetDefault(log)
}

// RequestAttrs returns a child logger pre-decorated with common request fields.
// Callers should set req_id to a per-request identifier (e.g. random UUID).
func RequestAttrs(log *slog.Logger, method, path, remote, reqID string) *slog.Logger {
	return log.With(
		slog.String("method", method),
		slog.String("path", path),
		slog.String("remote", remote),
		slog.String("req_id", reqID),
	)
}

func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error", "err":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logging: invalid level %q", s)
	}
}

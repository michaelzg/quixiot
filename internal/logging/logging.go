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
	"path/filepath"
	"strings"
	"sync"
)

const (
	// DefaultFileMaxBytes caps each rotating log file at 10 MiB.
	DefaultFileMaxBytes int64 = 10 << 20
	// DefaultFileMaxFiles keeps the active file plus two rotated files.
	DefaultFileMaxFiles = 3
)

// Options controls logger construction.
type Options struct {
	// Level is one of "debug", "info", "warn", "error". Empty defaults to "info".
	Level string
	// Writer defaults to os.Stderr.
	Writer io.Writer
	// File writes logs to a rotating file instead of Writer/os.Stderr.
	File string
	// MaxBytes caps each log file when File is set. Empty uses DefaultFileMaxBytes.
	MaxBytes int64
	// MaxFiles caps retained files, including the active file. Empty uses DefaultFileMaxFiles.
	MaxFiles int
}

// New returns a JSON-handled *slog.Logger.
func New(opts Options) (*slog.Logger, error) {
	lvl, err := parseLevel(opts.Level)
	if err != nil {
		return nil, err
	}
	w := opts.Writer
	if w != nil && opts.File != "" {
		return nil, fmt.Errorf("logging: Writer and File are mutually exclusive")
	}
	if opts.File != "" {
		w, err = NewRotatingFile(opts.File, opts.MaxBytes, opts.MaxFiles)
		if err != nil {
			return nil, err
		}
	} else if w == nil {
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

// RotatingFile is an io.Writer that bounds log files on disk.
type RotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	file     *os.File
	size     int64
}

// NewRotatingFile opens path for append and rotates it once it reaches maxBytes.
// maxFiles includes the active file. For example, maxFiles=3 keeps path,
// path.1, and path.2.
func NewRotatingFile(path string, maxBytes int64, maxFiles int) (*RotatingFile, error) {
	if path == "" {
		return nil, fmt.Errorf("logging: log file path is required")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultFileMaxBytes
	}
	if maxFiles <= 0 {
		maxFiles = DefaultFileMaxFiles
	}
	w := &RotatingFile{
		path:     path,
		maxBytes: maxBytes,
		maxFiles: maxFiles,
	}
	if err := w.openLocked(); err != nil {
		return nil, err
	}
	if w.size >= w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			_ = w.Close()
			return nil, err
		}
	}
	return w, nil
}

func (w *RotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	accepted := len(p)
	p = trimRecord(p, w.maxBytes)
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, err
	}
	return accepted, nil
}

func (w *RotatingFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *RotatingFile) openLocked() error {
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return fmt.Errorf("logging: create log dir: %w", err)
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("logging: open log file %s: %w", w.path, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("logging: stat log file %s: %w", w.path, err)
	}
	w.file = file
	w.size = info.Size()
	return nil
}

func (w *RotatingFile) rotateLocked() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return fmt.Errorf("logging: close log file before rotation: %w", err)
		}
		w.file = nil
	}

	if w.maxFiles <= 1 {
		if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("logging: remove active log: %w", err)
		}
		return w.openLocked()
	}

	if err := os.Remove(backupPath(w.path, w.maxFiles-1)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("logging: remove oldest rotated log: %w", err)
	}
	for i := w.maxFiles - 2; i >= 1; i-- {
		oldPath := backupPath(w.path, i)
		newPath := backupPath(w.path, i+1)
		if err := os.Rename(oldPath, newPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("logging: rotate %s to %s: %w", oldPath, newPath, err)
		}
	}
	if err := os.Rename(w.path, backupPath(w.path, 1)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("logging: rotate active log: %w", err)
	}
	return w.openLocked()
}

func backupPath(path string, index int) string {
	return fmt.Sprintf("%s.%d", path, index)
}

func trimRecord(p []byte, maxBytes int64) []byte {
	if maxBytes <= 0 || int64(len(p)) <= maxBytes {
		return p
	}
	maxLen := int(maxBytes)
	prefix := []byte("[log record truncated]\n")
	if maxLen <= len(prefix) {
		return append([]byte(nil), p[len(p)-maxLen:]...)
	}
	out := make([]byte, 0, maxLen)
	out = append(out, prefix...)
	out = append(out, p[len(p)-(maxLen-len(prefix)):]...)
	return out
}

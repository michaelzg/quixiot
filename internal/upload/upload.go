package upload

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Handler struct {
	Dir             string
	Logger          *slog.Logger
	MaxStorageBytes int64
	OnStored        func(Response)
}

type Response struct {
	Bytes          int64  `json:"bytes"`
	SHA256         string `json:"sha256"`
	DurationMillis int64  `json:"durationMs"`
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Dir == "" {
		http.Error(w, "upload directory not configured", http.StatusInternalServerError)
		return
	}
	if h.MaxStorageBytes > 0 && r.ContentLength > h.MaxStorageBytes {
		http.Error(w, fmt.Sprintf("upload exceeds storage cap of %d bytes", h.MaxStorageBytes), http.StatusRequestEntityTooLarge)
		return
	}

	name, err := sanitizeFilename(r.PathValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(h.Dir, 0o755); err != nil {
		http.Error(w, fmt.Sprintf("create upload dir: %v", err), http.StatusInternalServerError)
		return
	}

	start := time.Now()
	target := filepath.Join(h.Dir, name)
	tmpDir := filepath.Join(h.Dir, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		http.Error(w, fmt.Sprintf("create upload temp dir: %v", err), http.StatusInternalServerError)
		return
	}
	tmp, err := os.CreateTemp(tmpDir, name+".*")
	if err != nil {
		http.Error(w, fmt.Sprintf("create temp file: %v", err), http.StatusInternalServerError)
		return
	}
	tmpPath := tmp.Name()
	cleanupTemp := true
	defer func() {
		_ = tmp.Close()
		if cleanupTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	hasher := sha256.New()
	body := r.Body
	if h.MaxStorageBytes > 0 {
		body = http.MaxBytesReader(w, r.Body, h.MaxStorageBytes)
	}
	written, err := io.Copy(io.MultiWriter(tmp, hasher), body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, fmt.Sprintf("upload exceeds storage cap of %d bytes", h.MaxStorageBytes), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, fmt.Sprintf("stream upload body: %v", err), http.StatusBadRequest)
		return
	}
	if err := tmp.Close(); err != nil {
		http.Error(w, fmt.Sprintf("close temp file: %v", err), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmpPath, target); err != nil {
		http.Error(w, fmt.Sprintf("persist upload: %v", err), http.StatusInternalServerError)
		return
	}
	cleanupTemp = false

	if h.MaxStorageBytes > 0 {
		pruned, err := PruneDir(h.Dir, h.MaxStorageBytes, target)
		if err != nil {
			http.Error(w, fmt.Sprintf("prune upload storage: %v", err), http.StatusInternalServerError)
			return
		}
		if pruned.RemovedFiles > 0 {
			h.logger().Debug("pruned upload storage",
				"dir", h.Dir,
				"max_bytes", pruned.MaxBytes,
				"remaining_bytes", pruned.RemainingBytes,
				"removed_files", pruned.RemovedFiles,
				"removed_bytes", pruned.RemovedBytes,
			)
		}
	}

	resp := Response{
		Bytes:          written,
		SHA256:         hex.EncodeToString(hasher.Sum(nil)),
		DurationMillis: time.Since(start).Milliseconds(),
	}
	if log := h.logger(); log != nil {
		log.Debug("stored upload",
			"name", name,
			"path", target,
			"bytes", resp.Bytes,
			"sha256", resp.SHA256,
			"duration_ms", resp.DurationMillis,
		)
	}
	if h.OnStored != nil {
		h.OnStored(resp)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger().Error("write upload response", "error", err)
	}
}

func sanitizeFilename(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("upload name is required")
	}
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("upload name must not contain path separators")
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("upload name must not be %q", name)
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	safe := b.String()
	if safe == "" || safe == "." || safe == ".." {
		return "", fmt.Errorf("upload name %q has no safe filename content", name)
	}
	return safe, nil
}

type PruneResult struct {
	MaxBytes       int64
	RemainingBytes int64
	RemovedFiles   int
	RemovedBytes   int64
}

// PruneDir deletes the oldest regular files in dir until total size is under
// maxBytes. keepPath is preserved so the upload that just completed remains
// available even when old files must be evicted.
func PruneDir(dir string, maxBytes int64, keepPath string) (PruneResult, error) {
	result := PruneResult{MaxBytes: maxBytes}
	if maxBytes <= 0 {
		return result, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return result, fmt.Errorf("read upload dir: %w", err)
	}

	type uploadFile struct {
		path    string
		size    int64
		modTime time.Time
	}
	files := make([]uploadFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return result, fmt.Errorf("stat upload file %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		result.RemainingBytes += info.Size()
		files = append(files, uploadFile{path: path, size: info.Size(), modTime: info.ModTime()})
	}
	if result.RemainingBytes <= maxBytes {
		return result, nil
	}

	keepPath = filepath.Clean(keepPath)
	sort.Slice(files, func(i, j int) bool {
		if files[i].modTime.Equal(files[j].modTime) {
			return files[i].path < files[j].path
		}
		return files[i].modTime.Before(files[j].modTime)
	})
	for _, file := range files {
		if result.RemainingBytes <= maxBytes {
			break
		}
		if filepath.Clean(file.path) == keepPath {
			continue
		}
		if err := os.Remove(file.path); err != nil && !os.IsNotExist(err) {
			return result, fmt.Errorf("remove upload file %s: %w", filepath.Base(file.path), err)
		}
		result.RemainingBytes -= file.size
		result.RemovedBytes += file.size
		result.RemovedFiles++
	}
	if result.RemainingBytes > maxBytes {
		return result, fmt.Errorf("upload storage remains over cap: remaining=%d max=%d", result.RemainingBytes, maxBytes)
	}
	return result, nil
}

func (h Handler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

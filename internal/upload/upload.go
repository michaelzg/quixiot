package upload

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Handler struct {
	Dir      string
	Logger   *slog.Logger
	OnStored func(Response)
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
	tmp, err := os.CreateTemp(h.Dir, name+".tmp-*")
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
	written, err := io.Copy(io.MultiWriter(tmp, hasher), r.Body)
	if err != nil {
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

	resp := Response{
		Bytes:          written,
		SHA256:         hex.EncodeToString(hasher.Sum(nil)),
		DurationMillis: time.Since(start).Milliseconds(),
	}
	if log := h.logger(); log != nil {
		log.Info("stored upload",
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

func (h Handler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

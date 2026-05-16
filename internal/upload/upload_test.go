package upload_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quixiot/internal/upload"
)

func TestHandlerStoresUploadAndHashesBody(t *testing.T) {
	dir := t.TempDir()
	body := []byte("hello over http3")
	req := httptest.NewRequest(http.MethodPost, "/files/report.bin", bytes.NewReader(body))
	req.SetPathValue("name", "report.bin")
	rec := httptest.NewRecorder()

	h := upload.Handler{
		Dir:    dir,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp upload.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	wantSHA := sha256.Sum256(body)
	if resp.Bytes != int64(len(body)) {
		t.Fatalf("bytes: want %d got %d", len(body), resp.Bytes)
	}
	if resp.SHA256 != hex.EncodeToString(wantSHA[:]) {
		t.Fatalf("sha256: want %s got %s", hex.EncodeToString(wantSHA[:]), resp.SHA256)
	}

	data, err := os.ReadFile(filepath.Join(dir, "report.bin"))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if !bytes.Equal(data, body) {
		t.Fatalf("stored body mismatch: got %q", string(data))
	}
}

func TestHandlerRejectsPathSeparators(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/files/%2e%2e%2fsecret", bytes.NewReader([]byte("payload")))
	req.SetPathValue("name", "../secret")
	rec := httptest.NewRecorder()

	h := upload.Handler{Dir: t.TempDir()}
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 got %d", rec.Code)
	}
}

func TestHandlerPrunesOldUploadsToStorageCap(t *testing.T) {
	dir := t.TempDir()
	oldest := filepath.Join(dir, "oldest.bin")
	newer := filepath.Join(dir, "newer.bin")
	if err := os.WriteFile(oldest, []byte("12345678"), 0o644); err != nil {
		t.Fatalf("write oldest: %v", err)
	}
	if err := os.WriteFile(newer, []byte("12345"), 0o644); err != nil {
		t.Fatalf("write newer: %v", err)
	}
	now := time.Now()
	if err := os.Chtimes(oldest, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("chtimes oldest: %v", err)
	}
	if err := os.Chtimes(newer, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatalf("chtimes newer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/files/current.bin", bytes.NewReader([]byte("abcde")))
	req.SetPathValue("name", "current.bin")
	rec := httptest.NewRecorder()
	h := upload.Handler{
		Dir:             dir,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxStorageBytes: 12,
	}
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Fatalf("oldest upload should be pruned: %v", err)
	}
	for _, path := range []string{newer, filepath.Join(dir, "current.bin")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to remain: %v", path, err)
		}
	}
}

func TestHandlerRejectsUploadLargerThanStorageCap(t *testing.T) {
	dir := t.TempDir()
	req := httptest.NewRequest(http.MethodPost, "/files/too-big.bin", bytes.NewReader([]byte("12345")))
	req.SetPathValue("name", "too-big.bin")
	rec := httptest.NewRecorder()
	h := upload.Handler{Dir: dir, MaxStorageBytes: 4}
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: want 413 got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "too-big.bin")); !os.IsNotExist(err) {
		t.Fatalf("oversized upload should not be stored: %v", err)
	}
}

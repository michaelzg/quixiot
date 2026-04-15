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

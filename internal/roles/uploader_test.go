package roles_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"quixiot/internal/client"
	"quixiot/internal/roles"
)

type fakeUploaderClient struct {
	lastName string
	lastSize int64
	lastSeed int64
	calls    int
}

func (f *fakeUploaderClient) UploadDeterministic(context.Context, string, int64, int64) (client.UploadResult, error) {
	panic("use named method below")
}

func (f *fakeUploaderClient) Upload(ctx context.Context, name string, size int64, seed int64) (client.UploadResult, error) {
	f.calls++
	f.lastName = name
	f.lastSize = size
	f.lastSeed = seed
	return client.UploadResult{Bytes: size, SHA256: "ok", DurationMillis: 1}, nil
}

type uploaderAdapter struct{ *fakeUploaderClient }

func (a uploaderAdapter) UploadDeterministic(ctx context.Context, name string, size int64, seed int64) (client.UploadResult, error) {
	return a.fakeUploaderClient.Upload(ctx, name, size, seed)
}

func TestUploaderUploadOnce(t *testing.T) {
	fake := &fakeUploaderClient{}
	uploader := roles.Uploader{
		Client:   uploaderAdapter{fake},
		ClientID: "device-123",
		Interval: time.Second,
		Size:     1024,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := uploader.UploadOnce(context.Background(), 2); err != nil {
		t.Fatalf("UploadOnce: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("calls: want 1 got %d", fake.calls)
	}
	if fake.lastName != "device-123-002.bin" {
		t.Fatalf("name: got %q", fake.lastName)
	}
	if fake.lastSize != 1024 {
		t.Fatalf("size: got %d", fake.lastSize)
	}
	if fake.lastSeed != 3 {
		t.Fatalf("seed: got %d", fake.lastSeed)
	}
}

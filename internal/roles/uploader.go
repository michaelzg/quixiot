package roles

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"quixiot/internal/client"
	"quixiot/internal/metrics"
)

type UploaderClient interface {
	UploadDeterministic(ctx context.Context, name string, size int64, seed int64) (client.UploadResult, error)
}

type Uploader struct {
	Client   UploaderClient
	ClientID string
	Interval time.Duration
	Size     int64
	Logger   *slog.Logger
	Metrics  *metrics.ClientMetrics
}

func (u Uploader) Run(ctx context.Context) error {
	if u.Client == nil {
		return fmt.Errorf("uploader: client is required")
	}
	if u.ClientID == "" {
		return fmt.Errorf("uploader: client ID is required")
	}
	if u.Interval <= 0 {
		return fmt.Errorf("uploader: interval must be positive")
	}
	if u.Size < 0 {
		return fmt.Errorf("uploader: size must be non-negative")
	}

	var seq int64
	if err := u.UploadOnce(ctx, seq); err != nil {
		u.logger().Error("upload failed", "error", err)
	}

	ticker := time.NewTicker(u.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			seq++
			if err := u.UploadOnce(ctx, seq); err != nil {
				u.logger().Error("upload failed", "error", err)
			}
		}
	}
}

func (u Uploader) UploadOnce(ctx context.Context, seq int64) error {
	start := time.Now()
	name := fmt.Sprintf("%s-%03d.bin", safeID(u.ClientID), seq)
	seed := seq + 1
	resp, err := u.Client.UploadDeterministic(ctx, name, u.Size, seed)
	if err != nil {
		return fmt.Errorf("uploader: upload %s: %w", name, err)
	}
	u.logger().Info("upload complete",
		"client_id", u.ClientID,
		"name", name,
		"bytes", resp.Bytes,
		"sha256", resp.SHA256,
		"duration_ms", resp.DurationMillis,
	)
	if u.Metrics != nil {
		u.Metrics.UploadDuration.Observe(time.Since(start).Seconds())
	}
	return nil
}

func safeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "client"
	}
	return b.String()
}

func (u Uploader) logger() *slog.Logger {
	if u.Logger != nil {
		return u.Logger
	}
	return slog.Default()
}

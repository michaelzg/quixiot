package server_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"quixiot/internal/client"
	"quixiot/internal/server"
	"quixiot/internal/tlsutil"
)

func TestServerEndpointsOverHTTP3(t *testing.T) {
	dir := t.TempDir()
	paths, err := tlsutil.GenerateLocal(dir, []string{"127.0.0.1", "localhost", "::1"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("GenerateLocal: %v", err)
	}
	tlsConf, err := tlsutil.LoadServerTLS(paths.Server, paths.ServerKey)
	if err != nil {
		t.Fatalf("LoadServerTLS: %v", err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	srv, err := server.New(server.Options{
		PacketConn: pc,
		TLSConfig:  tlsConf,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test-version",
		StartedAt:  time.Now().Add(-2 * time.Second).UTC(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-errCh; err != nil {
			t.Fatalf("Serve: %v", err)
		}
	})

	c, err := client.New(client.Options{
		BaseURL: "https://" + pc.LocalAddr().String(),
		CAFile:  paths.CA,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	var state client.State
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, err = c.GetState(context.Background())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GetState: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}

	if state.Version != "test-version" {
		t.Fatalf("state version: want test-version got %q", state.Version)
	}
	if state.UptimeMillis <= 0 {
		t.Fatalf("state uptime_millis: want >0 got %d", state.UptimeMillis)
	}

	cfg, err := c.GetConfig(context.Background(), "device-123")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg.ClientID != "device-123" {
		t.Fatalf("config client_id: want device-123 got %q", cfg.ClientID)
	}
	if cfg.TelemetryTopic != "clients/device-123/telemetry" {
		t.Fatalf("telemetry topic mismatch: %q", cfg.TelemetryTopic)
	}
	if cfg.CommandTopic != "clients/device-123/commands" {
		t.Fatalf("command topic mismatch: %q", cfg.CommandTopic)
	}
}

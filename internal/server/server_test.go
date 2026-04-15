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
		UploadDir:  t.TempDir(),
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

	result, err := c.UploadDeterministic(context.Background(), "device-123-000.bin", 4096, 7)
	if err != nil {
		t.Fatalf("UploadDeterministic: %v", err)
	}
	if result.Bytes != 4096 {
		t.Fatalf("upload bytes: want 4096 got %d", result.Bytes)
	}

	sub, err := c.ConnectPubSub(context.Background(), "device-123-sub")
	if err != nil {
		t.Fatalf("ConnectPubSub subscriber: %v", err)
	}
	defer sub.Close()
	pub, err := c.ConnectPubSub(context.Background(), "device-123-pub")
	if err != nil {
		t.Fatalf("ConnectPubSub publisher: %v", err)
	}
	defer pub.Close()

	if err := sub.Subscribe(context.Background(), cfg.TelemetryTopic); err != nil {
		t.Fatalf("Subscribe telemetry: %v", err)
	}
	if err := sub.Subscribe(context.Background(), cfg.CommandTopic); err != nil {
		t.Fatalf("Subscribe command: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := pub.PublishDatagram(cfg.TelemetryTopic, []byte("temp=21")); err != nil {
		t.Fatalf("PublishDatagram: %v", err)
	}
	if err := pub.PublishStream(context.Background(), cfg.CommandTopic, []byte("reboot")); err != nil {
		t.Fatalf("PublishStream: %v", err)
	}

	deadline = time.Now().Add(3 * time.Second)
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case msg, ok := <-sub.Messages():
			if !ok {
				t.Fatal("subscriber messages closed early")
			}
			switch msg.Topic {
			case cfg.TelemetryTopic:
				if string(msg.Payload) == "temp=21" {
					seen[msg.Topic] = true
				}
			case cfg.CommandTopic:
				if string(msg.Payload) == "reboot" {
					seen[msg.Topic] = true
				}
			}
		default:
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for pubsub messages; seen=%v", seen)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

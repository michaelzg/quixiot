package server_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"quixiot/internal/client"
	"quixiot/internal/metrics"
	"quixiot/internal/server"
	"quixiot/internal/tlsutil"
)

func TestClientReusesSingleQUICConnectionAcrossHTTPAndPubSub(t *testing.T) {
	serverMetrics := metrics.NewServer()
	_, baseURL, caFile := startMetricsServer(t, serverMetrics)

	c, err := client.New(client.Options{
		BaseURL: baseURL,
		CAFile:  caFile,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	waitForServerReady(t, c)

	if _, err := c.GetConfig(context.Background(), "device-123"); err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if _, err := c.UploadDeterministic(context.Background(), "device-123-000.bin", 1024, 7); err != nil {
		t.Fatalf("UploadDeterministic: %v", err)
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

	waitForCondition(t, 5*time.Second, func() bool {
		return testutil.ToFloat64(serverMetrics.ConnectionsTotal) == 1 &&
			testutil.ToFloat64(serverMetrics.ConnectionsActive) == 1
	}, "client never converged to a single shared QUIC connection")
}

func TestServerMetricsReachHundredConcurrentConnections(t *testing.T) {
	serverMetrics := metrics.NewServer()
	_, baseURL, caFile := startMetricsServer(t, serverMetrics)

	clients := make([]*client.Client, 0, 100)
	sessions := make([]*client.PubSubSession, 0, 100)
	t.Cleanup(func() {
		for _, session := range sessions {
			_ = session.Close()
		}
		for _, c := range clients {
			_ = c.Close()
		}
	})

	for i := 0; i < 100; i++ {
		c, err := client.New(client.Options{
			BaseURL: baseURL,
			CAFile:  caFile,
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err != nil {
			t.Fatalf("client.New %d: %v", i, err)
		}
		clients = append(clients, c)
	}

	waitForServerReady(t, clients[0])

	for i, c := range clients {
		session, err := c.ConnectPubSub(context.Background(), "fleet-"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("ConnectPubSub %d: %v", i, err)
		}
		sessions = append(sessions, session)
	}

	waitForCondition(t, 10*time.Second, func() bool {
		return testutil.ToFloat64(serverMetrics.ConnectionsActive) >= 100
	}, "active connections metric never reached 100")
}

func startMetricsServer(t *testing.T, serverMetrics *metrics.ServerMetrics) (*server.Server, string, string) {
	t.Helper()

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
		Version:    "metrics-test",
		StartedAt:  time.Now().UTC(),
		UploadDir:  t.TempDir(),
		Metrics:    serverMetrics,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
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

	return srv, "https://" + pc.LocalAddr().String(), paths.CA
}

func waitForServerReady(t *testing.T, c *client.Client) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	var readyErr error
	for {
		if _, err := c.GetState(context.Background()); err == nil {
			return
		} else {
			readyErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become ready: %v", readyErr)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool, failure string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		if fn() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(failure)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

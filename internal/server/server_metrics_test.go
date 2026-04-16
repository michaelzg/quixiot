package server_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"quixiot/internal/client"
	"quixiot/internal/metrics"
	"quixiot/internal/server"
	"quixiot/internal/tlsutil"
)

func TestServerMetricsReachHundredConcurrentConnections(t *testing.T) {
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
		Metrics:    metrics.NewServer(),
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

	baseURL := "https://" + pc.LocalAddr().String()
	pubsubClient, err := client.New(client.Options{
		BaseURL: baseURL,
		CAFile:  paths.CA,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	t.Cleanup(func() { _ = pubsubClient.Close() })

	readyDeadline := time.Now().Add(3 * time.Second)
	var readyErr error
	for {
		if _, err := pubsubClient.GetState(context.Background()); err == nil {
			break
		} else {
			readyErr = err
		}
		if time.Now().After(readyDeadline) {
			t.Fatalf("server did not become ready: %v", readyErr)
		}
		time.Sleep(25 * time.Millisecond)
	}

	sessions := make([]*client.PubSubSession, 0, 100)
	for i := 0; i < 100; i++ {
		session, err := pubsubClient.ConnectPubSub(context.Background(), "fleet-"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("ConnectPubSub %d: %v", i, err)
		}
		sessions = append(sessions, session)
	}
	t.Cleanup(func() {
		for _, session := range sessions {
			_ = session.Close()
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		body, err := fetchHTTP3(t, baseURL+"/metrics", paths.CA)
		if err == nil {
			active, ok := metricValue(body, "quixiot_server_connections_active")
			if ok && active >= 100 {
				return
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("fetch metrics: %v", err)
			}
			t.Fatalf("active connections metric never reached 100")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func fetchHTTP3(t *testing.T, rawURL string, caFile string) (string, error) {
	t.Helper()

	tlsConf, err := tlsutil.LoadClientTrust(caFile)
	if err != nil {
		return "", err
	}
	transport := &http3.Transport{
		TLSClientConfig: tlsConf,
	}
	defer transport.Close()

	httpClient := &http.Client{Transport: transport}
	resp, err := httpClient.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}
	return string(body), nil
}

func metricValue(body string, name string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+" ") {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, name)), 64)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}

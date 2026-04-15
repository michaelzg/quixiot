package proxy_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"quixiot/internal/client"
	"quixiot/internal/proxy"
	"quixiot/internal/server"
	"quixiot/internal/tlsutil"
)

func TestProxyForwardsHTTP3AndUpload(t *testing.T) {
	dir := t.TempDir()
	paths, err := tlsutil.GenerateLocal(dir, []string{"127.0.0.1", "localhost", "::1"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("GenerateLocal: %v", err)
	}
	tlsConf, err := tlsutil.LoadServerTLS(paths.Server, paths.ServerKey)
	if err != nil {
		t.Fatalf("LoadServerTLS: %v", err)
	}

	backendConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket backend: %v", err)
	}
	t.Cleanup(func() { _ = backendConn.Close() })

	srv, err := server.New(server.Options{
		PacketConn: backendConn,
		TLSConfig:  tlsConf,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "through-proxy",
		StartedAt:  time.Now().Add(-2 * time.Second).UTC(),
		UploadDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	serverCtx, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Serve(serverCtx)
	}()
	t.Cleanup(func() {
		stopServer()
		if err := <-serverErr; err != nil {
			t.Fatalf("server.Serve: %v", err)
		}
	})

	listenAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ResolveUDPAddr listen: %v", err)
	}
	listenConn, err := net.ListenUDP("udp", listenAddr)
	if err != nil {
		t.Fatalf("ListenUDP proxy: %v", err)
	}

	backendUDP, err := net.ResolveUDPAddr("udp", backendConn.LocalAddr().String())
	if err != nil {
		t.Fatalf("ResolveUDPAddr backend: %v", err)
	}
	p, err := proxy.New(proxy.Options{
		ListenConn:   listenConn,
		UpstreamAddr: backendUDP,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		IdleTimeout:  time.Minute,
	})
	if err != nil {
		t.Fatalf("proxy.New: %v", err)
	}

	proxyCtx, stopProxy := context.WithCancel(context.Background())
	defer stopProxy()
	proxyErr := make(chan error, 1)
	go func() {
		proxyErr <- p.Serve(proxyCtx)
	}()
	t.Cleanup(func() {
		stopProxy()
		if err := <-proxyErr; err != nil {
			t.Fatalf("proxy.Serve: %v", err)
		}
	})

	c, err := client.New(client.Options{
		BaseURL: "https://" + p.Addr().String(),
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
			t.Fatalf("GetState through proxy: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if state.Version != "through-proxy" {
		t.Fatalf("state version: want through-proxy got %q", state.Version)
	}

	cfg, err := c.GetConfig(context.Background(), "device-123")
	if err != nil {
		t.Fatalf("GetConfig through proxy: %v", err)
	}
	if cfg.ClientID != "device-123" {
		t.Fatalf("config client_id: want device-123 got %q", cfg.ClientID)
	}

	result, err := c.UploadDeterministic(context.Background(), "proxy-upload.bin", 4096, 11)
	if err != nil {
		t.Fatalf("UploadDeterministic through proxy: %v", err)
	}
	if result.Bytes != 4096 {
		t.Fatalf("upload bytes: want 4096 got %d", result.Bytes)
	}
}

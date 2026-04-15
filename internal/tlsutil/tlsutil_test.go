package tlsutil_test

import (
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"

	"quixiot/internal/tlsutil"
)

// TestGenerateLocalRoundTrip generates a local CA + server leaf, spins up a
// TLS listener using the leaf, connects a client that trusts only the CA, and
// verifies a short echo. Negative assertions: ALPN is "h3"; TLS version is 1.3.
func TestGenerateLocalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	paths, err := tlsutil.GenerateLocal(dir,
		[]string{"127.0.0.1", "localhost", "::1"},
		24*time.Hour,
	)
	if err != nil {
		t.Fatalf("GenerateLocal: %v", err)
	}

	srvCfg, err := tlsutil.LoadServerTLS(paths.Server, paths.ServerKey)
	if err != nil {
		t.Fatalf("LoadServerTLS: %v", err)
	}
	cliCfg, err := tlsutil.LoadClientTrust(paths.CA)
	if err != nil {
		t.Fatalf("LoadClientTrust: %v", err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			accepted <- err
			return
		}
		defer c.Close()
		// Echo a single 4-byte message to prove the session is usable.
		_, err = io.CopyN(c, c, 4)
		accepted <- err
	}()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	cliCfg = cliCfg.Clone()
	cliCfg.ServerName = "localhost"
	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", port), cliCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo mismatch: got %q", buf)
	}

	state := conn.ConnectionState()
	if state.NegotiatedProtocol != tlsutil.ALPN {
		t.Errorf("ALPN: want %q got %q", tlsutil.ALPN, state.NegotiatedProtocol)
	}
	if state.Version != tls.VersionTLS13 {
		t.Errorf("TLS version: want 1.3 got 0x%04x", state.Version)
	}

	if err := <-accepted; err != nil && err != io.EOF {
		t.Errorf("server echo: %v", err)
	}
}

// TestClientTrustRejectsUnknownLeaf verifies LoadClientTrust's pool really does
// constrain the trust anchor: a leaf signed by a different CA must fail.
func TestClientTrustRejectsUnknownLeaf(t *testing.T) {
	dirA := t.TempDir()
	if _, err := tlsutil.GenerateLocal(dirA, []string{"127.0.0.1", "localhost"}, 24*time.Hour); err != nil {
		t.Fatalf("generate A: %v", err)
	}
	dirB := t.TempDir()
	pathsB, err := tlsutil.GenerateLocal(dirB, []string{"127.0.0.1", "localhost"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("generate B: %v", err)
	}

	// Client trusts A's CA; server serves B's leaf → handshake must fail.
	cliCfg, err := tlsutil.LoadClientTrust(tlsutil.PathsIn(dirA).CA)
	if err != nil {
		t.Fatalf("LoadClientTrust: %v", err)
	}
	srvCfg, err := tlsutil.LoadServerTLS(pathsB.Server, pathsB.ServerKey)
	if err != nil {
		t.Fatalf("LoadServerTLS: %v", err)
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close()
		}
	}()

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	cliCfg = cliCfg.Clone()
	cliCfg.ServerName = "localhost"
	_, err = tls.Dial("tcp", net.JoinHostPort("127.0.0.1", port), cliCfg)
	if err == nil {
		t.Fatal("expected handshake failure against unknown CA")
	}
}

package proxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestUDPAddrToAddrPortPreservesIPv6Zone(t *testing.T) {
	addr := &net.UDPAddr{
		IP:   net.ParseIP("fe80::1"),
		Port: 4443,
		Zone: "en0",
	}

	got, err := udpAddrToAddrPort(addr)
	if err != nil {
		t.Fatalf("udpAddrToAddrPort: %v", err)
	}

	want := netip.MustParseAddrPort("[fe80::1%en0]:4443")
	if got != want {
		t.Fatalf("addrport: want %s got %s", want, got)
	}
}

func TestProxyContinuesAfterSessionCreationFailure(t *testing.T) {
	listenConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}

	p, err := New(Options{
		ListenConn:   listenConn,
		UpstreamAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4444},
		DialUDP: func(network string, laddr, raddr *net.UDPAddr) (*net.UDPConn, error) {
			return nil, fmt.Errorf("boom")
		},
		IdleTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- p.Serve(ctx)
	}()

	clientConn, err := net.DialUDP("udp", nil, p.Addr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP client: %v", err)
	}
	defer clientConn.Close()

	if _, err := clientConn.Write([]byte("probe")); err != nil {
		t.Fatalf("write probe: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not exit after cancel")
	}
}

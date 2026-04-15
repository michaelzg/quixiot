package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ReadBufferSize = 8 * 1024 * 1024
	defaultIdleTTL = 5 * time.Minute
	sweepInterval  = time.Minute
	maxPacketSize  = 64 * 1024
	queueDepth     = 256
)

type Options struct {
	ListenConn   *net.UDPConn
	UpstreamAddr *net.UDPAddr
	Logger       *slog.Logger
	IdleTimeout  time.Duration
	DialUDP      func(network string, laddr, raddr *net.UDPAddr) (*net.UDPConn, error)
}

type Proxy struct {
	listenConn   *net.UDPConn
	upstreamAddr *net.UDPAddr
	logger       *slog.Logger
	idleTimeout  time.Duration
	dialUDP      func(network string, laddr, raddr *net.UDPAddr) (*net.UDPConn, error)

	sessionsMu sync.Mutex
	sessions   map[netip.AddrPort]*session

	closeOnce sync.Once
	closed    chan struct{}
}

type packet struct {
	data []byte
}

type session struct {
	owner        *Proxy
	key          netip.AddrPort
	clientAddr   *net.UDPAddr
	listenConn   *net.UDPConn
	upstreamAddr *net.UDPAddr
	upstreamConn *net.UDPConn
	logger       *slog.Logger

	incoming chan packet
	toServer chan packet
	toClient chan packet

	lastSeen  atomic.Int64
	closeOnce sync.Once
	done      chan struct{}
}

func New(opts Options) (*Proxy, error) {
	if opts.ListenConn == nil {
		return nil, fmt.Errorf("proxy: listen conn is required")
	}
	if opts.UpstreamAddr == nil {
		return nil, fmt.Errorf("proxy: upstream addr is required")
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	idleTimeout := opts.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = defaultIdleTTL
	}

	if err := opts.ListenConn.SetReadBuffer(ReadBufferSize); err != nil {
		return nil, fmt.Errorf("proxy: set listen read buffer: %w", err)
	}

	return &Proxy{
		listenConn:   opts.ListenConn,
		upstreamAddr: opts.UpstreamAddr,
		logger:       log,
		idleTimeout:  idleTimeout,
		dialUDP:      pickDialUDP(opts.DialUDP),
		sessions:     make(map[netip.AddrPort]*session),
		closed:       make(chan struct{}),
	}, nil
}

func (p *Proxy) Addr() net.Addr {
	return p.listenConn.LocalAddr()
}

func (p *Proxy) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = p.Close()
	}()
	go p.sweepLoop(ctx)

	buf := make([]byte, maxPacketSize)
	for {
		n, clientAddr, err := p.listenConn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil || p.isClosed() {
				return nil
			}
			return fmt.Errorf("proxy: read client packet: %w", err)
		}

		key, err := udpAddrToAddrPort(clientAddr)
		if err != nil {
			p.logger.Warn("drop packet with unparseable client addr", "remote", clientAddr.String(), "error", err)
			continue
		}

		sess, err := p.getOrCreateSession(key, clientAddr)
		if err != nil {
			p.logger.Warn("drop client packet: failed to create proxy session",
				"client", key.String(),
				"upstream", p.upstreamAddr.String(),
				"error", err,
			)
			continue
		}
		if err := sess.enqueueIncoming(copyPacket(buf[:n])); err != nil {
			if p.isClosed() {
				return nil
			}
			p.logger.Warn("drop client packet for closed session", "client", key.String(), "error", err)
		}
	}
}

func (p *Proxy) Close() error {
	var closeErr error
	p.closeOnce.Do(func() {
		close(p.closed)

		p.sessionsMu.Lock()
		sessions := make([]*session, 0, len(p.sessions))
		for key, sess := range p.sessions {
			delete(p.sessions, key)
			sessions = append(sessions, sess)
		}
		p.sessionsMu.Unlock()

		for _, sess := range sessions {
			if err := sess.close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		if err := p.listenConn.Close(); err != nil && closeErr == nil && !errors.Is(err, net.ErrClosed) {
			closeErr = err
		}
	})
	return closeErr
}

func (p *Proxy) getOrCreateSession(key netip.AddrPort, clientAddr *net.UDPAddr) (*session, error) {
	p.sessionsMu.Lock()
	if sess, ok := p.sessions[key]; ok {
		p.sessionsMu.Unlock()
		return sess, nil
	}
	p.sessionsMu.Unlock()

	upstreamConn, err := p.dialUDP("udp", nil, p.upstreamAddr)
	if err != nil {
		return nil, fmt.Errorf("proxy: dial upstream for %s: %w", key, err)
	}
	if err := upstreamConn.SetReadBuffer(ReadBufferSize); err != nil {
		_ = upstreamConn.Close()
		return nil, fmt.Errorf("proxy: set upstream read buffer: %w", err)
	}

	sess := &session{
		owner:        p,
		key:          key,
		clientAddr:   cloneUDPAddr(clientAddr),
		listenConn:   p.listenConn,
		upstreamAddr: p.upstreamAddr,
		upstreamConn: upstreamConn,
		logger:       p.logger.With("client", key.String(), "upstream", p.upstreamAddr.String()),
		incoming:     make(chan packet, queueDepth),
		toServer:     make(chan packet, queueDepth),
		toClient:     make(chan packet, queueDepth),
		done:         make(chan struct{}),
	}
	sess.touch()

	p.sessionsMu.Lock()
	if existing, ok := p.sessions[key]; ok {
		p.sessionsMu.Unlock()
		_ = sess.close()
		return existing, nil
	}
	p.sessions[key] = sess
	p.sessionsMu.Unlock()

	go sess.acceptLoop()
	go sess.toServerLoop()
	go sess.returnReadLoop()
	go sess.toClientLoop()

	p.logger.Info("opened proxy session", "client", key.String(), "upstream", p.upstreamAddr.String())
	return sess, nil
}

func (p *Proxy) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.closed:
			return
		case <-ticker.C:
			var idle []*session

			p.sessionsMu.Lock()
			for key, sess := range p.sessions {
				if sess.idleFor() > p.idleTimeout {
					delete(p.sessions, key)
					idle = append(idle, sess)
				}
			}
			p.sessionsMu.Unlock()

			for _, sess := range idle {
				p.logger.Info("evict idle proxy session", "client", sess.key.String(), "idle_for", sess.idleFor().String())
				_ = sess.close()
			}
		}
	}
}

func (p *Proxy) isClosed() bool {
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

func (s *session) acceptLoop() {
	for {
		select {
		case <-s.done:
			return
		case pkt := <-s.incoming:
			s.touch()
			select {
			case <-s.done:
				return
			case s.toServer <- pkt:
			}
		}
	}
}

func (s *session) toServerLoop() {
	for {
		select {
		case <-s.done:
			return
		case pkt := <-s.toServer:
			if _, err := s.upstreamConn.Write(pkt.data); err != nil {
				if !errors.Is(err, net.ErrClosed) {
					s.logger.Warn("write to upstream failed", "error", err)
				}
				_ = s.owner.removeSession(s.key, s)
				return
			}
			s.touch()
		}
	}
}

func (s *session) returnReadLoop() {
	buf := make([]byte, maxPacketSize)
	for {
		n, err := s.upstreamConn.Read(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.logger.Warn("read from upstream failed", "error", err)
			}
			_ = s.owner.removeSession(s.key, s)
			return
		}

		s.touch()
		select {
		case <-s.done:
			return
		case s.toClient <- packet{data: copyPacket(buf[:n])}:
		}
	}
}

func (s *session) toClientLoop() {
	for {
		select {
		case <-s.done:
			return
		case pkt := <-s.toClient:
			if _, err := s.listenConn.WriteToUDP(pkt.data, s.clientAddr); err != nil {
				if !errors.Is(err, net.ErrClosed) {
					s.logger.Warn("write to client failed", "error", err)
				}
				_ = s.owner.removeSession(s.key, s)
				return
			}
			s.touch()
		}
	}
}

func (s *session) enqueueIncoming(pkt []byte) error {
	select {
	case <-s.done:
		return os.ErrClosed
	case s.incoming <- packet{data: pkt}:
		return nil
	}
}

func (s *session) close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		close(s.done)
		if err := s.upstreamConn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			closeErr = err
		}
	})
	return closeErr
}

func (p *Proxy) removeSession(key netip.AddrPort, sess *session) error {
	p.sessionsMu.Lock()
	if current, ok := p.sessions[key]; ok && current == sess {
		delete(p.sessions, key)
	}
	p.sessionsMu.Unlock()
	return sess.close()
}

func (s *session) touch() {
	s.lastSeen.Store(time.Now().UnixNano())
}

func (s *session) idleFor() time.Duration {
	last := time.Unix(0, s.lastSeen.Load())
	return time.Since(last)
}

func udpAddrToAddrPort(addr *net.UDPAddr) (netip.AddrPort, error) {
	if addr == nil {
		return netip.AddrPort{}, fmt.Errorf("nil UDP addr")
	}
	ip, ok := netip.AddrFromSlice(addr.IP)
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("invalid IP %v", addr.IP)
	}
	if addr.Zone != "" {
		ip = ip.WithZone(addr.Zone)
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(addr.Port)), nil
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	ip := make(net.IP, len(addr.IP))
	copy(ip, addr.IP)
	return &net.UDPAddr{
		IP:   ip,
		Port: addr.Port,
		Zone: addr.Zone,
	}
}

func copyPacket(src []byte) []byte {
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

func pickDialUDP(fn func(network string, laddr, raddr *net.UDPAddr) (*net.UDPConn, error)) func(network string, laddr, raddr *net.UDPAddr) (*net.UDPConn, error) {
	if fn != nil {
		return fn
	}
	return net.DialUDP
}

package proxy

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"quixiot/internal/impair"
	"quixiot/internal/metrics"
)

const (
	ReadBufferSize = 8 * 1024 * 1024
	defaultIdleTTL = 5 * time.Minute
	sweepInterval  = time.Minute
	maxPacketSize  = 64 * 1024
	queueDepth     = 256
)

var errSessionQueueFull = errors.New("proxy: session ingress queue full")

type Options struct {
	ListenConn   *net.UDPConn
	UpstreamAddr *net.UDPAddr
	Logger       *slog.Logger
	IdleTimeout  time.Duration
	DialUDP      func(network string, laddr, raddr *net.UDPAddr) (*net.UDPConn, error)
	Profile      impair.Profile
	Metrics      *metrics.ProxyMetrics
}

type Proxy struct {
	listenConn   *net.UDPConn
	upstreamAddr *net.UDPAddr
	logger       *slog.Logger
	idleTimeout  time.Duration
	dialUDP      func(network string, laddr, raddr *net.UDPAddr) (*net.UDPConn, error)
	profile      impair.Profile
	metrics      *metrics.ProxyMetrics

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

	toServerPipeline *impair.Pipeline
	toClientPipeline *impair.Pipeline

	lastSeen  atomic.Int64
	closeOnce sync.Once
	done      chan struct{}
	onClose   func()
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
	profile, err := impair.NormalizeProfile(opts.Profile)
	if err != nil {
		return nil, err
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
		profile:      profile,
		metrics:      opts.Metrics,
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
		if p.metrics != nil {
			p.metrics.Packets.WithLabelValues("to_server").Inc()
			p.metrics.Bytes.WithLabelValues("to_server").Add(float64(n))
		}
		if err := sess.enqueueIncoming(copyPacket(buf[:n])); err != nil {
			if p.isClosed() {
				return nil
			}
			if errors.Is(err, errSessionQueueFull) {
				p.logger.Warn("drop client packet for overloaded session", "client", key.String(), "error", err)
				continue
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
	toServerPipeline, err := impair.NewPipelineWithHooks(p.profile.ToServer, sessionSeed(p.profile.Seed, key, "to-server"), p.pipelineHooks("to_server"))
	if err != nil {
		_ = upstreamConn.Close()
		return nil, err
	}
	toClientPipeline, err := impair.NewPipelineWithHooks(p.profile.ToClient, sessionSeed(p.profile.Seed, key, "to-client"), p.pipelineHooks("to_client"))
	if err != nil {
		_ = upstreamConn.Close()
		return nil, err
	}

	sess := &session{
		owner:            p,
		key:              key,
		clientAddr:       cloneUDPAddr(clientAddr),
		listenConn:       p.listenConn,
		upstreamAddr:     p.upstreamAddr,
		upstreamConn:     upstreamConn,
		logger:           p.logger.With("client", key.String(), "upstream", p.upstreamAddr.String()),
		incoming:         make(chan packet, queueDepth),
		toServer:         make(chan packet, queueDepth),
		toClient:         make(chan packet, queueDepth),
		toServerPipeline: toServerPipeline,
		toClientPipeline: toClientPipeline,
		done:             make(chan struct{}),
	}
	if p.metrics != nil {
		p.metrics.SessionsActive.Inc()
		p.metrics.SessionsTotal.Inc()
		sess.onClose = func() {
			p.metrics.SessionsActive.Dec()
		}
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
	s.applyLoop(s.toServer, s.toServerPipeline, func(data []byte) error {
		_, err := s.upstreamConn.Write(data)
		return err
	}, "write to upstream failed")
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
		if s.owner.metrics != nil {
			s.owner.metrics.Packets.WithLabelValues("to_client").Inc()
			s.owner.metrics.Bytes.WithLabelValues("to_client").Add(float64(n))
		}
		select {
		case <-s.done:
			return
		case s.toClient <- packet{data: copyPacket(buf[:n])}:
		}
	}
}

func (s *session) toClientLoop() {
	s.applyLoop(s.toClient, s.toClientPipeline, func(data []byte) error {
		_, err := s.listenConn.WriteToUDP(data, s.clientAddr)
		return err
	}, "write to client failed")
}

func (s *session) applyLoop(input <-chan packet, pipeline *impair.Pipeline, write func([]byte) error, logMessage string) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	defer timer.Stop()

	timerCh := (<-chan time.Time)(nil)
	for {
		if !s.flushReady(pipeline, write, logMessage) {
			return
		}
		timerCh = resetTimer(timer, timerCh, pipeline)

		select {
		case <-s.done:
			pipeline.Flush(time.Now())
			_ = s.flushReady(pipeline, write, logMessage)
			return
		case pkt := <-input:
			pipeline.Enqueue(time.Now(), pkt.data)
		case <-timerCh:
		}
	}
}

func (s *session) flushReady(pipeline *impair.Pipeline, write func([]byte) error, logMessage string) bool {
	for _, data := range pipeline.ReleaseReady(time.Now()) {
		if err := write(data); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.logger.Warn(logMessage, "error", err)
			}
			_ = s.owner.removeSession(s.key, s)
			return false
		}
		s.touch()
	}
	return true
}

func resetTimer(timer *time.Timer, current <-chan time.Time, pipeline *impair.Pipeline) <-chan time.Time {
	next, ok := pipeline.NextWake()
	if !ok {
		if current != nil && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		return nil
	}
	if current != nil && !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	delay := time.Until(next)
	if delay < 0 {
		delay = 0
	}
	timer.Reset(delay)
	return timer.C
}

func (s *session) enqueueIncoming(pkt []byte) error {
	select {
	case <-s.done:
		return os.ErrClosed
	case s.incoming <- packet{data: pkt}:
		return nil
	default:
		return errSessionQueueFull
	}
}

func (s *session) close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		close(s.done)
		if s.onClose != nil {
			s.onClose()
		}
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

func sessionSeed(base int64, key netip.AddrPort, direction string) int64 {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(key.String()))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(direction))
	return int64(hasher.Sum64() ^ uint64(base))
}

func (p *Proxy) pipelineHooks(direction string) impair.Hooks {
	if p.metrics == nil {
		return impair.Hooks{}
	}
	return impair.Hooks{
		OnDrop: func() {
			p.metrics.Drops.WithLabelValues(direction).Inc()
		},
		OnDuplicate: func() {
			p.metrics.Duplicates.WithLabelValues(direction).Inc()
		},
		OnReorder: func() {
			p.metrics.Reorders.WithLabelValues(direction).Inc()
		},
		OnDelay: func(delay time.Duration) {
			if delay > 0 {
				p.metrics.EnforcedDelay.WithLabelValues(direction).Observe(delay.Seconds())
			}
		},
	}
}

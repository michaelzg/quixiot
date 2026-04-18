package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/dunglas/httpsfv"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/quic-go/quicvarint"
)

const (
	wtAvailableProtocolsHeader = "WT-Available-Protocols"
	wtProtocolHeader           = "WT-Protocol"
	webTransportFrameType      = 0x41
	webTransportUniStreamType  = 0x54
	wtSessionGoneErrorCode     = quic.StreamErrorCode(0x170d7b68)
)

var errClientClosed = errors.New("client: closed")

type clientRoundTripper struct {
	client *Client
}

func (rt clientRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	shared, err := rt.client.getSharedConn(req.Context())
	if err != nil {
		return nil, err
	}
	return shared.raw.RoundTrip(req)
}

type pubsubTransportSession interface {
	Context() context.Context
	OpenStreamSync(context.Context) (*quic.Stream, error)
	AcceptUniStream(context.Context) (*quic.ReceiveStream, error)
	SendDatagram([]byte) error
	ReceiveDatagram(context.Context) ([]byte, error)
	Close() error
}

type sharedConn struct {
	client *Client
	conn   *quic.Conn
	raw    *http3.RawClientConn

	closeOnce sync.Once

	sessionsMu sync.Mutex
	sessions   map[quic.StreamID]*pubsubTransportSessionImpl
}

type pubsubTransportSessionImpl struct {
	shared    *sharedConn
	sessionID quic.StreamID
	request   *http3.RequestStream
	streamHdr []byte

	ctx    context.Context
	cancel context.CancelCauseFunc

	closeOnce sync.Once
	uniQueue  chan *quic.ReceiveStream
}

func (c *Client) getSharedConn(ctx context.Context) (*sharedConn, error) {
	c.sharedMu.Lock()
	defer c.sharedMu.Unlock()

	if c.closed {
		return nil, errClientClosed
	}
	if c.shared != nil && c.shared.isClosed() {
		c.shared = nil
	}
	if c.shared != nil {
		return c.shared, nil
	}

	shared, err := newSharedConn(ctx, c)
	if err != nil {
		return nil, err
	}
	c.shared = shared
	return shared, nil
}

func newSharedConn(ctx context.Context, c *Client) (*sharedConn, error) {
	tlsConf, err := c.sharedTLSConfig()
	if err != nil {
		return nil, err
	}

	conn, err := c.dialQUIC(ctx, c.authority, tlsConf, quicConfig())
	if err != nil {
		return nil, fmt.Errorf("client: dial shared QUIC: %w", err)
	}

	shared := &sharedConn{
		client:   c,
		conn:     conn,
		raw:      (&http3.Transport{EnableDatagrams: true, Logger: c.logger}).NewRawClientConn(conn),
		sessions: make(map[quic.StreamID]*pubsubTransportSessionImpl),
	}
	go shared.acceptBidirectionalStreams()
	go shared.acceptUnidirectionalStreams()

	select {
	case <-shared.raw.ReceivedSettings():
	case <-ctx.Done():
		_ = shared.close()
		return nil, fmt.Errorf("client: wait for HTTP/3 settings: %w", context.Cause(ctx))
	case <-conn.Context().Done():
		_ = shared.close()
		return nil, fmt.Errorf("client: wait for HTTP/3 settings: %w", context.Cause(conn.Context()))
	}

	settings := shared.raw.Settings()
	if !settings.EnableExtendedConnect {
		_ = shared.close()
		return nil, fmt.Errorf("client: server did not enable extended CONNECT")
	}
	if !settings.EnableDatagrams {
		_ = shared.close()
		return nil, fmt.Errorf("client: server did not enable HTTP/3 datagrams")
	}
	return shared, nil
}

func (c *Client) sharedTLSConfig() (*tls.Config, error) {
	tlsConf := c.tlsConfig.Clone()
	if tlsConf == nil {
		return nil, fmt.Errorf("client: missing TLS config")
	}

	serverName := c.authority
	if host, _, err := net.SplitHostPort(serverName); err == nil {
		serverName = host
	}
	if tlsConf.ServerName == "" {
		tlsConf.ServerName = serverName
	}
	tlsConf.NextProtos = []string{http3.NextProtoH3}
	return tlsConf, nil
}

func (sc *sharedConn) isClosed() bool {
	select {
	case <-sc.conn.Context().Done():
		return true
	default:
		return false
	}
}

func (sc *sharedConn) close() error {
	var closeErr error
	sc.closeOnce.Do(func() {
		sessions := sc.snapshotSessions()
		sc.sessionsMu.Lock()
		sc.sessions = nil
		sc.sessionsMu.Unlock()
		for _, session := range sessions {
			session.closeWithCause(context.Canceled)
		}
		if err := sc.raw.CloseWithError(0, ""); err != nil && closeErr == nil {
			closeErr = err
		}

		sc.client.sharedMu.Lock()
		if sc.client.shared == sc {
			sc.client.shared = nil
		}
		sc.client.sharedMu.Unlock()
	})
	return closeErr
}

func (sc *sharedConn) snapshotSessions() []*pubsubTransportSessionImpl {
	sc.sessionsMu.Lock()
	defer sc.sessionsMu.Unlock()

	if len(sc.sessions) == 0 {
		return nil
	}
	sessions := make([]*pubsubTransportSessionImpl, 0, len(sc.sessions))
	for _, session := range sc.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}

func (sc *sharedConn) acceptBidirectionalStreams() {
	for {
		str, err := sc.conn.AcceptStream(context.Background())
		if err != nil {
			_ = sc.close()
			return
		}
		go sc.handleBidirectionalStream(str)
	}
}

func (sc *sharedConn) acceptUnidirectionalStreams() {
	for {
		str, err := sc.conn.AcceptUniStream(context.Background())
		if err != nil {
			_ = sc.close()
			return
		}
		go sc.handleUnidirectionalStream(str)
	}
}

func (sc *sharedConn) handleBidirectionalStream(str *quic.Stream) {
	typ, err := quicvarint.Peek(str)
	if err != nil {
		return
	}
	if typ != webTransportFrameType {
		sc.raw.HandleBidirectionalStream(str)
		return
	}

	if _, err := quicvarint.Read(quicvarint.NewReader(str)); err != nil {
		str.CancelRead(quic.StreamErrorCode(http3.ErrCodeGeneralProtocolError))
		str.CancelWrite(quic.StreamErrorCode(http3.ErrCodeGeneralProtocolError))
		return
	}
	if _, err := quicvarint.Read(quicvarint.NewReader(str)); err != nil {
		str.CancelRead(quic.StreamErrorCode(http3.ErrCodeGeneralProtocolError))
		str.CancelWrite(quic.StreamErrorCode(http3.ErrCodeGeneralProtocolError))
		return
	}

	// Pubsub only expects incoming unidirectional delivery streams.
	str.CancelRead(wtSessionGoneErrorCode)
	str.CancelWrite(wtSessionGoneErrorCode)
}

func (sc *sharedConn) handleUnidirectionalStream(str *quic.ReceiveStream) {
	typ, err := quicvarint.Peek(str)
	if err != nil {
		return
	}
	if typ != webTransportUniStreamType {
		sc.raw.HandleUnidirectionalStream(str)
		return
	}

	if _, err := quicvarint.Read(quicvarint.NewReader(str)); err != nil {
		str.CancelRead(quic.StreamErrorCode(http3.ErrCodeGeneralProtocolError))
		return
	}
	sessionID, err := quicvarint.Read(quicvarint.NewReader(str))
	if err != nil {
		str.CancelRead(quic.StreamErrorCode(http3.ErrCodeGeneralProtocolError))
		return
	}

	session := sc.lookupSession(quic.StreamID(sessionID))
	if session == nil {
		str.CancelRead(wtSessionGoneErrorCode)
		return
	}
	session.enqueueUniStream(str)
}

func (sc *sharedConn) lookupSession(id quic.StreamID) *pubsubTransportSessionImpl {
	sc.sessionsMu.Lock()
	defer sc.sessionsMu.Unlock()
	return sc.sessions[id]
}

func (sc *sharedConn) registerSession(session *pubsubTransportSessionImpl) error {
	sc.sessionsMu.Lock()
	defer sc.sessionsMu.Unlock()

	if sc.sessions == nil {
		return errClientClosed
	}
	sc.sessions[session.sessionID] = session
	return nil
}

func (sc *sharedConn) unregisterSession(id quic.StreamID, session *pubsubTransportSessionImpl) {
	sc.sessionsMu.Lock()
	defer sc.sessionsMu.Unlock()

	if sc.sessions[id] == session {
		delete(sc.sessions, id)
	}
}

func (c *Client) connectPubSubTransport(ctx context.Context, clientID string) (pubsubTransportSession, *http.Response, error) {
	shared, err := c.getSharedConn(ctx)
	if err != nil {
		return nil, nil, err
	}
	return shared.connectPubSub(ctx, c.baseURL, clientID)
}

func (sc *sharedConn) connectPubSub(ctx context.Context, baseURL string, clientID string) (pubsubTransportSession, *http.Response, error) {
	hdr := make(http.Header)
	if clientID != "" {
		hdr.Set(clientIDHeader, clientID)
	}
	if err := addWTProtocolHeader(hdr, pubsubProtocol); err != nil {
		return nil, nil, fmt.Errorf("client: encode pubsub protocol header: %w", err)
	}

	u, err := url.Parse(baseURL + "/pubsub")
	if err != nil {
		return nil, nil, fmt.Errorf("client: parse pubsub URL: %w", err)
	}
	req := (&http.Request{
		Method: http.MethodConnect,
		Header: hdr,
		Proto:  "webtransport",
		Host:   u.Host,
		URL:    u,
	}).WithContext(ctx)

	request, err := sc.raw.OpenRequestStream(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("client: open pubsub request stream: %w", err)
	}
	if err := request.SendRequestHeader(req); err != nil {
		request.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = request.Close()
		return nil, nil, fmt.Errorf("client: send pubsub request headers: %w", err)
	}

	resp, err := request.ReadResponse()
	if err != nil {
		request.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = request.Close()
		return nil, nil, fmt.Errorf("client: read pubsub response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		request.CancelRead(quic.StreamErrorCode(http3.ErrCodeRequestCanceled))
		_ = request.Close()
		return nil, resp, nil
	}

	session := newPubSubTransportSession(sc, request)
	if err := sc.registerSession(session); err != nil {
		_ = session.Close()
		return nil, nil, err
	}
	go session.monitorRequestStream()
	return session, resp, nil
}

func newPubSubTransportSession(shared *sharedConn, request *http3.RequestStream) *pubsubTransportSessionImpl {
	ctx, cancel := context.WithCancelCause(request.Context())
	streamHdr := quicvarint.Append(nil, webTransportFrameType)
	streamHdr = quicvarint.Append(streamHdr, uint64(request.StreamID()))
	return &pubsubTransportSessionImpl{
		shared:    shared,
		sessionID: request.StreamID(),
		request:   request,
		streamHdr: streamHdr,
		ctx:       ctx,
		cancel:    cancel,
		uniQueue:  make(chan *quic.ReceiveStream, 256),
	}
}

func (s *pubsubTransportSessionImpl) monitorRequestStream() {
	_, err := io.Copy(io.Discard, s.request)
	if err != nil && !errors.Is(err, io.EOF) {
		s.closeWithCause(err)
		return
	}
	s.closeWithCause(context.Canceled)
}

func (s *pubsubTransportSessionImpl) Context() context.Context {
	return s.ctx
}

func (s *pubsubTransportSessionImpl) OpenStreamSync(ctx context.Context) (*quic.Stream, error) {
	str, err := s.shared.conn.OpenStreamSync(ctx)
	if err != nil {
		if cause := context.Cause(s.ctx); cause != nil {
			return nil, cause
		}
		return nil, err
	}
	if err := writeWebTransportStreamHeader(str, s.streamHdr); err != nil {
		str.CancelRead(wtSessionGoneErrorCode)
		str.CancelWrite(wtSessionGoneErrorCode)
		return nil, err
	}
	return str, nil
}

func (s *pubsubTransportSessionImpl) AcceptUniStream(ctx context.Context) (*quic.ReceiveStream, error) {
	if err := context.Cause(s.ctx); err != nil {
		return nil, err
	}
	select {
	case str, ok := <-s.uniQueue:
		if !ok {
			return nil, sessionCause(s.ctx)
		}
		return str, nil
	case <-s.ctx.Done():
		return nil, sessionCause(s.ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *pubsubTransportSessionImpl) SendDatagram(b []byte) error {
	return s.request.SendDatagram(b)
}

func (s *pubsubTransportSessionImpl) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	return s.request.ReceiveDatagram(ctx)
}

func (s *pubsubTransportSessionImpl) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		s.shared.unregisterSession(s.sessionID, s)
		s.cancel(context.Canceled)
		close(s.uniQueue)
		s.request.CancelRead(wtSessionGoneErrorCode)
		if err := s.request.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	})
	return closeErr
}

func (s *pubsubTransportSessionImpl) closeWithCause(cause error) {
	if cause == nil || errors.Is(cause, io.EOF) {
		cause = context.Canceled
	}
	s.closeOnce.Do(func() {
		s.shared.unregisterSession(s.sessionID, s)
		s.cancel(cause)
		close(s.uniQueue)
	})
}

func (s *pubsubTransportSessionImpl) enqueueUniStream(str *quic.ReceiveStream) {
	select {
	case <-s.ctx.Done():
		str.CancelRead(wtSessionGoneErrorCode)
	case s.uniQueue <- str:
	}
}

func sessionCause(ctx context.Context) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return context.Canceled
}

func writeWebTransportStreamHeader(str *quic.Stream, hdr []byte) error {
	if _, err := str.Write(hdr); err != nil {
		return err
	}
	str.SetReliableBoundary()
	return nil
}

func addWTProtocolHeader(hdr http.Header, protocol string) error {
	list := httpsfv.List{httpsfv.NewItem(protocol)}
	value, err := httpsfv.Marshal(list)
	if err != nil {
		return err
	}
	hdr.Set(wtAvailableProtocolsHeader, value)
	return nil
}

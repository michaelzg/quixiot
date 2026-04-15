package broker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/quic-go/webtransport-go"

	"quixiot/internal/wire"
)

const (
	outDatagramDepth = 128
	outStreamDepth   = 32
	maxDatagramBytes = 1024
	clientIDHeader   = "X-Client-ID"
	sessionCloseCode = 0
)

type Session struct {
	broker   *Broker
	wt       *webtransport.Session
	clientID string
	logger   *slog.Logger

	outDatagram chan wire.Frame
	outStream   chan wire.Frame

	subsMu sync.Mutex
	subs   map[string]struct{}
}

func (b *Broker) HandleRequest(w http.ResponseWriter, r *http.Request, server *webtransport.Server) {
	sess, err := server.Upgrade(w, r)
	if err != nil {
		b.logger.Warn("webtransport upgrade failed", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	clientID := r.Header.Get(clientIDHeader)
	if clientID == "" {
		clientID = "anonymous"
	}
	bs := &Session{
		broker:      b,
		wt:          sess,
		clientID:    clientID,
		logger:      b.logger.With("client_id", clientID, "remote", sess.RemoteAddr().String()),
		outDatagram: make(chan wire.Frame, outDatagramDepth),
		outStream:   make(chan wire.Frame, outStreamDepth),
		subs:        make(map[string]struct{}),
	}
	bs.serve()
}

func (s *Session) serve() {
	defer func() {
		s.cleanup()
		_ = s.wt.CloseWithError(sessionCloseCode, "")
	}()

	go s.datagramWriter()
	go s.streamWriter()
	go s.datagramReader()

	control, err := s.wt.AcceptStream(s.wt.Context())
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.logger.Warn("accept control stream failed", "error", err)
		}
		return
	}
	s.logger.Info("pubsub session opened")
	s.readControl(control)
}

func (s *Session) readControl(str *webtransport.Stream) {
	defer str.Close()
	for {
		frame, err := wire.ReadStreamFrame(str)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return
			}
			s.logger.Warn("read control frame failed", "error", err)
			return
		}
		switch frame.Kind {
		case wire.KindSub:
			s.subscribe(frame.Topic)
		case wire.KindUnsub:
			s.unsubscribe(frame.Topic)
		case wire.KindPub:
			s.broker.Publish(frame, SurfaceStream)
		default:
			s.logger.Warn("ignoring unexpected control frame", "kind", frame.Kind, "topic", frame.Topic)
		}
	}
}

func (s *Session) datagramReader() {
	for {
		data, err := s.wt.ReceiveDatagram(s.wt.Context())
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				s.logger.Debug("datagram reader stopped", "error", err)
			}
			return
		}
		if len(data) > maxDatagramBytes {
			s.logger.Warn("drop oversized incoming datagram", "bytes", len(data))
			continue
		}
		frame, err := wire.Decode(data)
		if err != nil {
			s.logger.Warn("drop invalid incoming datagram", "error", err)
			continue
		}
		if frame.Kind != wire.KindPub {
			s.logger.Warn("drop non-pub datagram", "kind", frame.Kind, "topic", frame.Topic)
			continue
		}
		s.broker.Publish(frame, SurfaceDatagram)
	}
}

func (s *Session) datagramWriter() {
	for {
		select {
		case <-s.sessionContext().Done():
			return
		case frame := <-s.outDatagram:
			data, err := wire.Encode(frame)
			if err != nil {
				s.logger.Warn("encode datagram failed", "error", err)
				continue
			}
			if len(data) > maxDatagramBytes {
				s.logger.Warn("drop oversized outgoing datagram", "topic", frame.Topic, "bytes", len(data))
				continue
			}
			if err := s.wt.SendDatagram(data); err != nil {
				s.logger.Warn("send datagram failed", "topic", frame.Topic, "error", err)
				return
			}
		}
	}
}

func (s *Session) streamWriter() {
	for {
		select {
		case <-s.sessionContext().Done():
			return
		case frame := <-s.outStream:
			str, err := s.wt.OpenUniStreamSync(s.sessionContext())
			if err != nil {
				s.logger.Warn("open uni stream failed", "topic", frame.Topic, "error", err)
				return
			}
			if err := wire.WriteStreamFrame(str, frame); err != nil {
				s.logger.Warn("write uni stream frame failed", "topic", frame.Topic, "error", err)
				_ = str.Close()
				continue
			}
			if err := str.Close(); err != nil {
				s.logger.Warn("close uni stream failed", "topic", frame.Topic, "error", err)
			}
		}
	}
}

func (s *Session) subscribe(topic string) {
	s.subsMu.Lock()
	if _, ok := s.subs[topic]; ok {
		s.subsMu.Unlock()
		return
	}
	s.subs[topic] = struct{}{}
	s.subsMu.Unlock()

	s.broker.subscribe(s, topic)
	s.logger.Info("subscribed", "topic", topic)
}

func (s *Session) unsubscribe(topic string) {
	s.subsMu.Lock()
	delete(s.subs, topic)
	s.subsMu.Unlock()

	s.broker.unsubscribe(s, topic)
	s.logger.Info("unsubscribed", "topic", topic)
}

func (s *Session) cleanup() {
	s.broker.removeSession(s)
	s.logger.Info("pubsub session closed")
}

func (s *Session) enqueueDatagram(frame wire.Frame) {
	select {
	case s.outDatagram <- frame:
		return
	default:
		select {
		case <-s.outDatagram:
		default:
		}
		select {
		case s.outDatagram <- frame:
		case <-s.sessionContext().Done():
		}
	}
}

func (s *Session) enqueueStream(frame wire.Frame) {
	select {
	case s.outStream <- frame:
	case <-s.sessionContext().Done():
	}
}

func (s *Session) sessionContext() context.Context {
	if s.wt != nil {
		return s.wt.Context()
	}
	return context.Background()
}

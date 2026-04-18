package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"quixiot/internal/metrics"
	"quixiot/internal/wire"
)

const (
	pubsubProtocol = "quixiot-pubsub-v1"
	clientIDHeader = "X-Client-ID"
	maxDatagramLen = 1024
)

type Surface string

const (
	SurfaceDatagram Surface = "datagram"
	SurfaceStream   Surface = "stream"
)

type PubSubMessage struct {
	Topic   string
	Payload []byte
	Surface Surface
}

type PubSubSession struct {
	session pubsubTransportSession
	control *quic.Stream
	logger  *slog.Logger
	metrics *metrics.ClientMetrics

	incoming chan PubSubMessage

	writeMu         sync.Mutex
	closeOnce       sync.Once
	incomingCloseMu sync.Once
}

func (c *Client) ConnectPubSub(ctx context.Context, clientID string) (*PubSubSession, error) {
	if clientID == "" {
		return nil, fmt.Errorf("client: client ID is required")
	}

	sess, resp, err := c.connectPubSubTransport(ctx, clientID)
	if err != nil {
		return nil, fmt.Errorf("client: dial pubsub: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("client: pubsub status %s", resp.Status)
	}

	control, err := sess.OpenStreamSync(ctx)
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("client: open control stream: %w", err)
	}

	ps := &PubSubSession{
		session:  sess,
		control:  control,
		logger:   c.logger.With("client_id", clientID),
		metrics:  c.metrics,
		incoming: make(chan PubSubMessage, 256),
	}
	go ps.readDatagrams()
	go ps.readUniStreams()
	go func() {
		<-sess.Context().Done()
		ps.closeIncoming()
	}()
	return ps, nil
}

func (p *PubSubSession) Subscribe(ctx context.Context, topic string) error {
	return p.writeControl(ctx, wire.Frame{Kind: wire.KindSub, Topic: topic})
}

func (p *PubSubSession) Unsubscribe(ctx context.Context, topic string) error {
	return p.writeControl(ctx, wire.Frame{Kind: wire.KindUnsub, Topic: topic})
}

func (p *PubSubSession) PublishStream(ctx context.Context, topic string, payload []byte) error {
	return p.writeControl(ctx, wire.Frame{
		Kind:    wire.KindPub,
		Topic:   topic,
		Payload: append([]byte(nil), payload...),
	})
}

func (p *PubSubSession) PublishDatagram(topic string, payload []byte) error {
	frame := wire.Frame{
		Kind:    wire.KindPub,
		Topic:   topic,
		Payload: append([]byte(nil), payload...),
	}
	data, err := wire.Encode(frame)
	if err != nil {
		return err
	}
	if len(data) > maxDatagramLen {
		return fmt.Errorf("client: datagram frame too large: %d bytes", len(data))
	}
	if err := p.session.SendDatagram(data); err != nil {
		return fmt.Errorf("client: send datagram: %w", err)
	}
	if p.metrics != nil {
		p.metrics.Datagrams.WithLabelValues("out", topic).Inc()
	}
	return nil
}

func (p *PubSubSession) Messages() <-chan PubSubMessage {
	return p.incoming
}

func (p *PubSubSession) Close() error {
	var closeErr error
	p.closeOnce.Do(func() {
		if p.control != nil {
			if err := p.control.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		if p.session != nil {
			if err := p.session.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
	})
	return closeErr
}

func (p *PubSubSession) writeControl(ctx context.Context, frame wire.Frame) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	p.writeMu.Lock()
	defer p.writeMu.Unlock()

	if err := p.control.SetWriteDeadline(deadlineFromContext(ctx)); err != nil {
		return fmt.Errorf("client: set control deadline: %w", err)
	}
	defer p.control.SetWriteDeadline(deadlineFromContext(nil))

	if err := wire.WriteStreamFrame(p.control, frame); err != nil {
		return fmt.Errorf("client: write control frame: %w", err)
	}
	return nil
}

func (p *PubSubSession) readDatagrams() {
	for {
		data, err := p.session.ReceiveDatagram(p.session.Context())
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				p.logger.Debug("pubsub datagram reader stopped", "error", err)
			}
			return
		}
		frame, err := wire.Decode(data)
		if err != nil {
			p.logger.Warn("drop invalid pubsub datagram", "error", err)
			continue
		}
		p.deliver(frame, SurfaceDatagram)
	}
}

func (p *PubSubSession) readUniStreams() {
	for {
		str, err := p.session.AcceptUniStream(p.session.Context())
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				p.logger.Debug("pubsub uni stream reader stopped", "error", err)
			}
			return
		}
		go p.readUniStream(str)
	}
}

func (p *PubSubSession) readUniStream(str *quic.ReceiveStream) {
	for {
		frame, err := wire.ReadStreamFrame(str)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				p.logger.Warn("read pubsub stream frame failed", "error", err)
			}
			return
		}
		p.deliver(frame, SurfaceStream)
	}
}

func (p *PubSubSession) deliver(frame wire.Frame, surface Surface) {
	if frame.Kind != wire.KindPub {
		return
	}
	msg := PubSubMessage{
		Topic:   frame.Topic,
		Payload: append([]byte(nil), frame.Payload...),
		Surface: surface,
	}
	if p.metrics != nil && surface == SurfaceDatagram {
		p.metrics.Datagrams.WithLabelValues("in", frame.Topic).Inc()
	}
	select {
	case p.incoming <- msg:
	case <-p.session.Context().Done():
	}
}

func (p *PubSubSession) closeIncoming() {
	p.incomingCloseMu.Do(func() {
		close(p.incoming)
	})
}

func deadlineFromContext(ctx context.Context) (deadline time.Time) {
	if ctx == nil {
		return time.Time{}
	}
	deadline, _ = ctx.Deadline()
	return deadline
}

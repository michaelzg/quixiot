package broker

import (
	"log/slog"
	"sync"

	"quixiot/internal/metrics"
	"quixiot/internal/wire"
)

type Surface string

const (
	SurfaceDatagram Surface = "datagram"
	SurfaceStream   Surface = "stream"
)

type Broker struct {
	logger  *slog.Logger
	metrics *metrics.ServerMetrics

	mu     sync.RWMutex
	topics map[string]map[*Session]struct{}
}

func New(log *slog.Logger, m *metrics.ServerMetrics) *Broker {
	if log == nil {
		log = slog.Default()
	}
	return &Broker{
		logger:  log,
		metrics: m,
		topics:  make(map[string]map[*Session]struct{}),
	}
}

func (b *Broker) Publish(frame wire.Frame, surface Surface) {
	if b.metrics != nil {
		b.metrics.Bytes.WithLabelValues(string(surfaceLabel(surface)), "in").Add(float64(len(frame.Payload)))
	}
	b.mu.RLock()
	subscribers := make([]*Session, 0, len(b.topics[frame.Topic]))
	for sess := range b.topics[frame.Topic] {
		subscribers = append(subscribers, sess)
	}
	b.mu.RUnlock()

	for _, sess := range subscribers {
		switch surface {
		case SurfaceDatagram:
			sess.enqueueDatagram(frame)
		default:
			sess.enqueueStream(frame)
		}
	}
}

func (b *Broker) subscribe(sess *Session, topic string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subs := b.topics[topic]
	if subs == nil {
		subs = make(map[*Session]struct{})
		b.topics[topic] = subs
	}
	subs[sess] = struct{}{}
	if b.metrics != nil {
		b.metrics.PubSubSubscribers.WithLabelValues(topic).Inc()
	}
}

func (b *Broker) unsubscribe(sess *Session, topic string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subs := b.topics[topic]
	if subs == nil {
		return
	}
	if _, ok := subs[sess]; ok && b.metrics != nil {
		b.metrics.PubSubSubscribers.WithLabelValues(topic).Dec()
	}
	delete(subs, sess)
	if len(subs) == 0 {
		delete(b.topics, topic)
	}
}

func (b *Broker) removeSession(sess *Session) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for topic, subs := range b.topics {
		if _, ok := subs[sess]; ok && b.metrics != nil {
			b.metrics.PubSubSubscribers.WithLabelValues(topic).Dec()
		}
		delete(subs, sess)
		if len(subs) == 0 {
			delete(b.topics, topic)
		}
	}
}

func surfaceLabel(surface Surface) string {
	switch surface {
	case SurfaceDatagram:
		return "wt_datagram"
	case SurfaceStream:
		return "wt_stream"
	default:
		return string(surface)
	}
}

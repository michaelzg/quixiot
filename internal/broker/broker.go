package broker

import (
	"log/slog"
	"sync"

	"quixiot/internal/wire"
)

type Surface string

const (
	SurfaceDatagram Surface = "datagram"
	SurfaceStream   Surface = "stream"
)

type Broker struct {
	logger *slog.Logger

	mu     sync.RWMutex
	topics map[string]map[*Session]struct{}
}

func New(log *slog.Logger) *Broker {
	if log == nil {
		log = slog.Default()
	}
	return &Broker{
		logger: log,
		topics: make(map[string]map[*Session]struct{}),
	}
}

func (b *Broker) Publish(frame wire.Frame, surface Surface) {
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
}

func (b *Broker) unsubscribe(sess *Session, topic string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subs := b.topics[topic]
	if subs == nil {
		return
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
		delete(subs, sess)
		if len(subs) == 0 {
			delete(b.topics, topic)
		}
	}
}

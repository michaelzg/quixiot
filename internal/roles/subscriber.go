package roles

import (
	"context"
	"fmt"
	"log/slog"

	"quixiot/internal/client"
)

type SubscriberSession interface {
	Subscribe(ctx context.Context, topic string) error
	Messages() <-chan client.PubSubMessage
}

type Subscriber struct {
	Session SubscriberSession
	Topics  []string
	Logger  *slog.Logger
}

func (s Subscriber) Run(ctx context.Context) error {
	if s.Session == nil {
		return fmt.Errorf("subscriber: session is required")
	}
	if len(s.Topics) == 0 {
		return fmt.Errorf("subscriber: at least one topic is required")
	}

	for _, topic := range s.Topics {
		if err := s.Session.Subscribe(ctx, topic); err != nil {
			return fmt.Errorf("subscriber: subscribe %s: %w", topic, err)
		}
		s.logger().Info("subscription active", "topic", topic)
	}

	counts := make(map[string]int)
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-s.Session.Messages():
			if !ok {
				return nil
			}
			counts[msg.Topic]++
			s.logger().Info("received pubsub message",
				"topic", msg.Topic,
				"surface", msg.Surface,
				"bytes", len(msg.Payload),
				"count", counts[msg.Topic],
			)
		}
	}
}

func (s Subscriber) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

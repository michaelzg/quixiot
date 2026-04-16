package roles

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"quixiot/internal/client"
	"quixiot/internal/metrics"
)

type SubscriberSession interface {
	Subscribe(ctx context.Context, topic string) error
	Messages() <-chan client.PubSubMessage
}

type Subscriber struct {
	Session SubscriberSession
	Topics  []string
	Logger  *slog.Logger
	Metrics *metrics.ClientMetrics
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
			if s.Metrics != nil {
				if ts, ok := parseStampedPayload(string(msg.Payload)); ok {
					s.Metrics.PublishLatency.Observe(time.Since(time.Unix(0, ts)).Seconds())
				}
			}
		}
	}
}

func (s Subscriber) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func parseStampedPayload(raw string) (int64, bool) {
	for _, part := range strings.Split(raw, ";") {
		if !strings.HasPrefix(part, "ts=") {
			continue
		}
		ts, err := strconv.ParseInt(strings.TrimPrefix(part, "ts="), 10, 64)
		if err != nil {
			return 0, false
		}
		return ts, true
	}
	return 0, false
}

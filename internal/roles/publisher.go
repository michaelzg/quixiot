package roles

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"quixiot/internal/metrics"
)

type PublisherSession interface {
	PublishDatagram(topic string, payload []byte) error
	PublishStream(ctx context.Context, topic string, payload []byte) error
}

type Publisher struct {
	Session           PublisherSession
	ClientID          string
	TelemetryTopic    string
	CommandTopic      string
	TelemetryInterval time.Duration
	CommandInterval   time.Duration
	PayloadSize       int
	Logger            *slog.Logger
	Metrics           *metrics.ClientMetrics
}

func (p Publisher) Run(ctx context.Context) error {
	if p.Session == nil {
		return fmt.Errorf("publisher: session is required")
	}
	if p.TelemetryTopic == "" || p.CommandTopic == "" {
		return fmt.Errorf("publisher: telemetry and command topics are required")
	}
	if p.TelemetryInterval <= 0 || p.CommandInterval <= 0 {
		return fmt.Errorf("publisher: intervals must be positive")
	}
	if p.PayloadSize <= 0 {
		return fmt.Errorf("publisher: payload size must be positive")
	}

	telemetryTicker := time.NewTicker(p.TelemetryInterval)
	commandTicker := time.NewTicker(p.CommandInterval)
	defer telemetryTicker.Stop()
	defer commandTicker.Stop()

	if err := p.publishTelemetry(); err != nil {
		p.logger().Error("publish telemetry failed", "error", err)
	}
	if err := p.publishCommand(ctx); err != nil {
		p.logger().Error("publish command failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-telemetryTicker.C:
			if err := p.publishTelemetry(); err != nil {
				p.logger().Error("publish telemetry failed", "error", err)
			}
		case <-commandTicker.C:
			if err := p.publishCommand(ctx); err != nil {
				p.logger().Error("publish command failed", "error", err)
			}
		}
	}
}

func (p Publisher) publishTelemetry() error {
	payload := []byte(stampedPayload(p.ClientID, "telemetry"))
	if p.PayloadSize > len(payload) {
		payload = append(payload, make([]byte, p.PayloadSize-len(payload))...)
	}
	if err := p.Session.PublishDatagram(p.TelemetryTopic, payload[:p.PayloadSize]); err != nil {
		return fmt.Errorf("publisher: telemetry datagram: %w", err)
	}
	p.logger().Info("published telemetry", "topic", p.TelemetryTopic, "bytes", len(payload[:p.PayloadSize]))
	return nil
}

func (p Publisher) publishCommand(ctx context.Context) error {
	payload := []byte(stampedPayload(p.ClientID, "command"))
	if err := p.Session.PublishStream(ctx, p.CommandTopic, payload); err != nil {
		return fmt.Errorf("publisher: command stream: %w", err)
	}
	p.logger().Info("published command", "topic", p.CommandTopic, "bytes", len(payload))
	return nil
}

func (p Publisher) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

func stampedPayload(clientID, kind string) string {
	return fmt.Sprintf("ts=%d;client=%s;kind=%s", time.Now().UnixNano(), clientID, kind)
}

package roles

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"quixiot/internal/client"
)

type PollerClient interface {
	GetState(ctx context.Context) (client.State, error)
	GetConfig(ctx context.Context, clientID string) (client.DeviceConfig, error)
}

type Poller struct {
	Client   PollerClient
	ClientID string
	Interval time.Duration
	Logger   *slog.Logger
}

func (p Poller) Run(ctx context.Context) error {
	if p.Client == nil {
		return fmt.Errorf("poller: client is required")
	}
	if p.ClientID == "" {
		return fmt.Errorf("poller: client ID is required")
	}
	if p.Interval <= 0 {
		return fmt.Errorf("poller: interval must be positive")
	}

	log := p.logger()
	if err := p.PollOnce(ctx); err != nil {
		log.Error("poll failed", "error", err)
	}

	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.PollOnce(ctx); err != nil {
				log.Error("poll failed", "error", err)
			}
		}
	}
}

func (p Poller) PollOnce(ctx context.Context) error {
	state, err := p.Client.GetState(ctx)
	if err != nil {
		return fmt.Errorf("poller: get state: %w", err)
	}
	cfg, err := p.Client.GetConfig(ctx, p.ClientID)
	if err != nil {
		return fmt.Errorf("poller: get config: %w", err)
	}

	p.logger().Info("poll snapshot",
		"client_id", p.ClientID,
		"server_version", state.Version,
		"uptime_millis", state.UptimeMillis,
		"poll_interval_seconds", cfg.PollIntervalSeconds,
		"telemetry_topic", cfg.TelemetryTopic,
		"command_topic", cfg.CommandTopic,
	)
	return nil
}

func (p Poller) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

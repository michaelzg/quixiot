package roles_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"quixiot/internal/client"
	"quixiot/internal/roles"
)

type fakePollerClient struct {
	stateCalls  int
	configCalls int
}

func (f *fakePollerClient) GetState(context.Context) (client.State, error) {
	f.stateCalls++
	return client.State{Version: "test", UptimeMillis: 42}, nil
}

func (f *fakePollerClient) GetConfig(context.Context, string) (client.DeviceConfig, error) {
	f.configCalls++
	return client.DeviceConfig{
		ClientID:            "device-123",
		PollIntervalSeconds: 5,
		TelemetryTopic:      "clients/device-123/telemetry",
		CommandTopic:        "clients/device-123/commands",
	}, nil
}

func TestPollerPollOnce(t *testing.T) {
	fake := &fakePollerClient{}
	poller := roles.Poller{
		Client:   fake,
		ClientID: "device-123",
		Interval: time.Second,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := poller.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if fake.stateCalls != 1 || fake.configCalls != 1 {
		t.Fatalf("unexpected calls: state=%d config=%d", fake.stateCalls, fake.configCalls)
	}
}

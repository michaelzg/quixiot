package roles_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"quixiot/internal/roles"
)

type fakePublisherSession struct {
	datagramTopic string
	datagramBody  []byte
	streamTopic   string
	streamBody    []byte
}

func (f *fakePublisherSession) PublishDatagram(topic string, payload []byte) error {
	f.datagramTopic = topic
	f.datagramBody = append([]byte(nil), payload...)
	return nil
}

func (f *fakePublisherSession) PublishStream(context.Context, string, []byte) error {
	panic("use named method below")
}

func (f *fakePublisherSession) PublishControl(ctx context.Context, topic string, payload []byte) error {
	f.streamTopic = topic
	f.streamBody = append([]byte(nil), payload...)
	return nil
}

type publisherAdapter struct{ *fakePublisherSession }

func (a publisherAdapter) PublishStream(ctx context.Context, topic string, payload []byte) error {
	return a.fakePublisherSession.PublishControl(ctx, topic, payload)
}

func TestPublisherRunPublishesBothSurfaces(t *testing.T) {
	fake := &fakePublisherSession{}
	pub := roles.Publisher{
		Session:           publisherAdapter{fake},
		ClientID:          "device-123",
		TelemetryTopic:    "telemetry",
		CommandTopic:      "commands",
		TelemetryInterval: time.Hour,
		CommandInterval:   time.Hour,
		PayloadSize:       32,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pub.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fake.datagramTopic != "telemetry" {
		t.Fatalf("datagram topic: got %q", fake.datagramTopic)
	}
	if fake.streamTopic != "commands" {
		t.Fatalf("stream topic: got %q", fake.streamTopic)
	}
}

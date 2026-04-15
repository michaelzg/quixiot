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

type fakeSubscriberSession struct {
	topics []string
	msgs   chan client.PubSubMessage
}

func (f *fakeSubscriberSession) Subscribe(_ context.Context, topic string) error {
	f.topics = append(f.topics, topic)
	return nil
}

func (f *fakeSubscriberSession) Messages() <-chan client.PubSubMessage {
	return f.msgs
}

func TestSubscriberRunSubscribesTopics(t *testing.T) {
	fake := &fakeSubscriberSession{msgs: make(chan client.PubSubMessage)}
	sub := roles.Subscriber{
		Session: fake,
		Topics:  []string{"telemetry", "commands"},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- sub.Run(ctx)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	close(fake.msgs)

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fake.topics) != 2 || fake.topics[0] != "telemetry" || fake.topics[1] != "commands" {
		t.Fatalf("topics: got %v", fake.topics)
	}
}

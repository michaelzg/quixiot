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

func TestMixedRunStopsBeforeStartingRemainingRolesOnEarlyCancel(t *testing.T) {
	subscriberMsgs := make(chan client.PubSubMessage)
	fakeSub := &fakeSubscriberSession{msgs: subscriberMsgs}
	fakePub := &fakePublisherSession{}
	fakePoller := &fakePollerClient{}
	fakeUploader := &fakeUploaderClient{}

	mixed := roles.Mixed{
		Poller: roles.Poller{
			Client:   fakePoller,
			ClientID: "device-123",
			Interval: time.Hour,
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		Uploader: roles.Uploader{
			Client:   uploaderAdapter{fakeUploader},
			ClientID: "device-123",
			Interval: time.Hour,
			Size:     1024,
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		Publisher: roles.Publisher{
			Session:           publisherAdapter{fakePub},
			ClientID:          "device-123",
			TelemetryTopic:    "telemetry",
			CommandTopic:      "commands",
			TelemetryInterval: time.Hour,
			CommandInterval:   time.Hour,
			PayloadSize:       32,
			Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		Subscriber: roles.Subscriber{
			Session: fakeSub,
			Topics:  []string{"telemetry", "commands"},
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- mixed.Run(ctx)
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()
	close(subscriberMsgs)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after early cancellation")
	}

	if fakePoller.stateCalls != 0 || fakePoller.configCalls != 0 {
		t.Fatalf("poller should not have started: state=%d config=%d", fakePoller.stateCalls, fakePoller.configCalls)
	}
	if fakeUploader.calls != 0 {
		t.Fatalf("uploader should not have started: calls=%d", fakeUploader.calls)
	}
	if fakePub.datagramTopic != "" || fakePub.streamTopic != "" {
		t.Fatalf("publisher should not have started: datagram=%q stream=%q", fakePub.datagramTopic, fakePub.streamTopic)
	}
}

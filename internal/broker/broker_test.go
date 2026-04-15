package broker

import (
	"io"
	"log/slog"
	"testing"

	"quixiot/internal/wire"
)

func TestBrokerPublishesToSubscribers(t *testing.T) {
	b := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	a := &Session{
		broker:      b,
		outDatagram: make(chan wire.Frame, 1),
		outStream:   make(chan wire.Frame, 1),
		subs:        make(map[string]struct{}),
	}
	c := &Session{
		broker:      b,
		outDatagram: make(chan wire.Frame, 1),
		outStream:   make(chan wire.Frame, 1),
		subs:        make(map[string]struct{}),
	}

	b.subscribe(a, "topic-a")
	b.subscribe(c, "topic-a")
	frame := wire.Frame{Kind: wire.KindPub, Topic: "topic-a", Payload: []byte("hello")}
	b.Publish(frame, SurfaceDatagram)

	for _, sess := range []*Session{a, c} {
		select {
		case got := <-sess.outDatagram:
			if got.Topic != "topic-a" || string(got.Payload) != "hello" {
				t.Fatalf("unexpected frame: %+v", got)
			}
		default:
			t.Fatal("expected datagram delivery")
		}
	}
}

func TestSessionDatagramQueueDropsOldest(t *testing.T) {
	sess := &Session{
		outDatagram: make(chan wire.Frame, 2),
	}

	sess.enqueueDatagram(wire.Frame{Kind: wire.KindPub, Topic: "topic", Payload: []byte("first")})
	sess.enqueueDatagram(wire.Frame{Kind: wire.KindPub, Topic: "topic", Payload: []byte("second")})
	sess.enqueueDatagram(wire.Frame{Kind: wire.KindPub, Topic: "topic", Payload: []byte("third")})

	got1 := <-sess.outDatagram
	got2 := <-sess.outDatagram
	if string(got1.Payload) != "second" || string(got2.Payload) != "third" {
		t.Fatalf("queue contents: want second/third got %q/%q", got1.Payload, got2.Payload)
	}
}

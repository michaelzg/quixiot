package wire

import (
	"bytes"
	"io"
	"reflect"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	frame := Frame{
		Kind:    KindPub,
		Topic:   "clients/demo/telemetry",
		Payload: []byte("hello"),
	}

	encoded, err := Encode(frame)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(decoded, frame) {
		t.Fatalf("round-trip mismatch: want %+v got %+v", frame, decoded)
	}
}

func TestWriteReadStreamFrame(t *testing.T) {
	frame := Frame{
		Kind:    KindSub,
		Topic:   "commands",
		Payload: []byte("payload"),
	}

	var buf bytes.Buffer
	if err := WriteStreamFrame(&buf, frame); err != nil {
		t.Fatalf("WriteStreamFrame: %v", err)
	}
	got, err := ReadStreamFrame(&buf)
	if err != nil {
		t.Fatalf("ReadStreamFrame: %v", err)
	}
	if !reflect.DeepEqual(got, frame) {
		t.Fatalf("stream frame mismatch: want %+v got %+v", frame, got)
	}
}

func TestReadStreamFrameEOF(t *testing.T) {
	if _, err := ReadStreamFrame(bytes.NewReader(nil)); err != io.EOF {
		t.Fatalf("ReadStreamFrame: want EOF got %v", err)
	}
}

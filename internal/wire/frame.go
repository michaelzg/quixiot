package wire

import (
	"fmt"
	"io"

	"github.com/quic-go/quic-go/quicvarint"
)

type Kind uint8

const (
	KindPub Kind = 1 + iota
	KindSub
	KindUnsub
	KindPubAck
	KindErr
)

type Frame struct {
	Kind    Kind
	Topic   string
	Payload []byte
}

func Encode(frame Frame) ([]byte, error) {
	if frame.Kind == 0 {
		return nil, fmt.Errorf("wire: frame kind is required")
	}
	if frame.Topic == "" {
		return nil, fmt.Errorf("wire: frame topic is required")
	}

	out := make([]byte, 0, 1+quicvarint.Len(uint64(len(frame.Topic)))+len(frame.Topic)+len(frame.Payload))
	out = append(out, byte(frame.Kind))
	out = quicvarint.Append(out, uint64(len(frame.Topic)))
	out = append(out, frame.Topic...)
	out = append(out, frame.Payload...)
	return out, nil
}

func Decode(data []byte) (Frame, error) {
	if len(data) < 2 {
		return Frame{}, fmt.Errorf("wire: short frame")
	}
	kind := Kind(data[0])
	if kind == 0 {
		return Frame{}, fmt.Errorf("wire: invalid kind 0")
	}
	topicLen, n, err := quicvarint.Parse(data[1:])
	if err != nil {
		return Frame{}, fmt.Errorf("wire: parse topic length: %w", err)
	}
	start := 1 + n
	end := start + int(topicLen)
	if end > len(data) {
		return Frame{}, fmt.Errorf("wire: topic length exceeds frame size")
	}
	topic := string(data[start:end])
	if topic == "" {
		return Frame{}, fmt.Errorf("wire: frame topic is required")
	}
	payload := make([]byte, len(data[end:]))
	copy(payload, data[end:])
	return Frame{
		Kind:    kind,
		Topic:   topic,
		Payload: payload,
	}, nil
}

func WriteStreamFrame(w io.Writer, frame Frame) error {
	data, err := Encode(frame)
	if err != nil {
		return err
	}

	header := quicvarint.Append(make([]byte, 0, quicvarint.Len(uint64(len(data)))), uint64(len(data)))
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("wire: write frame header: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("wire: write frame body: %w", err)
	}
	return nil
}

func ReadStreamFrame(r io.Reader) (Frame, error) {
	reader := quicvarint.NewReader(r)
	size, err := quicvarint.Read(reader)
	if err != nil {
		return Frame{}, err
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return Frame{}, fmt.Errorf("wire: read frame body: %w", err)
	}
	return Decode(data)
}

package transport

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
)

func TestFrameBoundsAndDirections(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	stream := newStream(context.Background(), a, nil)
	defer stream.Close()
	var header [5]byte
	header[0] = byte(Input)
	binary.BigEndian.PutUint32(header[1:], MaxFrame+1)
	go func() { _, _ = b.Write(header[:]) }()
	if _, err := stream.ReadFrame(); err == nil {
		t.Fatal("oversized frame accepted")
	}
	if err := stream.WriteFrame(Frame{Kind: Kind(255)}); err == nil {
		t.Fatal("unknown frame accepted")
	}
	if err := stream.WriteFrame(Frame{Kind: Input, Data: make([]byte, MaxFrame+1)}); err == nil {
		t.Fatal("oversized write accepted")
	}
	stream.Receipt.ReadOnly = true
	if err := stream.WriteInput([]byte("no")); err == nil {
		t.Fatal("read-only input accepted")
	}
	if err := stream.Resize(24, 80); err == nil {
		t.Fatal("read-only resize accepted")
	}
}

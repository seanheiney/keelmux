package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// A net.Pipe writer can close as soon as the peer's buffered reader has fetched
// its complete frame, before ReadFrame resumes after that underlying read.
type closeBeforeReadReturns struct {
	net.Conn
	peerClosed <-chan struct{}
}

func (c closeBeforeReadReturns) Read(data []byte) (int, error) {
	n, err := c.Conn.Read(data)
	<-c.peerClosed
	return n, err
}

func TestReadBufferedFrameAfterPeerClose(t *testing.T) {
	for _, frame := range []Frame{{Close, nil}, {Failure, []byte("explicit native failure")}, {Output, []byte("final output")}} {
		t.Run(map[Kind]string{Close: "close", Failure: "failure", Output: "output"}[frame.Kind], func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			closed := make(chan struct{})
			stream := newStream(context.Background(), closeBeforeReadReturns{a, closed}, nil)
			defer stream.Close()
			data := make([]byte, 5+len(frame.Data))
			data[0] = byte(frame.Kind)
			binary.BigEndian.PutUint32(data[1:5], uint32(len(frame.Data)))
			copy(data[5:], frame.Data)
			go func() {
				_, _ = b.Write(data)
				_ = b.Close()
				close(closed)
			}()
			got, err := stream.ReadFrame()
			if err != nil || got.Kind != frame.Kind || string(got.Data) != string(frame.Data) {
				t.Fatalf("complete buffered frame lost after peer close: %+v %v", got, err)
			}
			if _, err := stream.ReadFrame(); !errors.Is(err, io.EOF) {
				t.Fatalf("missing subsequent frame did not remain EOF: %v", err)
			}
		})
	}
}

func TestBridgePreservesBufferedCloseAfterPeerClose(t *testing.T) {
	upstreamRead, upstreamWrite := net.Pipe()
	defer upstreamRead.Close()
	defer upstreamWrite.Close()
	closed := make(chan struct{})
	upstream := newStream(context.Background(), closeBeforeReadReturns{upstreamRead, closed}, nil)
	defer upstream.Close()
	downstreamWrite, downstreamRead := net.Pipe()
	downstream := newStream(context.Background(), downstreamWrite, nil)
	client := newStream(context.Background(), downstreamRead, nil)
	defer downstream.Close()
	defer client.Close()
	bridged := make(chan struct{})
	go func() { bridge(downstream, upstream); close(bridged) }()
	go func() {
		_, _ = upstreamWrite.Write([]byte{byte(Close), 0, 0, 0, 0})
		_ = upstreamWrite.Close()
		close(closed)
	}()
	frame, err := client.ReadFrame()
	if err != nil || frame.Kind != Close || len(frame.Data) != 0 {
		t.Fatalf("bridge lost the native host's final Close: %+v %v", frame, err)
	}
	<-bridged
}

func TestReadBufferedMalformedOrTruncatedFrameAfterPeerClose(t *testing.T) {
	for _, data := range [][]byte{
		{byte(Close)},
		{byte(Close), 0, 0, 0},
		{byte(Output), 0, 0, 0, 2, 'x'},
		{0, 0, 0, 0, 0},
		{byte(Output), 0, 1, 0, 0},
	} {
		a, b := net.Pipe()
		closed := make(chan struct{})
		stream := newStream(context.Background(), closeBeforeReadReturns{a, closed}, nil)
		go func() {
			_, _ = b.Write(data)
			_ = b.Close()
			close(closed)
		}()
		_, err := stream.ReadFrame()
		_ = stream.Close()
		if err == nil {
			t.Fatalf("malformed or incomplete final frame accepted: %v", data)
		}
	}
}

// Keep the requested production deadline visible while accelerating only this
// connection's clock, so a partial-frame assertion needs no five-second wait.
type shortDeadlineConn struct {
	net.Conn
	deadlines chan time.Duration
}

func (c shortDeadlineConn) SetReadDeadline(deadline time.Time) error {
	if !deadline.IsZero() {
		c.deadlines <- time.Until(deadline)
		deadline = time.Now().Add(20 * time.Millisecond)
	}
	return c.Conn.SetReadDeadline(deadline)
}

func TestReadPartialFrameKeepsBoundedDeadline(t *testing.T) {
	for _, data := range [][]byte{{byte(Close)}, {byte(Output), 0, 0, 0, 2, 'x'}} {
		a, b := net.Pipe()
		deadlines := make(chan time.Duration, 2)
		stream := newStream(context.Background(), shortDeadlineConn{a, deadlines}, nil)
		go func() { _, _ = b.Write(data) }()
		_, err := stream.ReadFrame()
		_ = stream.Close()
		_ = b.Close()
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("partial frame did not time out: %v", err)
		}
		select {
		case requested := <-deadlines:
			if requested <= 0 || requested > ioTimeout {
				t.Fatalf("partial-frame deadline changed: %v", requested)
			}
		default:
			t.Fatal("partial frame did not request its bounded deadline")
		}
		if len(deadlines) != 0 {
			t.Fatal("partial-frame deadline was extended during the frame")
		}
	}
}

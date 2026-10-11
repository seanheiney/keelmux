package peerbridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/transport"
)

type tag byte

const (
	tHello tag = iota + 1
	tHelloAck
	tList
	tListed
	tAcquire
	tAcquired
	tOpen
	tReady
	tRelease
	tReleased
	tError
	tOutput
	tFailure
	tClose
)

type packet struct {
	kind            tag
	request, stream uint64
	data            []byte
}

func payloadLimit(t tag) int {
	switch t {
	case tListed:
		return InventoryLimit
	case tOutput:
		return transport.MaxFrame
	case tReady:
		return 4096
	case tFailure, tError:
		return 512
	default:
		return ControlLimit
	}
}
func (p packet) valid(client bool) bool {
	if p.kind < tHello || p.kind > tClose || len(p.data) > payloadLimit(p.kind) {
		return false
	}
	if client {
		switch p.kind {
		case tHello, tList, tAcquire, tRelease:
			return p.request != 0 && p.stream == 0
		case tOpen:
			return p.request != 0 && p.stream != 0
		case tClose:
			return p.request == 0 && p.stream != 0 && len(p.data) == 0
		default:
			return false
		}
	}
	switch p.kind {
	case tHelloAck, tListed, tAcquired, tReleased:
		return p.request != 0 && p.stream == 0
	case tError:
		return p.request != 0
	case tReady:
		return p.request != 0 && p.stream != 0
	case tOutput, tFailure:
		return p.request == 0 && p.stream != 0
	case tClose:
		return p.request == 0 && p.stream != 0 && len(p.data) == 0
	default:
		return false
	}
}

type wire struct {
	in       io.ReadCloser
	out      io.WriteCloser
	once     sync.Once
	writeMu  sync.Mutex
	closeErr error
}

func (w *wire) close() error {
	w.once.Do(func() {
		w.closeErr = w.in.Close()
		if e := w.out.Close(); w.closeErr == nil {
			w.closeErr = e
		}
	})
	return w.closeErr
}
func (w *wire) read(ctx context.Context) (packet, error) {
	var h [21]byte
	if _, e := io.ReadFull(w.in, h[:1]); e != nil {
		return packet{}, e
	}
	done := make(chan error, 1)
	var p packet
	go func() {
		_, e := io.ReadFull(w.in, h[1:])
		if e == nil {
			p.kind = tag(h[4])
			p.request = binary.BigEndian.Uint64(h[5:13])
			p.stream = binary.BigEndian.Uint64(h[13:])
			n := binary.BigEndian.Uint32(h[:4])
			if p.kind < tHello || p.kind > tClose || n > uint32(payloadLimit(p.kind)) {
				e = ErrInvalid
			} else {
				p.data = make([]byte, int(n))
				_, e = io.ReadFull(w.in, p.data)
			}
		}
		done <- e
	}()
	timer := time.NewTimer(IOTimeout)
	defer timer.Stop()
	select {
	case e := <-done:
		return p, e
	case <-ctx.Done():
		w.close()
		<-done
		return packet{}, ctx.Err()
	case <-timer.C:
		w.close()
		<-done
		return packet{}, ErrUnavailable
	}
}
func (w *wire) write(ctx context.Context, p packet) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.kind < tHello || p.kind > tClose || len(p.data) > payloadLimit(p.kind) {
		return ErrInvalid
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	b := make([]byte, 21+len(p.data))
	binary.BigEndian.PutUint32(b, uint32(len(p.data)))
	b[4] = byte(p.kind)
	binary.BigEndian.PutUint64(b[5:], p.request)
	binary.BigEndian.PutUint64(b[13:], p.stream)
	copy(b[21:], p.data)
	done := make(chan error, 1)
	go func() {
		for len(b) > 0 {
			n, e := w.out.Write(b)
			if e != nil {
				done <- e
				return
			}
			if n == 0 {
				done <- io.ErrShortWrite
				return
			}
			b = b[n:]
		}
		done <- nil
	}()
	timer := time.NewTimer(IOTimeout)
	defer timer.Stop()
	select {
	case e := <-done:
		return e
	case <-ctx.Done():
		w.close()
		<-done
		return ctx.Err()
	case <-timer.C:
		w.close()
		<-done
		return ErrUnavailable
	}
}
func jsonPacket(t tag, id, stream uint64, v any) (packet, error) {
	b, e := json.Marshal(v)
	p := packet{t, id, stream, b}
	if e != nil || len(b) > payloadLimit(t) {
		return packet{}, ErrInvalid
	}
	return p, nil
}

type helloRequest struct {
	Version  string   `json:"version"`
	Selector Selector `json:"selector"`
}
type helloResult struct {
	Version string      `json:"version"`
	Policy  PolicyStamp `json:"policy"`
}
type revisionRequest struct {
	Revision uint64 `json:"revision"`
}
type acquireRequest struct {
	Revision uint64                `json:"revision"`
	Lease    protocol.LeaseRequest `json:"lease"`
}
type openRequest struct {
	Revision uint64            `json:"revision"`
	Request  transport.Request `json:"request"`
}
type releaseRequest struct {
	Lease protocol.LeaseRequest `json:"lease"`
}
type acquiredResult struct {
	Lease    domain.Lease `json:"lease"`
	ReadOnly bool         `json:"read_only"`
}
type failure struct {
	Code string `json:"code"`
}

func errorCode(e error) string {
	switch {
	case errors.Is(e, ErrInvalid):
		return "invalid"
	case errors.Is(e, ErrDenied):
		return "denied"
	case errors.Is(e, ErrConflict):
		return "conflict"
	case errors.Is(e, ErrUncertain):
		return "uncertain"
	case errors.Is(e, ErrStorage):
		return "storage"
	case errors.Is(e, ErrCapacity):
		return "capacity"
	default:
		return "unavailable"
	}
}
func codeError(s string) error {
	switch s {
	case "invalid":
		return ErrInvalid
	case "denied":
		return ErrDenied
	case "conflict":
		return ErrConflict
	case "uncertain":
		return ErrUncertain
	case "storage":
		return ErrStorage
	case "capacity":
		return ErrCapacity
	case "unavailable":
		return ErrUnavailable
	default:
		return ErrInvalid
	}
}

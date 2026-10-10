// Package transport carries native terminal bytes over a private bp/1
// connection. Frames have no semantic prompt or permission meaning.
package transport

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

const MaxFrame = 32 << 10
const Upgrade = "bp-terminal/1"
const ioTimeout = 5 * time.Second

type Kind byte

const (
	Ready Kind = iota + 1
	Input
	Output
	Resize
	Close
	Failure
)

type Frame struct {
	Kind Kind
	Data []byte
}
type Request struct {
	HostID domain.ID             `json:"host_id"`
	Lease  protocol.LeaseRequest `json:"lease"`
	Rows   uint16                `json:"rows"`
	Cols   uint16                `json:"cols"`
	Term   string                `json:"term"`
}

// Receipt excludes home socket paths, native argv, and lease/login tokens.
type Receipt struct {
	HostID      domain.ID `json:"host_id"`
	SessionID   domain.ID `json:"session_id"`
	ExecutionID domain.ID `json:"execution_id"`
	Generation  uint64    `json:"generation"`
	OperationID domain.ID `json:"operation_id"`
	ClientID    string    `json:"client_id"`
	ReadOnly    bool      `json:"read_only"`
}

type Stream struct {
	conn      net.Conn
	reader    *bufio.Reader
	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
	Receipt   Receipt
}

func newStream(ctx context.Context, conn net.Conn, reader *bufio.Reader) *Stream {
	if reader == nil {
		reader = bufio.NewReader(conn)
	}
	s := &Stream{conn: conn, reader: reader, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.done:
		}
	}()
	return s
}

func (s *Stream) ReadFrame() (Frame, error) {
	var header [5]byte
	// A quiet native terminal may remain idle indefinitely. Once a peer starts
	// a frame it must finish that bounded frame, rather than pin ownership with
	// a partial header or payload forever.
	if _, err := io.ReadFull(s.reader, header[:1]); err != nil {
		return Frame{}, err
	}
	if err := s.conn.SetReadDeadline(time.Now().Add(ioTimeout)); err != nil {
		return Frame{}, err
	}
	defer s.conn.SetReadDeadline(time.Time{})
	if _, err := io.ReadFull(s.reader, header[1:]); err != nil {
		return Frame{}, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if header[0] < byte(Ready) || header[0] > byte(Failure) || length > MaxFrame {
		return Frame{}, errors.New("invalid or oversized terminal frame")
	}
	f := Frame{Kind: Kind(header[0]), Data: make([]byte, int(length))}
	if _, err := io.ReadFull(s.reader, f.Data); err != nil {
		return Frame{}, err
	}
	return f, nil
}

func (s *Stream) WriteFrame(f Frame) error {
	if f.Kind < Ready || f.Kind > Failure || len(f.Data) > MaxFrame {
		return errors.New("invalid or oversized terminal frame")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(ioTimeout)); err != nil {
		return err
	}
	defer s.conn.SetWriteDeadline(time.Time{})
	var header [5]byte
	header[0] = byte(f.Kind)
	binary.BigEndian.PutUint32(header[1:], uint32(len(f.Data)))
	if err := writeAll(s.conn, header[:]); err != nil {
		return err
	}
	return writeAll(s.conn, f.Data)
}

func writeAll(w io.Writer, bytes []byte) error {
	for len(bytes) > 0 {
		n, err := w.Write(bytes)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		bytes = bytes[n:]
	}
	return nil
}

func (s *Stream) WriteInput(bytes []byte) error {
	if s.Receipt.ReadOnly {
		return errors.New("read-only stream rejects input")
	}
	for len(bytes) > 0 {
		length := len(bytes)
		if length > MaxFrame {
			length = MaxFrame
		}
		if err := s.WriteFrame(Frame{Input, bytes[:length]}); err != nil {
			return err
		}
		bytes = bytes[length:]
	}
	return nil
}

func (s *Stream) Resize(rows, cols uint16) error {
	if s.Receipt.ReadOnly {
		return errors.New("read-only stream rejects resize")
	}
	if rows == 0 || cols == 0 || rows > 4096 || cols > 4096 {
		return errors.New("invalid terminal size")
	}
	data := make([]byte, 4)
	binary.BigEndian.PutUint16(data, rows)
	binary.BigEndian.PutUint16(data[2:], cols)
	return s.WriteFrame(Frame{Resize, data})
}

func (s *Stream) Close() error {
	var err error
	s.closeOnce.Do(func() { close(s.done); err = s.conn.Close() })
	return err
}

func (s *Stream) Done() <-chan struct{} { return s.done }

func receiptFor(request Request) Receipt {
	l := request.Lease
	return Receipt{request.HostID, l.SessionID, l.ExecutionID, l.Generation, l.OperationID, l.ClientID, l.ReadOnly}
}

func (r Request) validate() error {
	l := r.Lease
	if r.HostID == "" || l.SessionID == "" || l.ExecutionID == "" || l.Generation == 0 || l.OperationID == "" || l.ClientID == "" || l.Token != "" {
		return errors.New("terminal stream requires exact acquisition identity, without a supplied token")
	}
	if r.Rows == 0 || r.Cols == 0 || r.Rows > 4096 || r.Cols > 4096 {
		return errors.New("terminal size must be between 1 and 4096")
	}
	if len(r.Term) > 80 || strings.IndexFunc(r.Term, func(c rune) bool {
		return !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("_+.-", c))
	}) != -1 {
		return errors.New("invalid terminal declaration")
	}
	return nil
}

func readReady(s *Stream, request Request) error {
	f, err := s.ReadFrame()
	if err != nil {
		return err
	}
	if f.Kind != Ready {
		return errors.New("terminal stream did not provide a ready receipt")
	}
	if err := json.Unmarshal(f.Data, &s.Receipt); err != nil {
		return err
	}
	if s.Receipt != receiptFor(request) {
		return errors.New("terminal receipt differs from requested acquisition")
	}
	return nil
}

type Client struct {
	*protocol.Client
	socket string
}

func NewClient(socket string) *Client { return &Client{protocol.NewClient(socket), socket} }

func (c *Client) Open(ctx context.Context, request Request) (*Stream, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, err
	}
	s := newStream(ctx, conn, nil)
	fail := func(err error) (*Stream, error) { _ = s.Close(); return nil, err }
	if err := conn.SetDeadline(time.Now().Add(ioTimeout)); err != nil {
		return fail(err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return fail(err)
	}
	r, err := http.NewRequest("POST", "http://backplane/v1/terminal", strings.NewReader(string(data)))
	if err != nil {
		return fail(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Backplane-Protocol", protocol.Version)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", Upgrade)
	if err := r.Write(conn); err != nil {
		return fail(err)
	}
	response, err := http.ReadResponse(s.reader, r)
	if err != nil {
		return fail(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols || response.Header.Get("Upgrade") != Upgrade {
		defer response.Body.Close()
		var failure protocol.Error
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure)
		return fail(&protocol.HTTPError{StatusCode: response.StatusCode, Message: failure.Message})
	}
	if err := readReady(s, request); err != nil {
		return fail(err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fail(err)
	}
	return s, nil
}

type OpenFunc func(context.Context, Request) (*Stream, error)

// Handler serves only the native terminal upgrade. Mount it beside the host
// control handler on a private socket, or bind Open to an authenticated entry.
func Handler(open OpenFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/terminal" || r.Header.Get("X-Backplane-Protocol") != protocol.Version || r.Header.Get("Upgrade") != Upgrade || !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
			httpFailure(w, 400, "private bp/1 terminal upgrade required")
			return
		}
		var request Request
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			httpFailure(w, 400, "invalid terminal request")
			return
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			httpFailure(w, 400, "one terminal request required")
			return
		}
		if err := request.validate(); err != nil {
			httpFailure(w, 400, err.Error())
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			httpFailure(w, 500, "private terminal upgrade unavailable")
			return
		}
		upstream, err := open(r.Context(), request)
		if err != nil {
			status := 503
			var h *protocol.HTTPError
			var conflict domain.ConflictError
			if errors.As(err, &h) {
				status = h.StatusCode
			} else if errors.As(err, &conflict) {
				status = 409
			}
			httpFailure(w, status, err.Error())
			return
		}
		defer upstream.Close()
		conn, buffered, err := hijacker.Hijack()
		if err != nil {
			return
		}
		downstream := newStream(r.Context(), conn, buffered.Reader)
		defer downstream.Close()
		_, err = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + Upgrade + "\r\nX-Backplane-Protocol: " + protocol.Version + "\r\n\r\n")
		if err != nil || buffered.Flush() != nil {
			return
		}
		data, _ := json.Marshal(upstream.Receipt)
		if err := downstream.WriteFrame(Frame{Ready, data}); err != nil {
			return
		}
		bridge(downstream, upstream)
	})
}

func httpFailure(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.Error{Message: message})
}

func bridge(a, b *Stream) {
	done := make(chan struct{}, 2)
	copyFrames := func(dst, src *Stream) {
		defer func() { done <- struct{}{} }()
		for {
			f, err := src.ReadFrame()
			if err != nil {
				return
			}
			if err := dst.WriteFrame(f); err != nil || f.Kind == Close || f.Kind == Failure {
				return
			}
		}
	}
	go copyFrames(a, b)
	go copyFrames(b, a)
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}

func terminalError(detail string) error { return fmt.Errorf("native terminal unavailable: %s", detail) }

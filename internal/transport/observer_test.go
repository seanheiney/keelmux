package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/protocol"
)

const observerTestReady = `{"host_id":"host_fixture","session_id":"session_fixture","execution_id":"execution_fixture","generation":7,"operation_id":"attach_fixture","client_id":"client_fixture","read_only":true}`

func observerTestRequest() Request {
	return Request{HostID: "host_fixture", Lease: protocol.LeaseRequest{
		SessionID: "session_fixture", ExecutionID: "execution_fixture", Generation: 7,
		OperationID: "attach_fixture", ClientID: "client_fixture", ReadOnly: true,
	}, Rows: 24, Cols: 80, Term: "xterm-256color"}
}

type observerTestConn struct {
	net.Conn
	reads        atomic.Int32
	written      atomic.Int64
	closes       atomic.Int32
	readOnce     sync.Once
	closeOnce    sync.Once
	readStarted  chan struct{}
	closed       chan struct{}
	mu           sync.Mutex
	deadlines    []time.Time
	deadlineFail int
}

func observerTrackedConn(c net.Conn) *observerTestConn {
	return &observerTestConn{Conn: c, readStarted: make(chan struct{}), closed: make(chan struct{})}
}

func (c *observerTestConn) Read(p []byte) (int, error) {
	c.reads.Add(1)
	c.readOnce.Do(func() { close(c.readStarted) })
	return c.Conn.Read(p)
}

func (c *observerTestConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Add(int64(n))
	return n, err
}

func (c *observerTestConn) Close() error {
	c.closes.Add(1)
	err := c.Conn.Close()
	c.closeOnce.Do(func() { close(c.closed) })
	return err
}

func (c *observerTestConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadlines = append(c.deadlines, deadline)
	fail := c.deadlineFail == len(c.deadlines)
	c.mu.Unlock()
	if fail {
		return errors.New("fixture deadline unavailable")
	}
	return c.Conn.SetDeadline(deadline)
}

func observerWait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("owned connection worker did not finish")
	}
}

// This emits real length-framed bytes independently of Stream.WriteFrame, so
// the receiver's admission and direction checks are actually exercised.
func observerSend(conn net.Conn, f Frame) error {
	if err := conn.SetWriteDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	data := make([]byte, 5+len(f.Data))
	data[0] = byte(f.Kind)
	binary.BigEndian.PutUint32(data[1:5], uint32(len(f.Data)))
	copy(data[5:], f.Data)
	_, err := io.Copy(conn, bytes.NewReader(data))
	return err
}

func observerAdmission(t *testing.T, ctx context.Context, request Request, first Frame, failDeadline int) (*Stream, *observerTestConn, net.Conn, error) {
	t.Helper()
	local, peer := net.Pipe()
	conn := observerTrackedConn(local)
	conn.deadlineFail = failDeadline
	t.Cleanup(func() { _ = local.Close(); _ = peer.Close() })
	written := make(chan struct{})
	go func() { _ = observerSend(peer, first); close(written) }()
	stream, err := ReadOnlyStream(ctx, conn, request)
	if stream != nil {
		t.Cleanup(func() { _ = stream.Close() })
	}
	observerWait(t, written)
	return stream, conn, peer, err
}

func observerOpen(t *testing.T, ctx context.Context) (*Stream, *observerTestConn, net.Conn) {
	t.Helper()
	stream, conn, peer, err := observerAdmission(t, ctx, observerTestRequest(), Frame{Ready, []byte(observerTestReady)}, 0)
	if err != nil || stream == nil {
		t.Fatalf("actual exact Ready refused: %v", err)
	}
	return stream, conn, peer
}

func TestReadOnlyObserverRejectsInvalidAcquisitionWithoutIO(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Request)
	}{
		{"writable", func(r *Request) { r.Lease.ReadOnly = false }},
		{"supplied-token", func(r *Request) { r.Lease.Token = "lease_foreign" }},
		{"missing-host", func(r *Request) { r.HostID = "" }},
		{"missing-session", func(r *Request) { r.Lease.SessionID = "" }},
		{"missing-execution", func(r *Request) { r.Lease.ExecutionID = "" }},
		{"missing-generation", func(r *Request) { r.Lease.Generation = 0 }},
		{"missing-operation", func(r *Request) { r.Lease.OperationID = "" }},
		{"missing-client", func(r *Request) { r.Lease.ClientID = "" }},
		{"zero-rows", func(r *Request) { r.Rows = 0 }},
		{"zero-cols", func(r *Request) { r.Cols = 0 }},
		{"oversized-rows", func(r *Request) { r.Rows = 4097 }},
		{"oversized-cols", func(r *Request) { r.Cols = 4097 }},
		{"terminal-control", func(r *Request) { r.Term = "xterm\n" }},
		{"oversized-terminal", func(r *Request) { r.Term = strings.Repeat("x", 81) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			local, peer := net.Pipe()
			defer peer.Close()
			conn := observerTrackedConn(local)
			request := observerTestRequest()
			tc.mutate(&request)
			stream, err := ReadOnlyStream(context.Background(), conn, request)
			if stream != nil || err == nil || conn.reads.Load() != 0 || conn.written.Load() != 0 || conn.closes.Load() != 1 {
				t.Fatalf("invalid acquisition performed IO or retained connection: stream=%v err=%v reads=%d writes=%d closes=%d", stream != nil, err, conn.reads.Load(), conn.written.Load(), conn.closes.Load())
			}
			if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("owned connection not closed: %v", err)
			}
		})
	}
	if stream, err := ReadOnlyStream(context.Background(), nil, observerTestRequest()); stream != nil || err == nil {
		t.Fatal("nil connection accepted")
	}
}

func TestReadOnlyObserverRequiresStrictActualReady(t *testing.T) {
	base := strings.TrimSuffix(observerTestReady, "}")
	cases := map[string][]byte{
		"duplicate-equal":       []byte(base + `,"read_only":true}`),
		"duplicate-conflicting": []byte(base + `,"read_only":false,"read_only":true}`),
		"escaped-duplicate":     []byte(base + `,"host\u005fid":"host_fixture"}`),
		"aliased-field":         []byte(strings.Replace(observerTestReady, `"host_id"`, `"HOST_ID"`, 1)),
		"aliased-additional":    []byte(base + `,"Host_ID":"host_fixture"}`),
		"unknown-token":         []byte(base + `,"token":"lease_private"}`),
		"unknown-argv":          []byte(base + `,"argv":["unreviewed"]}`),
		"trailing-value":        []byte(observerTestReady + `{}`),
		"array":                 []byte("[" + observerTestReady + "]"),
		"null-object":           []byte("null"),
		"wrong-generation-type": []byte(strings.Replace(observerTestReady, `"generation":7`, `"generation":"7"`, 1)),
		"wrong-readonly-type":   []byte(strings.Replace(observerTestReady, `"read_only":true`, `"read_only":"true"`, 1)),
		"oversized-ready":       []byte(observerTestReady + strings.Repeat(" ", 4097)),
		"malformed":             []byte(base),
		"invalid-utf8":          append([]byte(base), 0xff, '}'),
	}
	fields := []string{"host_id", "session_id", "execution_id", "generation", "operation_id", "client_id", "read_only"}
	for _, field := range fields {
		var missing, null map[string]json.RawMessage
		if err := json.Unmarshal([]byte(observerTestReady), &missing); err != nil {
			t.Fatal(err)
		}
		delete(missing, field)
		data, err := json.Marshal(missing)
		if err != nil {
			t.Fatal(err)
		}
		cases["missing-"+field] = data
		if err := json.Unmarshal([]byte(observerTestReady), &null); err != nil {
			t.Fatal(err)
		}
		null[field] = json.RawMessage("null")
		data, err = json.Marshal(null)
		if err != nil {
			t.Fatal(err)
		}
		cases["null-"+field] = data
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			stream, conn, _, err := observerAdmission(t, context.Background(), observerTestRequest(), Frame{Ready, data}, 0)
			if stream != nil || err == nil || conn.written.Load() != 0 || conn.closes.Load() != 1 {
				t.Fatalf("ambiguous Ready admitted or connection retained: stream=%v err=%v writes=%d closes=%d", stream != nil, err, conn.written.Load(), conn.closes.Load())
			}
		})
	}
	for _, kind := range []Kind{Input, Output, Resize, Close, Failure, Kind(0), Kind(255)} {
		t.Run("wrong-first-frame-"+strconv.Itoa(int(kind)), func(t *testing.T) {
			stream, conn, _, err := observerAdmission(t, context.Background(), observerTestRequest(), Frame{kind, []byte(observerTestReady)}, 0)
			if stream != nil || err == nil || conn.closes.Load() != 1 {
				t.Fatalf("historical receipt without actual Ready admitted: kind=%d err=%v", kind, err)
			}
		})
	}
}

func TestReadOnlyObserverRejectsEveryReadyIdentityMismatch(t *testing.T) {
	for field, value := range map[string]any{
		"host_id": "host_other", "session_id": "session_other", "execution_id": "execution_other",
		"generation": 8, "operation_id": "attach_other", "client_id": "client_other", "read_only": false,
	} {
		t.Run(field, func(t *testing.T) {
			var receipt map[string]any
			if err := json.Unmarshal([]byte(observerTestReady), &receipt); err != nil {
				t.Fatal(err)
			}
			receipt[field] = value
			data, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			stream, conn, _, err := observerAdmission(t, context.Background(), observerTestRequest(), Frame{Ready, data}, 0)
			if stream != nil || err == nil || conn.closes.Load() != 1 {
				t.Fatalf("different actual acquisition admitted: field=%s err=%v", field, err)
			}
		})
	}
}

func TestReadOnlyObserverReceiptMutationCannotWrite(t *testing.T) {
	stream, conn, peer := observerOpen(t, context.Background())
	// The public projection is writable by callers; authority must not be.
	stream.Receipt = Receipt{ReadOnly: false, ClientID: "forged-writer"}
	readDone := make(chan struct{})
	var received []byte
	go func() { received, _ = io.ReadAll(peer); close(readDone) }()
	t.Cleanup(func() { _ = stream.Close(); observerWait(t, readDone) })
	for _, input := range [][]byte{nil, []byte("native input\n")} {
		if err := stream.WriteInput(input); err == nil {
			t.Fatal("receipt mutation admitted input")
		}
	}
	if err := stream.Resize(30, 100); err == nil {
		t.Fatal("receipt mutation admitted resize")
	}
	for _, f := range []Frame{
		{Ready, []byte(observerTestReady)}, {Input, nil}, {Input, []byte("raw input")},
		{Output, nil}, {Output, []byte("forged output")}, {Resize, []byte{0, 24, 0, 80}},
		{Failure, []byte("forged failure")}, {Close, []byte("not empty")},
		{Kind(0), nil}, {Kind(255), nil},
	} {
		if err := stream.WriteFrame(f); err == nil {
			t.Fatalf("raw frame bypassed observer fence: kind=%d", f.Kind)
		}
	}
	if conn.written.Load() != 0 {
		t.Fatalf("forbidden client bytes reached peer: %d", conn.written.Load())
	}
	if err := stream.WriteFrame(Frame{Close, nil}); err != nil {
		t.Fatalf("allowed empty Close refused: %v", err)
	}
	_ = stream.Close()
	observerWait(t, readDone)
	if !bytes.Equal(received, []byte{byte(Close), 0, 0, 0, 0}) || conn.written.Load() != 5 {
		t.Fatalf("peer received anything beyond exact empty Close: %x", received)
	}
}

func TestReadOnlyObserverRejectsServerWriterDirections(t *testing.T) {
	for _, f := range []Frame{{Ready, []byte(observerTestReady)}, {Input, []byte("input")}, {Resize, []byte{0, 24, 0, 80}}, {Close, []byte("nonempty")}} {
		t.Run("kind-"+strconv.Itoa(int(f.Kind)), func(t *testing.T) {
			stream, conn, peer := observerOpen(t, context.Background())
			stream.Receipt.ReadOnly = false
			sent := make(chan struct{})
			go func() { _ = observerSend(peer, f); close(sent) }()
			if _, err := stream.ReadFrame(); err == nil {
				t.Fatalf("server writer direction admitted: kind=%d", f.Kind)
			}
			observerWait(t, conn.closed)
			observerWait(t, sent)
			if conn.closes.Load() != 1 || conn.written.Load() != 0 {
				t.Fatal("forbidden direction did not close its owned connection")
			}
		})
	}
}

func TestReadOnlyObserverPreservesUnicodeOutputAndServerClose(t *testing.T) {
	stream, conn, peer := observerOpen(t, context.Background())
	if stream.Receipt != (Receipt{HostID: "host_fixture", SessionID: "session_fixture", ExecutionID: "execution_fixture", Generation: 7, OperationID: "attach_fixture", ClientID: "client_fixture", ReadOnly: true}) {
		t.Fatalf("exact Ready projection changed: %+v", stream.Receipt)
	}
	output := []byte("你好 café e\u0301 🧭\x1b[31mred\x1b[0m\r\n")
	sent := make(chan error, 1)
	go func() {
		// Split within a UTF-8 code point: terminal transport preserves bytes.
		for _, data := range [][]byte{output[:1], output[1:11], output[11:]} {
			if err := observerSend(peer, Frame{Output, data}); err != nil {
				sent <- err
				return
			}
		}
		if err := observerSend(peer, Frame{Failure, []byte("bounded stream failure")}); err != nil {
			sent <- err
			return
		}
		err := observerSend(peer, Frame{Close, nil})
		_ = peer.Close()
		sent <- err
	}()
	var got []byte
	for range 3 {
		f, err := stream.ReadFrame()
		if err != nil || f.Kind != Output {
			t.Fatalf("terminal output lost: kind=%d err=%v", f.Kind, err)
		}
		got = append(got, f.Data...)
	}
	if !bytes.Equal(got, output) {
		t.Fatalf("Unicode/ANSI bytes changed: got=%q want=%q", got, output)
	}
	f, err := stream.ReadFrame()
	if err != nil || f.Kind != Failure || string(f.Data) != "bounded stream failure" {
		t.Fatalf("server Failure lost: %+v %v", f, err)
	}
	f, err = stream.ReadFrame()
	if err != nil || f.Kind != Close || len(f.Data) != 0 {
		t.Fatalf("complete empty server Close lost: %+v %v", f, err)
	}
	if _, err := stream.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("closed peer did not stay EOF: %v", err)
	}
	if err := <-sent; err != nil || conn.written.Load() != 0 {
		t.Fatalf("output required writer authority: send=%v written=%d", err, conn.written.Load())
	}
}

func TestReadOnlyObserverOwnsConnectionOnCancellation(t *testing.T) {
	t.Run("before-ready", func(t *testing.T) {
		local, peer := net.Pipe()
		defer peer.Close()
		conn := observerTrackedConn(local)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		type admission struct {
			stream *Stream
			err    error
		}
		result := make(chan admission, 1)
		go func() {
			stream, err := ReadOnlyStream(ctx, conn, observerTestRequest())
			if stream != nil {
				_ = stream.Close()
			}
			result <- admission{stream, err}
		}()
		observerWait(t, conn.readStarted)
		cancel()
		observerWait(t, conn.closed)
		select {
		case got := <-result:
			if got.stream != nil || got.err == nil {
				t.Fatal("canceled Ready wait succeeded")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("canceled admission did not finish")
		}
		if conn.closes.Load() != 1 || conn.written.Load() != 0 {
			t.Fatal("canceled admission did not own exact connection cleanup")
		}
	})
	t.Run("quiet-after-ready", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		stream, conn, _ := observerOpen(t, ctx)
		conn.mu.Lock()
		deadlines := append([]time.Time(nil), conn.deadlines...)
		conn.mu.Unlock()
		if len(deadlines) != 2 || deadlines[0].IsZero() || !deadlines[1].IsZero() {
			t.Fatal("handshake deadline retained on quiet live stream")
		}
		readDone := make(chan error, 1)
		go func() { _, err := stream.ReadFrame(); readDone <- err }()
		cancel()
		observerWait(t, stream.Done())
		observerWait(t, conn.closed)
		select {
		case err := <-readDone:
			if err == nil {
				t.Fatal("canceled quiet read succeeded")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("owned quiet reader survived cancellation")
		}
		_ = stream.Close()
		_ = stream.Close()
		if conn.closes.Load() != 1 {
			t.Fatalf("owned connection closed more than once: %d", conn.closes.Load())
		}
	})
}

func TestReadOnlyObserverDeadlineFailureClosesOwnedConnection(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run("phase-"+strconv.Itoa(failAt), func(t *testing.T) {
			stream, conn, _, err := observerAdmission(t, context.Background(), observerTestRequest(), Frame{Ready, []byte(observerTestReady)}, failAt)
			if stream != nil || err == nil || conn.closes.Load() != 1 || conn.written.Load() != 0 {
				t.Fatalf("deadline failure retained owned connection: phase=%d err=%v", failAt, err)
			}
		})
	}
}

func TestReadOnlyObserverLostReadyClosesOwnedConnection(t *testing.T) {
	for name, wire := range map[string][]byte{
		"eof-before-frame": nil,
		"partial-header":   {byte(Ready)},
		"partial-payload":  {byte(Ready), 0, 0, 0, 20, '{', '}'},
	} {
		t.Run(name, func(t *testing.T) {
			local, peer := net.Pipe()
			conn := observerTrackedConn(local)
			defer local.Close()
			defer peer.Close()
			sent := make(chan struct{})
			go func() {
				_, _ = peer.Write(wire)
				_ = peer.Close()
				close(sent)
			}()
			stream, err := ReadOnlyStream(context.Background(), conn, observerTestRequest())
			observerWait(t, sent)
			if stream != nil || err == nil || conn.closes.Load() != 1 || conn.written.Load() != 0 {
				t.Fatalf("missing Ready retained stream or connection: stream=%v err=%v closes=%d", stream != nil, err, conn.closes.Load())
			}
		})
	}
}

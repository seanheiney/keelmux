package peerbridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/transport"
)

type fixture struct {
	t                         *testing.T
	dir, path                 string
	policy                    ReadPolicy
	session                   domain.Session
	listener                  net.Listener
	http                      *http.Server
	mu                        sync.Mutex
	attaches, opens, releases int
	leases                    map[domain.ID]domain.Lease
	terminals                 map[domain.ID]net.Conn
	attachHold, openHold      chan struct{}
	attachSeen, openSeen      chan struct{}
	badHost                   bool
}

func newFixture(t *testing.T) *fixture {
	temporary, e := os.MkdirTemp("/tmp", "kpb-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(temporary) })
	dir, e := filepath.EvalSymlinks(temporary)
	if e != nil {
		t.Fatal(e)
	}
	os.Chmod(dir, 0700)
	f := &fixture{t: t, dir: dir, path: filepath.Join(dir, "authority.json"), leases: map[domain.ID]domain.Lease{}, terminals: map[domain.ID]net.Conn{}, attachSeen: make(chan struct{}, 16), openSeen: make(chan struct{}, 16)}
	host := domain.NewID("host")
	f.session = domain.NewSession(host, "fixture", dir, "fixture", "none")
	f.session.Execution.State = "running"
	f.policy = ReadPolicy{Version: Version, ScopeID: domain.NewID("scope"), HostID: host, Principal: "owner", Socket: filepath.Join(dir, "home.sock"), Revision: 1, Enabled: true, Sessions: []SessionActions{{f.session.ID, true, true}}}
	f.listener, e = net.Listen("unix", f.policy.Socket)
	if e != nil {
		t.Fatal(e)
	}
	os.Chmod(f.policy.Socket, 0600)
	f.http = &http.Server{Handler: http.HandlerFunc(f.handle)}
	go f.http.Serve(f.listener)
	if e = ProvisionPolicy(context.Background(), f.path, f.policy); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		f.http.Close()
		f.mu.Lock()
		for _, c := range f.terminals {
			c.Close()
		}
		f.mu.Unlock()
	})
	return f
}
func (f *fixture) handle(w http.ResponseWriter, q *http.Request) {
	switch q.URL.Path {
	case "/v1/sessions":
		json.NewEncoder(w).Encode(protocol.Inventory{Version: protocol.Version, Sessions: []domain.Session{f.session}})
	case "/v1/peer-inventory":
		f.mu.Lock()
		host := f.policy.HostID
		if f.badHost {
			host = domain.NewID("host")
		}
		f.mu.Unlock()
		w.Header().Set("X-Backplane-Protocol", protocol.Version)
		json.NewEncoder(w).Encode(inventory{"keelmux-peer/1", host, time.Now().UTC(), []domain.Session{f.session}})
	case "/v1/attach":
		var r protocol.LeaseRequest
		if json.NewDecoder(q.Body).Decode(&r) != nil {
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		f.attaches++
		l, ok := f.leases[r.OperationID]
		if !ok {
			l = domain.Lease{SessionID: r.SessionID, ExecutionID: r.ExecutionID, Generation: r.Generation, OperationID: r.OperationID, ClientID: r.ClientID, Token: string(domain.NewID("lease"))}
			f.leases[r.OperationID] = l
		}
		hold := f.attachHold
		f.mu.Unlock()
		f.attachSeen <- struct{}{}
		if hold != nil {
			<-hold
		}
		json.NewEncoder(w).Encode(domain.AttachSpec{Argv: []string{"private-native-argv"}, Lease: l, ReadOnly: true})
	case "/v1/release":
		var r protocol.LeaseRequest
		json.NewDecoder(q.Body).Decode(&r)
		f.mu.Lock()
		l, ok := f.leases[r.OperationID]
		if ok && l.Token == r.Token {
			f.releases++
			delete(f.leases, r.OperationID)
		}
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(204)
			return
		}
		if l.Token != r.Token {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(204)
	case "/v1/terminal":
		var r transport.Request
		json.NewDecoder(q.Body).Decode(&r)
		h := w.(http.Hijacker)
		c, b, e := h.Hijack()
		if e != nil {
			return
		}
		f.mu.Lock()
		f.opens++
		f.terminals[r.Lease.OperationID] = c
		hold := f.openHold
		f.mu.Unlock()
		b.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + transport.Upgrade + "\r\n\r\n")
		b.Flush()
		f.openSeen <- struct{}{}
		if hold != nil {
			<-hold
		}
		receipt := transport.Receipt{HostID: r.HostID, SessionID: r.Lease.SessionID, ExecutionID: r.Lease.ExecutionID, Generation: r.Lease.Generation, OperationID: r.Lease.OperationID, ClientID: r.Lease.ClientID, ReadOnly: true}
		data, _ := json.Marshal(receipt)
		c.SetWriteDeadline(time.Now().Add(time.Second))
		_ = terminalWrite(c, transport.Ready, data)
		c.SetWriteDeadline(time.Time{})
		defer c.Close()
		var hbuf [5]byte
		_, _ = io.ReadFull(b.Reader, hbuf[:])
		if hbuf[0] != 0 && hbuf[0] != byte(transport.Close) {
			f.t.Errorf("forbidden native frame %d", hbuf[0])
		}
	default:
		w.WriteHeader(501)
	}
}
func terminalWrite(w io.Writer, k transport.Kind, data []byte) error {
	b := make([]byte, 5+len(data))
	b[0] = byte(k)
	binary.BigEndian.PutUint32(b[1:], uint32(len(data)))
	copy(b[5:], data)
	return writeBytes(w, b)
}
func (f *fixture) request() protocol.LeaseRequest {
	return protocol.LeaseRequest{SessionID: f.session.ID, ExecutionID: f.session.Execution.ID, Generation: 1, OperationID: domain.NewID("attach"), ClientID: "viewer", ReadOnly: true}
}
func (f *fixture) connect() (*Client, <-chan error) {
	f.t.Helper()
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), f.path, b, b); close(done) }()
	c, e := NewClient(a, Selector{f.policy.ScopeID, f.policy.HostID, f.policy.Principal})
	if e != nil {
		f.t.Fatal(e)
	}
	if _, e = c.Hello(context.Background()); e != nil {
		c.Close()
		f.t.Fatal(e)
	}
	f.t.Cleanup(func() {
		c.Close()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			f.t.Error("server cleanup did not complete")
		}
	})
	return c, done
}
func waitSignal(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(time.Second):
		t.Fatal("fixture phase not reached")
	}
}
func waitDone(t *testing.T, c <-chan error) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(4 * time.Second):
		t.Fatal("server did not close")
	}
}
func (f *fixture) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attaches, f.opens, f.releases
}
func (f *fixture) emit(op domain.ID, data []byte) {
	f.t.Helper()
	f.mu.Lock()
	c := f.terminals[op]
	f.mu.Unlock()
	if c == nil {
		f.t.Fatal("no exact terminal")
	}
	c.SetWriteDeadline(time.Now().Add(time.Second))
	e := terminalWrite(c, transport.Output, data)
	c.SetWriteDeadline(time.Time{})
	if e != nil {
		f.t.Fatal(e)
	}
}

func TestReadBridgeLifecycleProjectionExactReadyAndNoReopen(t *testing.T) {
	f := newFixture(t)
	c, done := f.connect()
	v, e := c.List(context.Background())
	if e != nil || len(v) != 1 || !v[0].Capabilities.NativeAttach || !v[0].Capabilities.ReadOnlyAttach || v[0].Execution.Runtime != (domain.RuntimeRef{}) || v[0].Capabilities.Stop {
		t.Fatalf("projection %v %v", v, e)
	}
	r := f.request()
	a, e := c.Attach(context.Background(), r)
	if e != nil || len(a.Argv) != 0 {
		t.Fatalf("attach %v %v", a, e)
	}
	again, e := c.Attach(context.Background(), r)
	if e != nil || again.Lease != a.Lease {
		t.Fatal("confirmed acquisition replay")
	}
	req := transport.Request{HostID: f.policy.HostID, Lease: r, Rows: 24, Cols: 80, Term: "xterm-256color"}
	s, e := c.Open(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	if s.Receipt.OperationID != r.OperationID || s.Receipt.ExecutionID != r.ExecutionID || s.Receipt.Generation != 1 || !s.Receipt.ReadOnly {
		t.Fatal("receipt changed")
	}
	s.Receipt.ReadOnly = false
	if s.WriteInput([]byte("forbidden")) == nil || s.Resize(20, 40) == nil || s.WriteFrame(transport.Frame{Kind: transport.Input, Data: []byte("forbidden")}) == nil || s.WriteFrame(transport.Frame{Kind: transport.Output}) == nil {
		t.Fatal("mutable receipt bypass")
	}
	want := []byte("\x1b[32m完整🙂 é\x1b[0m\r\n")
	go f.emit(r.OperationID, want)
	frame, e := s.ReadFrame()
	if e != nil || frame.Kind != transport.Output || !bytes.Equal(frame.Data, want) {
		t.Fatalf("bytes %q %v", frame.Data, e)
	}
	s.Close()
	if _, e = c.Open(context.Background(), req); !errors.Is(e, ErrUncertain) {
		t.Fatalf("same-operation reopen: %v", e)
	}
	r.Token = a.Lease.Token
	if e = c.Release(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	if e = c.Release(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	acq, opened, released := f.counts()
	if acq != 1 || opened != 1 || released != 1 {
		t.Fatalf("effects %d %d %d", acq, opened, released)
	}
	c.Close()
	waitDone(t, done)
}
func TestHomeRevokeRegrantCancelsQuietOriginalRevision(t *testing.T) {
	f := newFixture(t)
	c, done := f.connect()
	r := f.request()
	a, e := c.Attach(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	s, e := c.Open(context.Background(), transport.Request{HostID: f.policy.HostID, Lease: r, Rows: 24, Cols: 80})
	if e != nil {
		t.Fatal(e)
	}
	next := f.policy
	next.Enabled = false
	if e = UpdatePolicy(context.Background(), f.path, 1, next); e != nil {
		t.Fatal(e)
	}
	next.Enabled = true
	if e = UpdatePolicy(context.Background(), f.path, 2, next); e != nil {
		t.Fatal(e)
	}
	read := make(chan error, 1)
	go func() { _, e := s.ReadFrame(); read <- e }()
	select {
	case e := <-read:
		if e == nil {
			t.Fatal("old revision survived ABA")
		}
	case <-time.After(time.Second):
		t.Fatal("quiet revoke not observed")
	}
	c.Close()
	waitDone(t, done)
	newClient, _ := f.connect()
	r.Token = a.Lease.Token
	if e = newClient.Release(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	if _, e = newClient.Open(context.Background(), transport.Request{HostID: f.policy.HostID, Lease: tokenFree(r), Rows: 24, Cols: 80}); !errors.Is(e, ErrDenied) {
		t.Fatalf("regrant revived attempted op %v", e)
	}
}
func TestUnknownAcquireAttemptNeverUsesAttachAsLookup(t *testing.T) {
	f := newFixture(t)
	r := f.request()
	store, e := openStore(f.path)
	if e != nil {
		t.Fatal(e)
	}
	e = store.access(context.Background(), true, func(a *authority) error {
		a.Operations = append(a.Operations, operation{Request: r, Revision: 1})
		return nil
	})
	store.close()
	if e != nil {
		t.Fatal(e)
	}
	c, _ := f.connect()
	for i := 0; i < 2; i++ {
		if _, e = c.Attach(context.Background(), r); !errors.Is(e, ErrUncertain) {
			t.Fatalf("unknown retry %v", e)
		}
	}
	a, o, l := f.counts()
	if a != 0 || o != 0 || l != 0 {
		t.Fatal("unknown attempt created effect")
	}
}
func TestRevokeWhileHomeOpenBlockedNeverPublishesReady(t *testing.T) {
	f := newFixture(t)
	f.openHold = make(chan struct{})
	c, done := f.connect()
	r := f.request()
	if _, e := c.Attach(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	opened := make(chan error, 1)
	go func() {
		_, e := c.Open(context.Background(), transport.Request{HostID: f.policy.HostID, Lease: r, Rows: 24, Cols: 80})
		opened <- e
	}()
	waitSignal(t, f.openSeen)
	next := f.policy
	next.Enabled = false
	if e := UpdatePolicy(context.Background(), f.path, 1, next); e != nil {
		t.Fatal(e)
	}
	close(f.openHold)
	select {
	case e := <-opened:
		if e == nil {
			t.Fatal("old Ready admitted after revoke")
		}
	case <-time.After(time.Second):
		t.Fatal("pending revoke not canceled")
	}
	c.Close()
	waitDone(t, done)
	a, o, _ := f.counts()
	if a != 1 || o != 1 {
		t.Fatalf("effects %d %d", a, o)
	}
	store, e := openStore(f.path)
	if e != nil {
		t.Fatal(e)
	}
	defer store.close()
	store.access(context.Background(), false, func(a *authority) error {
		op := findOperation(a, r.OperationID)
		if op == nil || !op.OpenAttempted || op.ReadyObserved {
			t.Fatal("durable fence or no-Ready violated")
		}
		return nil
	})
}
func TestKnownCleanupAfterDisabledPolicyAndForeignReleaseDenied(t *testing.T) {
	f := newFixture(t)
	c, done := f.connect()
	r := f.request()
	a, e := c.Attach(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	waitDone(t, done)
	next := f.policy
	next.Enabled = false
	next.Sessions = []SessionActions{}
	if e = UpdatePolicy(context.Background(), f.path, 1, next); e != nil {
		t.Fatal(e)
	}
	c, _ = f.connect()
	if _, e = c.List(context.Background()); !errors.Is(e, ErrDenied) {
		t.Fatalf("disabled list %v", e)
	}
	r.Token = a.Lease.Token
	bad := r
	bad.ClientID = "foreign"
	if e = c.Release(context.Background(), bad); !errors.Is(e, ErrDenied) {
		t.Fatalf("foreign cleanup %v", e)
	}
	if e = c.Release(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	bad = r
	bad.Token = "substituted"
	if e = c.Release(context.Background(), bad); !errors.Is(e, ErrDenied) {
		t.Fatal("token substitution")
	}
}
func TestPolicyReadNoCreationStrictOwnershipAndCAS(t *testing.T) {
	f := newFixture(t)
	before, e := os.ReadDir(f.dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ReadPolicyFile(filepath.Join(f.dir, "missing.json")); e == nil {
		t.Fatal("missing read succeeded")
	}
	after, _ := os.ReadDir(f.dir)
	if len(before) != len(after) {
		t.Fatal("read created state")
	}
	alias := filepath.Join(f.dir, "alias.json")
	if e = os.Symlink(f.path, alias); e != nil {
		t.Fatal(e)
	}
	if _, e = ReadPolicyFile(alias); e == nil {
		t.Fatal("symlink authority")
	}
	os.Chmod(f.path, 0644)
	if _, e = ReadPolicyFile(f.path); e == nil {
		t.Fatal("public policy")
	}
	os.Chmod(f.path, 0600)
	next := f.policy
	next.Principal = "other"
	if e = UpdatePolicy(context.Background(), f.path, 1, next); !errors.Is(e, ErrConflict) {
		t.Fatal("principal rebound")
	}
	next = f.policy
	next.Enabled = false
	if e = UpdatePolicy(context.Background(), f.path, 2, next); !errors.Is(e, ErrConflict) {
		t.Fatal("stale revision")
	}
	if e = UpdatePolicy(context.Background(), f.path, 1, next); e != nil {
		t.Fatal(e)
	}
	if e = UpdatePolicy(context.Background(), f.path, 2, next); e != nil {
		t.Fatal(e)
	}
	p, e := ReadPolicyFile(f.path)
	if e != nil || p.Revision != 2 {
		t.Fatal("same-value update advanced")
	}
	os.Remove(f.path + ".lock")
	if _, e = ReadPolicyFile(f.path); e == nil {
		t.Fatal("missing lock repaired")
	}
	if _, e = os.Stat(f.path + ".lock"); !os.IsNotExist(e) {
		t.Fatal("read created missing lock")
	}
}
func TestStrictJSONAndWireDirectionsBoundBeforeAllocation(t *testing.T) {
	for _, b := range []string{`{"Revision":1}`, `{"reviſion":1}`, `{"revision":1,"Revision":2}`, `{"revision":1,"unknown":2}`, `{"revision":1} {}`, `{"revision":1,"revision":1}`} {
		var r revisionRequest
		if strict([]byte(b), ControlLimit, &r) == nil {
			t.Fatalf("accepted %s", b)
		}
	}
	for _, p := range []packet{{kind: tOutput, stream: 1}, {kind: tReady, request: 1, stream: 1}, {kind: tClose, stream: 1, data: []byte{1}}, {kind: tOpen, request: 1}, {kind: tag(99), request: 1}} {
		if p.valid(true) {
			t.Fatal("forbidden client direction")
		}
	}
	a, b := net.Pipe()
	w := &wire{in: b, out: b}
	defer a.Close()
	defer w.close()
	go func() {
		header := make([]byte, 21)
		binary.BigEndian.PutUint32(header, uint32(transport.MaxFrame+1))
		header[4] = byte(tOutput)
		writeBytes(a, header)
	}()
	if _, e := w.read(context.Background()); !errors.Is(e, ErrInvalid) {
		t.Fatal("oversized frame accepted")
	}
}
func TestWrongHomeIdentityAndSocketChangeHaveZeroEffects(t *testing.T) {
	f := newFixture(t)
	f.badHost = true
	a, b := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), f.path, b, b); close(done) }()
	c, _ := NewClient(a, Selector{f.policy.ScopeID, f.policy.HostID, f.policy.Principal})
	if _, e := c.Hello(context.Background()); e == nil {
		t.Fatal("foreign home accepted")
	}
	c.Close()
	waitDone(t, done)
	f.badHost = false
	c, nextDone := f.connect()
	os.Chmod(f.policy.Socket, 0666)
	if _, e := c.Attach(context.Background(), f.request()); e == nil {
		t.Fatal("public endpoint effect")
	}
	c.Close()
	waitDone(t, nextDone)
	aCount, oCount, rCount := f.counts()
	if aCount+oCount+rCount != 0 {
		t.Fatal("identity refusal had effects")
	}
}

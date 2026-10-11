package peerbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/transport"
	"golang.org/x/sys/unix"
)

// The connection owns its transport cleanup. Close must finish that cleanup and
// retain its refusal even if EOF aborts the client before its caller closes it.
type cleanupFaultConn struct {
	net.Conn
	once             sync.Once
	err              error
	entered, release chan struct{}
}

func (c *cleanupFaultConn) Close() error {
	c.once.Do(func() {
		c.Conn.Close()
		close(c.entered)
		<-c.release
	})
	return c.err
}

func TestClientCloseRetainsOwnedTransportCleanupFailure(t *testing.T) {
	for _, abortFirst := range []bool{false, true} {
		name := "caller"
		if abortFirst {
			name = "reader_eof"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a, b := net.Pipe()
			defer b.Close()
			fault := errors.New("fixture owned transport cleanup unconfirmed")
			owned := &cleanupFaultConn{Conn: a, err: fault, entered: make(chan struct{}), release: make(chan struct{})}
			defer func() {
				select {
				case <-owned.release:
				default:
					close(owned.release)
				}
				a.Close()
			}()
			serverDone := make(chan error, 1)
			go func() { serverDone <- Serve(context.Background(), f.path, b, b) }()
			c, e := NewClient(owned, Selector{f.policy.ScopeID, f.policy.HostID, f.policy.Principal})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = c.Hello(context.Background()); e != nil {
				t.Fatal(e)
			}
			if abortFirst {
				b.Close()
				waitSignal(t, owned.entered)
			}
			closed := make(chan error, 1)
			go func() { closed <- c.Close() }()
			waitSignal(t, owned.entered)
			select {
			case e = <-closed:
				t.Fatalf("close returned before owned cleanup completed: %v", e)
			default:
			}
			close(owned.release)
			select {
			case e = <-closed:
				if !errors.Is(e, fault) {
					t.Fatalf("cleanup refusal suppressed: %v", e)
				}
			case <-time.After(time.Second):
				t.Fatal("client cleanup did not join")
			}
			if e = c.Close(); !errors.Is(e, fault) {
				t.Fatalf("repeated close lost cleanup refusal: %v", e)
			}
			waitDone(t, serverDone)
			aCount, openCount, releaseCount := f.counts()
			if aCount != 0 || openCount != 0 || releaseCount != 0 {
				t.Fatal("close issued a home effect")
			}
		})
	}
}

func TestConcurrentReadOnlyStreamsAndControlStayBounded(t *testing.T) {
	f := newFixture(t)
	c, _ := f.connect()
	requests := make([]protocol.LeaseRequest, 8)
	streams := make([]*transport.Stream, 8)
	for i := range requests {
		requests[i] = f.request()
		if _, e := c.Attach(context.Background(), requests[i]); e != nil {
			t.Fatal(e)
		}
	}
	var wg sync.WaitGroup
	errc := make(chan error, 16)
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, e := c.Open(context.Background(), transport.Request{HostID: f.policy.HostID, Lease: requests[i], Rows: 24, Cols: 80})
			streams[i] = s
			errc <- e
		}(i)
	}
	wg.Wait()
	for range requests {
		if e := <-errc; e != nil {
			t.Fatal(e)
		}
	}
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := []byte{byte('A' + i)}
			f.emit(requests[i].OperationID, data)
			got, e := streams[i].ReadFrame()
			if e == nil && !bytes.Equal(got.Data, data) {
				e = ErrConflict
			}
			errc <- e
		}(i)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := c.List(context.Background())
			if e == nil && len(v) != 1 {
				e = ErrInvalid
			}
			errc <- e
		}()
	}
	wg.Wait()
	for i := 0; i < 16; i++ {
		if e := <-errc; e != nil {
			t.Fatal(e)
		}
	}
	for _, s := range streams {
		s.Close()
	}
	a, o, _ := f.counts()
	if a != 8 || o != 8 {
		t.Fatalf("effects %d %d", a, o)
	}
}
func TestStalledViewerDoesNotBlockHealthyControl(t *testing.T) {
	f := newFixture(t)
	c, _ := f.connect()
	r := f.request()
	if _, e := c.Attach(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	s, e := c.Open(context.Background(), transport.Request{HostID: f.policy.HostID, Lease: r, Rows: 24, Cols: 80})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	f.mu.Lock()
	homeConn := f.terminals[r.OperationID]
	f.mu.Unlock()
	emitted := make(chan struct{})
	go func() {
		defer close(emitted)
		homeConn.SetWriteDeadline(time.Now().Add(time.Second))
		for i := 0; i < 12; i++ {
			if terminalWrite(homeConn, transport.Output, bytes.Repeat([]byte{'x'}, transport.MaxFrame)) != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	v, e := c.List(ctx)
	if e != nil || len(v) != 1 {
		t.Fatalf("healthy control blocked %v", e)
	}
	select {
	case <-emitted:
	case <-time.After(2 * time.Second):
		t.Fatal("emitter not bounded")
	}
	s.Close()
	if _, e = c.List(context.Background()); e != nil {
		t.Fatalf("stalled viewer killed peer %v", e)
	}
}
func TestOpenStreamSurvivesControlBudget(t *testing.T) {
	f := newFixture(t)
	c, _ := f.connect()
	r := f.request()
	if _, e := c.Attach(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	s, e := c.Open(context.Background(), transport.Request{HostID: f.policy.HostID, Lease: r, Rows: 24, Cols: 80})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	timer := time.NewTimer(ControlTimeout + 50*time.Millisecond)
	defer timer.Stop()
	<-timer.C
	go f.emit(r.OperationID, []byte("still-live"))
	result := make(chan error, 1)
	go func() {
		got, e := s.ReadFrame()
		if e == nil && string(got.Data) != "still-live" {
			e = ErrInvalid
		}
		result <- e
	}()
	select {
	case e := <-result:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("live stream lost to control timer")
	}
}
func TestQueuedNavigationReplyCannotCrossRevocationCommit(t *testing.T) {
	for _, kind := range []tag{tListed, tAcquired, tReady, tOutput} {
		t.Run(errorTag(kind), func(t *testing.T) {
			f := newFixture(t)
			store, e := openStore(f.path)
			if e != nil {
				t.Fatal(e)
			}
			defer store.close()
			h, _ := newHome(f.policy.Socket)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			s := &server{ctx: ctx, cancel: cancel, wire: &wire{in: b, out: b}, store: store, home: h, policy: f.policy, control: make(chan packet, 16), wake: make(chan struct{}, 1), streams: map[uint64]*serverStream{}}
			if e = s.enqueue(packet{kind: kind, request: 1, stream: 1, data: []byte("private-marker")}, nil); e != nil {
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
			done := make(chan struct{})
			go func() { s.writer(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("stale writer not refused")
			}
			a.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			buf := make([]byte, 1)
			n, _ := a.Read(buf)
			if n != 0 {
				t.Fatal("late private reply leaked")
			}
		})
	}
}
func errorTag(t tag) string {
	switch t {
	case tListed:
		return "inventory"
	case tAcquired:
		return "lease"
	case tReady:
		return "ready"
	default:
		return "output"
	}
}
func TestUnopenedHistoricalAcquisitionNotRevivedByRegrant(t *testing.T) {
	f := newFixture(t)
	c, done := f.connect()
	r := f.request()
	if _, e := c.Attach(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	next := f.policy
	next.Enabled = false
	if e := UpdatePolicy(context.Background(), f.path, 1, next); e != nil {
		t.Fatal(e)
	}
	next.Enabled = true
	if e := UpdatePolicy(context.Background(), f.path, 2, next); e != nil {
		t.Fatal(e)
	}
	c.Close()
	waitDone(t, done)
	c, _ = f.connect()
	if _, e := c.Open(context.Background(), transport.Request{HostID: f.policy.HostID, Lease: r, Rows: 24, Cols: 80}); !errors.Is(e, ErrDenied) {
		t.Fatalf("old acquire revision open %v", e)
	}
	_, opened, _ := f.counts()
	if opened != 0 {
		t.Fatal("ABA created native open")
	}
}
func TestJournalDeletionAndLockReplacementStayPoisoned(t *testing.T) {
	for _, action := range []string{"operations", "lock", "corrupt"} {
		t.Run(action, func(t *testing.T) {
			f := newFixture(t)
			s, e := openStore(f.path)
			if e != nil {
				t.Fatal(e)
			}
			defer s.close()
			r := f.request()
			if e = s.access(context.Background(), true, func(a *authority) error {
				a.Operations = append(a.Operations, operation{Request: r, Revision: 1})
				return nil
			}); e != nil {
				t.Fatal(e)
			}
			original, _ := os.ReadFile(f.path)
			switch action {
			case "operations":
				var a authority
				json.Unmarshal(original, &a)
				a.Operations = nil
				b, _ := json.Marshal(a)
				os.WriteFile(f.path, b, 0600)
			case "lock":
				os.Rename(f.path+".lock", f.path+".old-lock")
				os.WriteFile(f.path+".lock", nil, 0600)
			case "corrupt":
				os.WriteFile(f.path, []byte("{}"), 0600)
			}
			effects := 0
			e = s.access(context.Background(), true, func(*authority) error { effects++; return nil })
			if e == nil || effects != 0 {
				t.Fatal("changed evidence accepted")
			}
			if action != "lock" {
				os.WriteFile(f.path, original, 0600)
			}
			if e = s.access(context.Background(), false, func(*authority) error { effects++; return nil }); e == nil || effects != 0 {
				t.Fatal("poison repaired itself")
			}
		})
	}
}
func TestHeldAuthorityLockDeadlineHasNoPartialEffects(t *testing.T) {
	f := newFixture(t)
	s, e := openStore(f.path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	lock, e := openAt(s.dir, s.name+".lock", unix.O_RDWR)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		t.Fatal("test lock")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	before, _ := os.ReadFile(f.path)
	effects := 0
	e = s.access(ctx, true, func(*authority) error { effects++; return nil })
	if !errors.Is(e, context.DeadlineExceeded) || effects != 0 {
		t.Fatalf("held lock %v", e)
	}
	after, _ := os.ReadFile(f.path)
	if !bytes.Equal(before, after) {
		t.Fatal("timeout wrote authority")
	}
}
func TestPolicyCapacityAndIdentityStrings(t *testing.T) {
	f := newFixture(t)
	s, e := openStore(f.path)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < MaxOperations; i++ {
		r := f.request()
		e = s.access(context.Background(), true, func(a *authority) error {
			a.Operations = append(a.Operations, operation{Request: r, Revision: 1})
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	s.close()
	c, _ := f.connect()
	if _, e = c.Attach(context.Background(), f.request()); !errors.Is(e, ErrCapacity) {
		t.Fatalf("capacity %v", e)
	}
	a, o, r := f.counts()
	if a+o+r != 0 {
		t.Fatal("capacity had effect")
	}
	for _, kind := range []string{"attach", "op", "operation", "future-kind"} {
		req := f.request()
		req.OperationID = domain.NewID(kind)
		if validateLease(req, false) != nil {
			t.Fatalf("canonical prefix refused %s", kind)
		}
	}
	bad := f.request()
	bad.ClientID = "viewer\u202e"
	if validateLease(bad, false) == nil {
		t.Fatal("format-control identity accepted")
	}
}
func TestInventoryPolicyDoesNotInventNativeCapabilities(t *testing.T) {
	f := newFixture(t)
	f.session.Mode = domain.Observed
	f.session.Capabilities = domain.Capabilities{}
	c, _ := f.connect()
	v, e := c.List(context.Background())
	if e != nil || len(v) != 1 || v[0].Capabilities.NativeAttach || v[0].Capabilities.ReadOnlyAttach {
		t.Fatalf("invented capabilities %v %v", v, e)
	}
}

func TestVisibleFailedCommitNeedsFreshHeldEvidenceSync(t *testing.T) {
	f := newFixture(t)
	s, e := openStore(f.path)
	if e != nil {
		t.Fatal(e)
	}
	r := f.request()
	lease := domain.Lease{SessionID: r.SessionID, ExecutionID: r.ExecutionID, Generation: r.Generation, OperationID: r.OperationID, ClientID: r.ClientID, Token: string(domain.NewID("lease"))}
	s.syncHook = func(file *os.File) error {
		info, _ := file.Stat()
		if info.IsDir() {
			return ErrStorage
		}
		return file.Sync()
	}
	e = s.access(context.Background(), true, func(a *authority) error {
		a.Operations = append(a.Operations, operation{Request: r, Revision: 1, Lease: &lease})
		return nil
	})
	if !errors.Is(e, ErrStorage) {
		t.Fatal("failed directory sync accepted")
	}
	if e = s.confirm(context.Background()); !errors.Is(e, ErrStorage) {
		t.Fatal("poison erased original failure")
	}
	s.close()
	fresh, e := openStore(f.path)
	if e != nil {
		t.Fatal(e)
	}
	defer fresh.close()
	entered := make(chan struct{})
	release := make(chan struct{})
	fresh.syncHook = func(file *os.File) error {
		info, _ := file.Stat()
		if info.IsDir() {
			close(entered)
			<-release
		}
		return file.Sync()
	}
	done := make(chan error, 1)
	go func() { done <- fresh.confirm(context.Background()) }()
	waitSignal(t, entered)
	select {
	case <-done:
		t.Fatal("success before held directory sync")
	default:
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if e = fresh.access(context.Background(), false, func(a *authority) error {
		if o := findOperation(a, r.OperationID); o == nil || o.Lease == nil || *o.Lease != lease {
			t.Fatal("confirmation changed exact record")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	a, o, rel := f.counts()
	if a+o+rel != 0 {
		t.Fatal("local sync had native effect")
	}
}

func TestAuthoritySyncReplacementRefusesHomeEffect(t *testing.T) {
	for _, confirmation := range []bool{false, true} {
		phase := "intent"
		if confirmation {
			phase = "confirmation"
		}
		for _, replacement := range []string{"lock", "directory", "content", "same_bytes"} {
			t.Run(phase+"_"+replacement, func(t *testing.T) {
				f := newFixture(t)
				store, e := openStore(f.path)
				if e != nil {
					t.Fatal(e)
				}
				defer store.close()
				h, e := newHome(f.policy.Socket)
				if e != nil {
					t.Fatal(e)
				}
				actor := &server{store: store, home: h, policy: f.policy, owned: map[domain.ID]protocol.LeaseRequest{}}
				var once sync.Once
				store.syncHook = func(file *os.File) error {
					if e := file.Sync(); e != nil {
						return e
					}
					info, e := file.Stat()
					if e != nil {
						return e
					}
					if info.IsDir() {
						once.Do(func() {
							switch replacement {
							case "lock":
								if e = os.Rename(f.path+".lock", f.path+".lock-old"); e == nil {
									e = os.WriteFile(f.path+".lock", nil, 0600)
								}
							case "directory":
								moved := f.dir + "-moved"
								t.Cleanup(func() { os.RemoveAll(moved) })
								if e = os.Rename(f.dir, moved); e == nil {
									e = os.Mkdir(f.dir, 0700)
								}
							case "content":
								e = os.WriteFile(f.path, []byte("corrupt-during-sync"), 0600)
							case "same_bytes":
								var data []byte
								if data, e = os.ReadFile(f.path); e == nil {
									if e = os.Rename(f.path, f.path+".old"); e == nil {
										e = os.WriteFile(f.path, data, 0600)
									}
								}
							}
						})
					}
					return e
				}
				if confirmation {
					e = store.confirm(context.Background())
				} else {
					_, e = actor.acquire(context.Background(), f.request())
				}
				if !errors.Is(e, ErrStorage) {
					t.Fatalf("replaced durable authority accepted: %v", e)
				}
				if e = store.access(context.Background(), false, func(*authority) error { return nil }); !errors.Is(e, ErrStorage) {
					t.Fatalf("replacement poison was cleared: %v", e)
				}
				a, o, r := f.counts()
				if a+o+r != 0 {
					t.Fatal("stale persistence admitted a home effect")
				}
			})
		}
	}
}

func TestEquivalentPolicyOrderingDoesNotRewriteRevision(t *testing.T) {
	f := newFixture(t)
	s, e := openStore(f.path)
	if e != nil {
		t.Fatal(e)
	}
	p := f.policy
	p.Sessions = append(p.Sessions, SessionActions{SessionID: domain.NewID("session"), Inventory: true})
	sort.Slice(p.Sessions, func(i, j int) bool { return p.Sessions[i].SessionID > p.Sessions[j].SessionID })
	p.Revision++
	if e = s.access(context.Background(), true, func(a *authority) error { a.Policy = p; return nil }); e != nil {
		t.Fatal(e)
	}
	s.close()
	before, e := os.ReadFile(f.path)
	if e != nil {
		t.Fatal(e)
	}
	next := p
	next.Sessions = append([]SessionActions(nil), p.Sessions...)
	next.Sessions[0], next.Sessions[1] = next.Sessions[1], next.Sessions[0]
	if e = UpdatePolicy(context.Background(), f.path, p.Revision, next); e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(f.path)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("equivalent action set rewrote authority at unchanged revision")
	}
	if next.Sessions[0] != p.Sessions[1] {
		t.Fatal("update mutated caller's action set")
	}
}

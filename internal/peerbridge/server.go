package peerbridge

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/transport"
	"golang.org/x/sys/unix"
)

type serverStream struct {
	id      uint64
	request transport.Request
	stream  *transport.Stream
	cancel  context.CancelFunc
	queue   chan packet
}
type server struct {
	ctx        context.Context
	cancel     context.CancelFunc
	wire       *wire
	store      *authorityStore
	home       *home
	policy     ReadPolicy
	mu         sync.Mutex
	streams    map[uint64]*serverStream
	owned      map[domain.ID]protocol.LeaseRequest
	queueBytes int
	control    chan packet
	wake       chan struct{}
	calls      chan struct{}
	wg         sync.WaitGroup
	lastServed uint64
}

// Serve owns only the supplied pipes and its exact home attachments. Input and
// output must unblock their outstanding reads/writes when Close is called.
func Serve(ctx context.Context, path string, in io.ReadCloser, out io.WriteCloser) error {
	if in == nil || out == nil {
		return ErrInvalid
	}
	w := &wire{in: in, out: out}
	defer w.close()
	store, e := openStore(path)
	if e != nil {
		return e
	}
	defer store.close()
	lease, e := openAt(store.dir, store.name+".serve", unix.O_RDWR)
	if e != nil {
		return e
	}
	defer lease.Close()
	if unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return ErrConflict
	}
	defer unix.Flock(int(lease.Fd()), unix.LOCK_UN)
	confirmCtx, confirmCancel := context.WithTimeout(ctx, ControlTimeout)
	e = store.confirm(confirmCtx)
	confirmCancel()
	if e != nil {
		return e
	}
	h, e := newHome(store.initial.Socket)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &server{ctx: ctx, cancel: cancel, wire: w, store: store, home: h, policy: store.initial, streams: map[uint64]*serverStream{}, owned: map[domain.ID]protocol.LeaseRequest{}, control: make(chan packet, 16), wake: make(chan struct{}, 1), calls: make(chan struct{}, 16)}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); <-ctx.Done(); w.close() }()
	defer func() {
		cancel()
		w.close()
		s.mu.Lock()
		for _, st := range s.streams {
			st.cancel()
		}
		s.mu.Unlock()
		s.wg.Wait()
		s.cleanup()
	}()
	// Hello alone has a startup budget. Quiet reads after admission are legal.
	helloCtx, helloCancel := context.WithTimeout(ctx, ControlTimeout)
	defer helloCancel()
	first := make(chan struct {
		p packet
		e error
	}, 1)
	go func() {
		p, e := w.read(helloCtx)
		first <- struct {
			p packet
			e error
		}{p, e}
	}()
	var p packet
	select {
	case r := <-first:
		p, e = r.p, r.e
	case <-helloCtx.Done():
		w.close()
		<-first
		return ErrUnavailable
	}
	if e != nil || !p.valid(true) || p.kind != tHello {
		return ErrInvalid
	}
	var hello helloRequest
	if strict(p.data, ControlLimit, &hello) != nil || hello.Version != Version || validateSelector(hello.Selector) != nil || hello.Selector != (Selector{s.policy.ScopeID, s.policy.HostID, s.policy.Principal}) {
		return ErrDenied
	}
	if _, e = h.list(helloCtx, s.policy.HostID); e != nil {
		return e
	}
	if e = s.checkPolicy(helloCtx, false, func(a *authority) error { s.policy = a.Policy; return nil }); e != nil {
		return e
	}
	ack, _ := jsonPacket(tHelloAck, p.request, 0, helloResult{Version, s.policy.stamp()})
	if e = w.write(helloCtx, ack); e != nil {
		return e
	}
	helloCancel()
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.writer() }()
	go func() { defer s.wg.Done(); s.watch(lease) }()
	last := p.request
	lastStream := uint64(0)
	for {
		p, e = w.read(ctx)
		if e != nil {
			return e
		}
		if !p.valid(true) || p.kind == tHello {
			return ErrInvalid
		}
		if p.kind == tClose {
			s.closeStream(p.stream)
			continue
		}
		if p.request <= last {
			return ErrInvalid
		}
		last = p.request
		if p.kind == tOpen {
			if p.stream <= lastStream {
				return ErrInvalid
			}
			lastStream = p.stream
		}
		select {
		case s.calls <- struct{}{}:
		default:
			return ErrCapacity
		}
		s.wg.Add(1)
		go func(p packet) { defer s.wg.Done(); defer func() { <-s.calls }(); s.dispatch(p) }(p)
	}
}
func (s *server) checkPolicy(ctx context.Context, write bool, fn func(*authority) error) error {
	return s.store.access(ctx, write, func(a *authority) error {
		if !sameBinding(s.policy, a.Policy) || s.policy.Revision != a.Policy.Revision {
			return ErrDenied
		}
		if e := s.home.check(); e != nil {
			return e
		}
		return fn(a)
	})
}
func (s *server) watch(lease *os.File) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, ControlTimeout)
			e := s.checkPolicy(ctx, false, func(*authority) error {
				if !samePathFile(s.store.dir, s.store.name+".serve", lease) {
					return ErrStorage
				}
				return nil
			})
			cancel()
			if e != nil {
				s.cancel()
				return
			}
		}
	}
}
func (s *server) enqueue(p packet, stream *serverStream) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return ErrUnavailable
	}
	if s.queueBytes+len(p.data) > InventoryLimit {
		return ErrCapacity
	}
	if stream == nil {
		select {
		case s.control <- p:
		default:
			return ErrCapacity
		}
	} else {
		select {
		case stream.queue <- p:
		default:
			return ErrCapacity
		}
	}
	s.queueBytes += len(p.data)
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}
func (s *server) nextPacket() (packet, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p packet
	select {
	case p = <-s.control:
		s.queueBytes -= len(p.data)
		return p, true
	default:
	}
	ids := make([]uint64, 0, len(s.streams))
	for id := range s.streams {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for n := 0; n < 2; n++ {
		for _, id := range ids {
			if n == 0 && id <= s.lastServed || n == 1 && id > s.lastServed {
				continue
			}
			select {
			case p = <-s.streams[id].queue:
				s.queueBytes -= len(p.data)
				s.lastServed = id
				return p, true
			default:
			}
		}
	}
	return p, false
}
func (s *server) writer() {
	for {
		p, ok := s.nextPacket()
		if !ok {
			select {
			case <-s.ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}
		var e error
		if p.kind == tReady || p.kind == tOutput || p.kind == tListed || p.kind == tAcquired {
			e = s.checkPolicy(s.ctx, false, func(a *authority) error {
				if !a.Policy.Enabled {
					return ErrDenied
				}
				return s.wire.write(s.ctx, p)
			})
		} else {
			e = s.wire.write(s.ctx, p)
		}
		if e != nil {
			s.cancel()
			return
		}
		if p.kind == tClose || p.kind == tFailure {
			s.closeStream(p.stream)
		}
	}
}
func (s *server) reply(p packet, t tag, v any) {
	q, e := jsonPacket(t, p.request, p.stream, v)
	if e == nil {
		e = s.enqueue(q, nil)
	}
	if e != nil {
		s.cancel()
	}
}
func (s *server) fail(p packet, e error) {
	q, _ := jsonPacket(tError, p.request, p.stream, failure{errorCode(e)})
	if s.enqueue(q, nil) != nil {
		s.cancel()
	}
}
func (s *server) dispatch(p packet) {
	ctx, cancel := context.WithTimeout(s.ctx, ControlTimeout)
	defer cancel()
	switch p.kind {
	case tList:
		var r revisionRequest
		if strict(p.data, ControlLimit, &r) != nil || r.Revision != s.policy.Revision {
			s.fail(p, ErrInvalid)
			return
		}
		var policy ReadPolicy
		e := s.checkPolicy(ctx, false, func(a *authority) error {
			if !a.Policy.Enabled {
				return ErrDenied
			}
			policy = a.Policy
			return nil
		})
		if e != nil {
			s.fail(p, e)
			return
		}
		v, e := s.home.list(ctx, policy.HostID)
		if e != nil {
			s.fail(p, e)
			return
		}
		out := []domain.Session{}
		for _, v := range v {
			if allowed(policy, v.ID, false) {
				attachable := v.Mode == domain.Managed && v.Execution.State == "running" && v.Capabilities.NativeAttach && v.Capabilities.ReadOnlyAttach
				v.Execution.Runtime = domain.RuntimeRef{}
				v.Capabilities = domain.Capabilities{Explanation: "Home-authorized observer route; exact current acquisition and Ready are required."}
				if allowed(policy, v.ID, true) && attachable {
					v.Capabilities.NativeAttach = true
					v.Capabilities.ReadOnlyAttach = true
				}
				out = append(out, v)
			}
		}
		e = s.checkPolicy(ctx, false, func(*authority) error {
			q, err := jsonPacket(tListed, p.request, 0, out)
			if err != nil {
				return err
			}
			return s.enqueue(q, nil)
		})
		if e != nil {
			s.fail(p, e)
		}
	case tAcquire:
		var r acquireRequest
		if strict(p.data, ControlLimit, &r) != nil || r.Revision != s.policy.Revision || validateLease(r.Lease, false) != nil {
			s.fail(p, ErrInvalid)
			return
		}
		v, e := s.acquire(ctx, r.Lease)
		if e != nil {
			s.fail(p, e)
			return
		}
		s.reply(p, tAcquired, acquiredResult{v.Lease, true})
	case tOpen:
		var r openRequest
		if strict(p.data, ControlLimit, &r) != nil || r.Revision != s.policy.Revision || r.Request.HostID != s.policy.HostID || validateRequest(r.Request) != nil {
			s.fail(p, ErrInvalid)
			return
		}
		if e := s.open(p, r.Request); e != nil {
			s.fail(p, e)
		}
	case tRelease:
		var r releaseRequest
		if strict(p.data, ControlLimit, &r) != nil || validateLease(r.Lease, true) != nil {
			s.fail(p, ErrInvalid)
			return
		}
		if e := s.release(ctx, r.Lease); e != nil {
			s.fail(p, e)
			return
		}
		s.reply(p, tReleased, struct{}{})
	default:
		s.fail(p, ErrInvalid)
	}
}
func findOperation(a *authority, id domain.ID) *operation {
	for i := range a.Operations {
		if a.Operations[i].Request.OperationID == id {
			return &a.Operations[i]
		}
	}
	return nil
}
func (s *server) acquire(ctx context.Context, r protocol.LeaseRequest) (domain.AttachSpec, error) {
	var known *domain.Lease
	e := s.checkPolicy(ctx, true, func(a *authority) error {
		if o := findOperation(a, r.OperationID); o != nil {
			if o.Request != r {
				return ErrConflict
			}
			if o.Lease == nil {
				return ErrUncertain
			}
			if o.Released {
				return ErrConflict
			}
			l := *o.Lease
			known = &l
			return nil
		}
		if !allowed(a.Policy, r.SessionID, true) {
			return ErrDenied
		}
		if len(a.Operations) >= MaxOperations {
			return ErrCapacity
		}
		a.Operations = append(a.Operations, operation{Request: r, Revision: a.Policy.Revision})
		return nil
	})
	if e != nil {
		return domain.AttachSpec{}, e
	}
	if known != nil {
		return domain.AttachSpec{Lease: *known, ReadOnly: true}, nil
	}
	v, e := s.home.attach(ctx, r)
	if e != nil {
		return domain.AttachSpec{}, ErrUncertain
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), ControlTimeout)
	defer cancel()
	e = s.store.access(saveCtx, true, func(a *authority) error {
		o := findOperation(a, r.OperationID)
		if o == nil || o.Request != r || o.Lease != nil {
			return ErrConflict
		}
		l := v.Lease
		o.Lease = &l
		return nil
	})
	if e != nil {
		return domain.AttachSpec{}, e
	}
	s.mu.Lock()
	s.owned[r.OperationID] = protocol.LeaseRequest{SessionID: r.SessionID, ExecutionID: r.ExecutionID, Generation: r.Generation, OperationID: r.OperationID, ClientID: r.ClientID, ReadOnly: true, Token: v.Lease.Token}
	s.mu.Unlock()
	if e = s.checkPolicy(ctx, false, func(a *authority) error {
		if !allowed(a.Policy, r.SessionID, true) {
			return ErrDenied
		}
		return nil
	}); e != nil {
		return domain.AttachSpec{}, e
	}
	return v, nil
}
func (s *server) open(p packet, r transport.Request) error {
	s.mu.Lock()
	if len(s.streams) >= 8 {
		s.mu.Unlock()
		return ErrCapacity
	}
	live, cancel := context.WithCancel(s.ctx)
	st := &serverStream{id: p.stream, request: r, cancel: cancel, queue: make(chan packet, 4)}
	s.streams[p.stream] = st
	s.mu.Unlock()
	ok := false
	defer func() {
		if !ok {
			s.closeStream(p.stream)
		}
	}()
	ctx, cc := context.WithTimeout(s.ctx, ControlTimeout)
	defer cc()
	e := s.checkPolicy(ctx, true, func(a *authority) error {
		if !allowed(a.Policy, r.Lease.SessionID, true) {
			return ErrDenied
		}
		o := findOperation(a, r.Lease.OperationID)
		if o != nil && o.Revision != a.Policy.Revision {
			return ErrDenied
		}
		if o == nil || o.Request != r.Lease || o.Lease == nil || o.Released {
			if o != nil && o.OpenAttempted {
				return ErrUncertain
			}
			return ErrConflict
		}
		if o.OpenAttempted {
			return ErrUncertain
		}
		o.OpenAttempted = true
		return nil
	})
	if e != nil {
		return e
	}
	stream, e := s.home.open(live, r)
	if e != nil {
		return ErrUncertain
	}
	s.mu.Lock()
	st.stream = stream
	s.mu.Unlock()
	e = s.checkPolicy(ctx, true, func(a *authority) error {
		o := findOperation(a, r.Lease.OperationID)
		if o == nil {
			return ErrStorage
		}
		o.ReadyObserved = true
		return nil
	})
	if e != nil {
		stream.Close()
		return e
	}
	data, _ := json.Marshal(stream.Receipt)
	e = s.checkPolicy(ctx, false, func(*authority) error { return s.enqueue(packet{tReady, p.request, p.stream, data}, nil) })
	if e != nil {
		stream.Close()
		return e
	}
	ok = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			f, e := stream.ReadFrame()
			kind := tOutput
			if e != nil {
				kind = tClose
				f.Data = nil
			} else if f.Kind == transport.Close {
				kind = tClose
			} else if f.Kind == transport.Failure {
				kind = tFailure
				f.Data = []byte("terminal unavailable")
			} else if f.Kind != transport.Output {
				s.closeStream(p.stream)
				return
			}
			e = s.checkPolicy(live, false, func(*authority) error { return s.enqueue(packet{kind, 0, p.stream, f.Data}, st) })
			if e != nil {
				s.closeStream(p.stream)
				_ = s.enqueue(packet{kind: tFailure, stream: p.stream, data: []byte("observer channel unavailable")}, nil)
				return
			}
			if kind == tClose || kind == tFailure {
				return
			}
		}
	}()
	return nil
}
func (s *server) closeStream(id uint64) {
	s.mu.Lock()
	st := s.streams[id]
	if st != nil {
		delete(s.streams, id)
		for {
			select {
			case p := <-st.queue:
				s.queueBytes -= len(p.data)
			default:
				goto drained
			}
		}
	}
drained:
	s.mu.Unlock()
	if st != nil {
		st.cancel()
		if st.stream != nil {
			st.stream.Close()
		}
	}
}
func (s *server) release(ctx context.Context, r protocol.LeaseRequest) error {
	var released bool
	e := s.store.access(ctx, true, func(a *authority) error {
		if !sameBinding(s.policy, a.Policy) {
			return ErrConflict
		}
		o := findOperation(a, r.OperationID)
		if o == nil || o.Request != tokenFree(r) || o.Lease == nil || o.Lease.Token != r.Token {
			return ErrDenied
		}
		released = o.Released
		if !released {
			o.ReleaseAttempted = true
		}
		return nil
	})
	if e != nil || released {
		return e
	}
	s.mu.Lock()
	ids := []uint64{}
	for id, st := range s.streams {
		if st.request.Lease.OperationID == r.OperationID {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.closeStream(id)
	}
	if e = s.home.release(ctx, r); e != nil {
		return ErrUncertain
	}
	return s.store.access(ctx, true, func(a *authority) error {
		o := findOperation(a, r.OperationID)
		if o == nil || o.Lease == nil || o.Lease.Token != r.Token {
			return ErrStorage
		}
		o.Released = true
		return nil
	})
}
func (s *server) cleanup() {
	s.mu.Lock()
	requests := make([]protocol.LeaseRequest, 0, len(s.owned))
	for _, r := range s.owned {
		requests = append(requests, r)
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), ControlTimeout)
	defer cancel()
	for _, r := range requests {
		_ = s.release(ctx, r)
	}
}

package peerbridge

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/transport"
)

type waiting struct {
	kind   tag
	stream uint64
	reply  chan packet
}
type clientStream struct {
	id    uint64
	pipe  net.Conn
	queue chan packet
	done  chan struct{}
	once  sync.Once
}
type Client struct {
	mu               sync.Mutex
	sendMu           sync.Mutex
	helloMu          sync.Mutex
	wire             *wire
	selector         Selector
	stamp            PolicyStamp
	started, closed  bool
	ctx              context.Context
	cancel           context.CancelFunc
	next, nextStream uint64
	queueBytes       int
	pending          map[uint64]waiting
	streams          map[uint64]*clientStream
	retired          map[uint64]bool
	wg               sync.WaitGroup
}

func NewClient(conn io.ReadWriteCloser, selector Selector) (*Client, error) {
	if conn == nil || validateSelector(selector) != nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{wire: &wire{in: conn, out: conn}, selector: selector, ctx: ctx, cancel: cancel, pending: map[uint64]waiting{}, streams: map[uint64]*clientStream{}, retired: map[uint64]bool{}}, nil
}
func (c *Client) abort() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.cancel()
	c.wire.close()
	for _, s := range c.streams {
		s.pipe.Close()
	}
	c.mu.Unlock()
}
func (c *Client) Close() error { c.abort(); c.wg.Wait(); return nil }
func (c *Client) Hello(ctx context.Context) (PolicyStamp, error) {
	c.helloMu.Lock()
	defer c.helloMu.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return PolicyStamp{}, ErrUnavailable
	}
	if c.stamp.Revision != 0 {
		s := c.stamp
		c.mu.Unlock()
		return s, nil
	}
	if !c.started {
		c.started = true
		c.wg.Add(1)
		go func() { defer c.wg.Done(); c.reader() }()
	}
	c.mu.Unlock()
	p, e := c.call(ctx, tHello, tHelloAck, 0, helloRequest{Version, c.selector})
	if e != nil {
		return PolicyStamp{}, e
	}
	var v helloResult
	if strict(p.data, ControlLimit, &v) != nil || v.Version != Version || v.Policy.Revision == 0 || v.Policy.ScopeID != c.selector.ScopeID || v.Policy.HostID != c.selector.HostID || v.Policy.Principal != c.selector.Principal {
		c.abort()
		return PolicyStamp{}, ErrConflict
	}
	c.mu.Lock()
	c.stamp = v.Policy
	c.mu.Unlock()
	return v.Policy, nil
}
func (c *Client) revision() (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.stamp.Revision == 0 {
		return 0, ErrUnavailable
	}
	return c.stamp.Revision, nil
}
func (c *Client) call(ctx context.Context, kind, want tag, stream uint64, v any) (packet, error) {
	return c.callPrepared(ctx, kind, want, stream, v, nil)
}
func (c *Client) callPrepared(ctx context.Context, kind, want tag, stream uint64, v any, prepare func(uint64) (uint64, error)) (packet, error) {
	ctx, cancel := context.WithTimeout(ctx, ControlTimeout)
	defer cancel()
	c.sendMu.Lock()
	if ctx.Err() != nil {
		c.sendMu.Unlock()
		return packet{}, ctx.Err()
	}
	c.mu.Lock()
	if c.closed || len(c.pending) >= 16 || c.next == ^uint64(0) {
		c.mu.Unlock()
		c.sendMu.Unlock()
		return packet{}, ErrCapacity
	}
	c.next++
	id := c.next
	if prepare != nil {
		var e error
		stream, e = prepare(id)
		if e != nil {
			c.mu.Unlock()
			c.sendMu.Unlock()
			return packet{}, e
		}
	}
	wait := waiting{want, stream, make(chan packet, 1)}
	c.pending[id] = wait
	c.mu.Unlock()
	p, e := jsonPacket(kind, id, stream, v)
	if e == nil {
		e = c.wire.write(ctx, p)
	}
	c.sendMu.Unlock()
	if e != nil {
		c.abort()
		return packet{}, ErrUncertain
	}
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	select {
	case p := <-wait.reply:
		c.mu.Lock()
		c.queueBytes -= len(p.data)
		c.mu.Unlock()
		if p.kind == tError {
			var f failure
			if strict(p.data, 512, &f) != nil {
				c.abort()
				return p, ErrInvalid
			}
			return p, codeError(f.Code)
		}
		return p, nil
	case <-ctx.Done():
		c.abort()
		return packet{}, ErrUncertain
	case <-c.ctx.Done():
		return packet{}, ErrUnavailable
	}
}
func (c *Client) reader() {
	defer c.abort()
	for {
		p, e := c.wire.read(c.ctx)
		if e != nil || !p.valid(false) {
			return
		}
		c.mu.Lock()
		if p.request != 0 {
			if c.queueBytes+len(p.data) > InventoryLimit {
				c.mu.Unlock()
				return
			}
			wait, ok := c.pending[p.request]
			if !ok || wait.stream != p.stream || p.kind != wait.kind && p.kind != tError {
				c.mu.Unlock()
				return
			}
			delete(c.pending, p.request)
			if p.kind == tReady {
				st := c.streams[p.stream]
				if st == nil {
					c.mu.Unlock()
					return
				}
				select {
				case st.queue <- p:
					c.queueBytes += len(p.data)
				default:
					c.mu.Unlock()
					return
				}
				p.data = nil
			}
			c.queueBytes += len(p.data)
			wait.reply <- p
			c.mu.Unlock()
			continue
		}
		st := c.streams[p.stream]
		if st == nil {
			retired := c.retired[p.stream]
			c.mu.Unlock()
			if !retired {
				return
			}
			continue
		}
		if c.queueBytes+len(p.data) > InventoryLimit {
			c.mu.Unlock()
			c.closeStream(p.stream, true)
			continue
		}
		select {
		case st.queue <- p:
			c.queueBytes += len(p.data)
			c.mu.Unlock()
		default:
			c.mu.Unlock()
			c.closeStream(p.stream, true)
		}
	}
}
func (c *Client) List(ctx context.Context) ([]domain.Session, error) {
	r, e := c.revision()
	if e != nil {
		return nil, e
	}
	p, e := c.call(ctx, tList, tListed, 0, revisionRequest{r})
	if e != nil {
		return nil, e
	}
	var sessions []domain.Session
	if strict(p.data, InventoryLimit, &sessions) != nil || len(sessions) > MaxSessions {
		c.abort()
		return nil, ErrInvalid
	}
	seen := map[domain.ID]bool{}
	for _, s := range sessions {
		if s.HostID != c.selector.HostID || !domain.ValidObjectID(s.ID, "session") || seen[s.ID] || s.Execution.Runtime != (domain.RuntimeRef{}) || s.Capabilities.Stop || s.Capabilities.SemanticPrompt || s.Capabilities.NativeResume || s.Capabilities.NativeAttach != s.Capabilities.ReadOnlyAttach {
			c.abort()
			return nil, ErrInvalid
		}
		seen[s.ID] = true
	}
	return sessions, nil
}
func (c *Client) Attach(ctx context.Context, r protocol.LeaseRequest) (domain.AttachSpec, error) {
	if validateLease(r, false) != nil {
		return domain.AttachSpec{}, ErrInvalid
	}
	rev, e := c.revision()
	if e != nil {
		return domain.AttachSpec{}, e
	}
	p, e := c.call(ctx, tAcquire, tAcquired, 0, acquireRequest{rev, r})
	if e != nil {
		return domain.AttachSpec{}, e
	}
	var v acquiredResult
	if strict(p.data, ControlLimit, &v) != nil || !v.ReadOnly || !validText(v.Lease.Token, 256) || v.Lease != (domain.Lease{SessionID: r.SessionID, ExecutionID: r.ExecutionID, Generation: r.Generation, OperationID: r.OperationID, ClientID: r.ClientID, Token: v.Lease.Token}) {
		c.abort()
		return domain.AttachSpec{}, ErrConflict
	}
	return domain.AttachSpec{Lease: v.Lease, ReadOnly: true}, nil
}
func (c *Client) Release(ctx context.Context, r protocol.LeaseRequest) error {
	if validateLease(r, true) != nil {
		return ErrInvalid
	}
	if _, e := c.revision(); e != nil {
		return e
	}
	p, e := c.call(ctx, tRelease, tReleased, 0, releaseRequest{r})
	if e != nil {
		return e
	}
	var v struct{}
	if strict(p.data, ControlLimit, &v) != nil {
		return ErrInvalid
	}
	return nil
}
func (c *Client) Open(ctx context.Context, r transport.Request) (*transport.Stream, error) {
	if r.HostID != c.selector.HostID || validateRequest(r) != nil {
		return nil, ErrInvalid
	}
	rev, e := c.revision()
	if e != nil {
		return nil, e
	}
	local, remote := net.Pipe()
	var id uint64
	bounded := &handshakeConn{Conn: local, until: time.Now().Add(ControlTimeout)}
	type admission struct {
		s *transport.Stream
		e error
	}
	admitted := make(chan admission, 1)
	go func() { s, e := transport.ReadOnlyStream(ctx, bounded, r); admitted <- admission{s, e} }()
	if _, e = c.callPrepared(ctx, tOpen, tReady, 0, openRequest{rev, r}, func(dispatchID uint64) (uint64, error) {
		if len(c.streams) >= 8 || c.nextStream >= MaxOperations {
			return 0, ErrCapacity
		}
		c.nextStream++
		id = dispatchID
		st := &clientStream{id: id, pipe: remote, queue: make(chan packet, 4), done: make(chan struct{})}
		c.streams[id] = st
		c.wg.Add(2)
		go func() { defer c.wg.Done(); c.streamOutput(st) }()
		go func() { defer c.wg.Done(); c.streamClose(st) }()
		return id, nil
	}); e != nil {
		local.Close()
		remote.Close()
		c.closeStream(id, true)
		<-admitted
		return nil, e
	}
	result := <-admitted
	if result.e != nil {
		c.closeStream(id, true)
		return nil, result.e
	}
	bounded.unbound()
	return result.s, nil
}
func (c *Client) streamOutput(st *clientStream) {
	defer c.closeStream(st.id, false)
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-st.done:
			return
		case p := <-st.queue:
			c.mu.Lock()
			c.queueBytes -= len(p.data)
			c.mu.Unlock()
			kind := transport.Output
			switch p.kind {
			case tReady:
				kind = transport.Ready
			case tFailure:
				kind = transport.Failure
			case tClose:
				kind = transport.Close
			case tOutput:
			default:
				return
			}
			b := make([]byte, 5+len(p.data))
			b[0] = byte(kind)
			binary.BigEndian.PutUint32(b[1:], uint32(len(p.data)))
			copy(b[5:], p.data)
			st.pipe.SetWriteDeadline(time.Now().Add(IOTimeout))
			if writeBytes(st.pipe, b) != nil {
				return
			}
			if kind == transport.Close || kind == transport.Failure {
				return
			}
		}
	}
}
func (c *Client) streamClose(st *clientStream) {
	var h [5]byte
	_, e := io.ReadFull(st.pipe, h[:])
	if e == nil && (h[0] != byte(transport.Close) || binary.BigEndian.Uint32(h[1:]) != 0) {
		c.abort()
		return
	}
	c.closeStream(st.id, true)
}
func (c *Client) closeStream(id uint64, notify bool) {
	c.mu.Lock()
	st := c.streams[id]
	if st != nil {
		delete(c.streams, id)
		c.retired[id] = true
		for {
			select {
			case p := <-st.queue:
				c.queueBytes -= len(p.data)
			default:
				goto drained
			}
		}
	}
drained:
	c.mu.Unlock()
	if st == nil {
		return
	}
	st.once.Do(func() { close(st.done); st.pipe.Close() })
	if notify {
		ctx, cancel := context.WithTimeout(c.ctx, ControlTimeout)
		defer cancel()
		if c.wire.write(ctx, packet{kind: tClose, stream: id}) != nil {
			c.abort()
		}
	}
}
func writeBytes(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, e := w.Write(b)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

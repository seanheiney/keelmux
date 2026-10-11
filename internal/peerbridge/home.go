package peerbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/transport"
)

type socketPin struct{ socket, parent os.FileInfo }

func inspectSocket(path string) (socketPin, error) {
	var p socketPin
	if !cleanPath(path, 100) {
		return p, ErrInvalid
	}
	canonical, e := filepath.EvalSymlinks(path)
	if e != nil || canonical != path {
		return p, ErrUnavailable
	}
	p.parent, e = os.Lstat(filepath.Dir(path))
	if e != nil || !privateInfo(p.parent, true) {
		return socketPin{}, ErrUnavailable
	}
	p.socket, e = os.Lstat(path)
	if e != nil || !privateInfo(p.socket, false) {
		return socketPin{}, ErrUnavailable
	}
	return p, nil
}
func privateInfo(i os.FileInfo, dir bool) bool {
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Geteuid()) {
		return false
	}
	if dir {
		return i.IsDir() && i.Mode().Perm() == 0700
	}
	return i.Mode()&os.ModeSocket != 0 && i.Mode().Perm() == 0600
}

type home struct {
	socket string
	pin    socketPin
}

func newHome(socket string) (*home, error) {
	p, e := inspectSocket(socket)
	if e != nil {
		return nil, e
	}
	return &home{socket, p}, nil
}
func (h *home) check() error {
	p, e := inspectSocket(h.socket)
	if e != nil || !os.SameFile(h.pin.socket, p.socket) || !os.SameFile(h.pin.parent, p.parent) {
		return ErrUnavailable
	}
	return nil
}
func (h *home) request(ctx context.Context, path string, in any, out any, limit int) error {
	if e := h.check(); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, ControlTimeout)
	defer cancel()
	var body io.Reader
	method := http.MethodGet
	if in != nil {
		b, e := json.Marshal(in)
		if e != nil || len(b) > ControlLimit {
			return ErrInvalid
		}
		body = bytes.NewReader(b)
		method = http.MethodPost
	}
	t := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if e := h.check(); e != nil {
			return nil, e
		}
		return (&net.Dialer{}).DialContext(ctx, "unix", h.socket)
	}}
	defer t.CloseIdleConnections()
	c := &http.Client{Transport: t, Timeout: ControlTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	q, e := http.NewRequestWithContext(ctx, method, "http://home"+path, body)
	if e != nil {
		return ErrInvalid
	}
	q.Header.Set("X-Backplane-Protocol", protocol.Version)
	q.Header.Set("Content-Type", "application/json")
	r, e := c.Do(q)
	if e != nil {
		return ErrUnavailable
	}
	defer r.Body.Close()
	if r.StatusCode != 200 && r.StatusCode != 204 {
		if r.StatusCode == 400 {
			return ErrInvalid
		}
		if r.StatusCode == 403 {
			return ErrDenied
		}
		if r.StatusCode == 409 {
			return ErrConflict
		}
		return ErrUnavailable
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, int64(limit+1)))
	if e != nil || len(b) > limit {
		return ErrInvalid
	}
	if out == nil {
		if len(b) != 0 {
			return ErrInvalid
		}
	} else if strict(b, limit, out) != nil {
		return ErrInvalid
	}
	return h.check()
}

type inventory struct {
	Version    string           `json:"version"`
	HostID     domain.ID        `json:"host_id"`
	CapturedAt time.Time        `json:"captured_at"`
	Sessions   []domain.Session `json:"sessions"`
}

func (h *home) list(ctx context.Context, host domain.ID) ([]domain.Session, error) {
	var v inventory
	if e := h.request(ctx, "/v1/peer-inventory", nil, &v, InventoryLimit); e != nil {
		return nil, e
	}
	if v.Version != "keelmux-peer/1" || v.HostID != host || v.CapturedAt.IsZero() || len(v.Sessions) > MaxSessions {
		return nil, ErrConflict
	}
	seen := map[domain.ID]bool{}
	for _, s := range v.Sessions {
		if s.HostID != host || !domain.ValidObjectID(s.ID, "session") || !domain.ValidObjectID(s.WorkID, "work") || !domain.ValidObjectID(s.ThreadID, "thread") || !domain.ValidObjectID(s.LaneID, "lane") || !domain.ValidObjectID(s.Execution.ID, "execution") || s.Execution.Generation == 0 || seen[s.ID] || !cleanPath(s.Workspace, 4096) || !validText(s.Name, 128) {
			return nil, ErrInvalid
		}
		seen[s.ID] = true
	}
	// The identity endpoint intentionally strips capabilities. Fetch actual
	// current home capability declarations separately, and join exact lifetimes.
	var actual protocol.Inventory
	if e := h.request(ctx, "/v1/sessions", nil, &actual, InventoryLimit); e != nil {
		return nil, e
	}
	if actual.Version != protocol.Version || len(actual.Sessions) > MaxSessions {
		return nil, ErrInvalid
	}
	byID := map[domain.ID]domain.Session{}
	for _, s := range actual.Sessions {
		if s.HostID != host || byID[s.ID].ID != "" {
			return nil, ErrConflict
		}
		byID[s.ID] = s
	}
	for i, s := range v.Sessions {
		current, ok := byID[s.ID]
		if !ok || current.WorkID != s.WorkID || current.ThreadID != s.ThreadID || current.LaneID != s.LaneID || current.Execution.ID != s.Execution.ID || current.Execution.Generation != s.Execution.Generation {
			return nil, ErrConflict
		}
		v.Sessions[i].Capabilities = current.Capabilities
		v.Sessions[i].Mode = current.Mode
		v.Sessions[i].Execution.State = current.Execution.State
	}
	return v.Sessions, nil
}
func (h *home) attach(ctx context.Context, r protocol.LeaseRequest) (domain.AttachSpec, error) {
	var v domain.AttachSpec
	if e := h.request(ctx, "/v1/attach", r, &v, ControlLimit); e != nil {
		return v, e
	}
	want := domain.Lease{SessionID: r.SessionID, ExecutionID: r.ExecutionID, Generation: r.Generation, OperationID: r.OperationID, ClientID: r.ClientID, Token: v.Lease.Token}
	if !validText(v.Lease.Token, 256) || v.Lease != want || !v.ReadOnly {
		return domain.AttachSpec{}, ErrConflict
	}
	v.Argv = nil
	return v, nil
}
func (h *home) release(ctx context.Context, r protocol.LeaseRequest) error {
	return h.request(ctx, "/v1/release", r, nil, ControlLimit)
}

// This wrapper limits the handshake deadline without letting the control
// timeout become the successfully opened stream's lifetime context.
type handshakeConn struct {
	net.Conn
	mu     sync.Mutex
	until  time.Time
	reader *bufio.Reader
}

func (c *handshakeConn) limit(t time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.until.IsZero() && (t.IsZero() || t.After(c.until)) {
		return c.until
	}
	return t
}
func (c *handshakeConn) SetDeadline(t time.Time) error     { return c.Conn.SetDeadline(c.limit(t)) }
func (c *handshakeConn) SetReadDeadline(t time.Time) error { return c.Conn.SetReadDeadline(c.limit(t)) }
func (c *handshakeConn) SetWriteDeadline(t time.Time) error {
	return c.Conn.SetWriteDeadline(c.limit(t))
}
func (c *handshakeConn) Read(p []byte) (int, error) {
	if c.reader != nil {
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}
func (c *handshakeConn) unbound() {
	c.mu.Lock()
	c.until = time.Time{}
	c.mu.Unlock()
	_ = c.Conn.SetDeadline(time.Time{})
}
func (h *home) open(ctx context.Context, r transport.Request) (*transport.Stream, error) {
	if e := h.check(); e != nil {
		return nil, e
	}
	dialCtx, cancel := context.WithTimeout(ctx, ControlTimeout)
	conn, e := (&net.Dialer{}).DialContext(dialCtx, "unix", h.socket)
	cancel()
	if e != nil {
		return nil, ErrUnavailable
	}
	c := &handshakeConn{Conn: conn, until: time.Now().Add(ControlTimeout)}
	fail := func(e error) (*transport.Stream, error) { c.Close(); return nil, e }
	if c.SetDeadline(c.until) != nil {
		return fail(ErrUnavailable)
	}
	b, e := json.Marshal(r)
	if e != nil || len(b) > ControlLimit {
		return fail(ErrInvalid)
	}
	q, _ := http.NewRequest("POST", "http://home/v1/terminal", bytes.NewReader(b))
	q.Header.Set("X-Backplane-Protocol", protocol.Version)
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("Connection", "Upgrade")
	q.Header.Set("Upgrade", transport.Upgrade)
	if q.Write(c) != nil {
		return fail(ErrUnavailable)
	}
	br := bufio.NewReader(conn)
	res, e := http.ReadResponse(br, q)
	if e != nil {
		return fail(ErrUnavailable)
	}
	if res.StatusCode != 101 || res.Header.Get("Upgrade") != transport.Upgrade {
		return fail(ErrUnavailable)
	}
	c.reader = br
	s, e := transport.ReadOnlyStream(ctx, c, r)
	if e != nil {
		return nil, e
	}
	c.unbound()
	if e = h.check(); e != nil {
		s.Close()
		return nil, e
	}
	return s, nil
}

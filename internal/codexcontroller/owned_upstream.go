package codexcontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/coder/websocket"
	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"github.com/seanheiney/keelmux/internal/domain"
)

// OwnedCapabilities describes evidence separately from requested functionality.
// Resume is deliberately disabled until native automatic-continuation behavior
// and exact-thread lifecycle are qualified independently of a zero-turn probe.
type OwnedCapabilities struct {
	AuthenticatedTransport, RunningImageProof, ExactThreadInspection, Resume bool
	Reason                                                                   string
}

// OwnedUpstream implements authenticated native inspection. It cannot currently
// enable a primary launcher: CheckPrimaryReady and Resume both fail closed.
// No model, account, configuration, tool or approval RPC is implemented here.
type OwnedUpstream struct {
	mu      sync.Mutex
	anchors map[domain.ID]*ownedAnchor
}
type ownedAnchor struct {
	conn      *websocket.Conn
	transport *http.Transport
	launch    Launch
	ref       domain.RuntimeRef
	identity  string
	next      uint64
}

func NewOwnedUpstream() *OwnedUpstream {
	return &OwnedUpstream{anchors: make(map[domain.ID]*ownedAnchor)}
}
func (u *OwnedUpstream) String() string               { return "[owned Codex inspection adapter]" }
func (u *OwnedUpstream) GoString() string             { return u.String() }
func (u *OwnedUpstream) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }
func (u *OwnedUpstream) Capabilities() OwnedCapabilities {
	return OwnedCapabilities{RunningImageProof: ownedImageSupported(), Reason: "Pinned Codex Unix token enforcement and native resume are unqualified. The installed 0.159.0 Unix listener accepted a wrong token; primary launch and exact-thread evidence remain unavailable. Darwin running-image digest proof is unavailable."}
}
func (u *OwnedUpstream) CheckPrimaryReady() error { return ErrUnavailable }
func (u *OwnedUpstream) Resume(context.Context, Launch, domain.RuntimeRef) (Evidence, error) {
	return Evidence{}, ErrUnavailable
}

// Probe authenticates an exact owned process on the same Unix connection and
// verifies its declared native home. It is a transport capability probe only;
// it never returns primary or thread evidence and never resumes a thread.
func (u *OwnedUpstream) Probe(ctx context.Context, l Launch, ref domain.RuntimeRef) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	a, err := u.connect(ctx, l, ref)
	if err != nil {
		return err
	}
	a.close()
	return nil
}
func (u *OwnedUpstream) connect(ctx context.Context, l Launch, ref domain.RuntimeRef) (*ownedAnchor, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, ErrInvalid
	}
	if !cleanPath(l.Endpoint) || !cleanPath(l.CodexHome) || len(l.Capability()) != 64 || ref.PID <= 0 || ref.ExecutionID != l.Request.Target.ExecutionID || ref.OperationID != l.Request.OperationID {
		return nil, ErrInvalid
	}
	// A server accepting a correct token has not proved authentication. Require
	// that this same owned socket/process rejects an unrelated token at upgrade.
	_, expectedIdentity, err := ownedSocket(l.Endpoint)
	if err != nil {
		return nil, ErrUnavailable
	}
	wrong, err := nonce(32)
	if err != nil || wrong == l.Capability() {
		return nil, ErrUnavailable
	}
	denied, status, err := dialOwned(ctx, l, ref, wrong)
	if denied != nil {
		denied.close()
	}
	if err == nil || (status != http.StatusUnauthorized && status != http.StatusForbidden) {
		return nil, ErrUnavailable
	}
	a, _, err := dialOwned(ctx, l, ref, l.Capability())
	if err != nil {
		return nil, err
	}
	if a.identity != expectedIdentity {
		a.close()
		return nil, ErrConflict
	}
	var init struct {
		CodexHome string `json:"codexHome"`
		UserAgent string `json:"userAgent"`
	}
	if err = a.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "keelmux_owned_inspector", "title": "Keelmux owned inspection", "version": "0.1.0"}, "capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false, "explicitGatewayOauth": true}}, &init); err != nil {
		a.close()
		return nil, err
	}
	canonical, e := filepath.EvalSymlinks(init.CodexHome)
	if e != nil || canonical != l.CodexHome {
		a.close()
		return nil, ErrConflict
	}
	if err = a.write(ctx, map[string]any{"method": "initialized"}); err != nil {
		a.close()
		return nil, err
	}
	return a, nil
}

// dialOwned verifies the connected peer before the HTTP upgrade and pins the
// endpoint inode across it. Upgrade errors never export native diagnostics.
func dialOwned(ctx context.Context, l Launch, ref domain.RuntimeRef, token string) (*ownedAnchor, int, error) {
	path, identity, err := ownedSocket(l.Endpoint)
	if err != nil {
		return nil, 0, ErrUnavailable
	}
	var mu sync.Mutex
	var raw net.Conn
	canceled := false
	closeRaw := func() {
		mu.Lock()
		canceled = true
		c := raw
		mu.Unlock()
		if c != nil {
			_ = c.Close()
		}
	}
	stop := context.AfterFunc(ctx, closeRaw)
	defer stop()
	transport := &http.Transport{DisableKeepAlives: true}
	transport.DialContext = func(inner context.Context, _, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(inner, "unix", path)
		if err != nil {
			return nil, ErrUnavailable
		}
		mu.Lock()
		if canceled || raw != nil {
			mu.Unlock()
			conn.Close()
			return nil, ErrUnavailable
		}
		raw = conn
		mu.Unlock()
		unixConn, ok := conn.(*net.UnixConn)
		if !ok || verifyOwnedPeer(unixConn, ref) != nil {
			conn.Close()
			return nil, ErrConflict
		}
		return conn, nil
	}
	conn, response, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}}, CompressionMode: websocket.CompressionDisabled})
	status := 0
	if response != nil {
		status = response.StatusCode
	}
	if err != nil {
		closeRaw()
		transport.CloseIdleConnections()
		return nil, status, ErrUnavailable
	}
	stop()
	if ctx.Err() != nil {
		closeRaw()
		conn.CloseNow()
		transport.CloseIdleConnections()
		return nil, 0, ErrUnavailable
	}
	conn.SetReadLimit(1 << 20)
	a := &ownedAnchor{conn: conn, transport: transport, launch: l, ref: ref, identity: identity}
	if currentPath, currentID, e := ownedSocket(l.Endpoint); e != nil || currentPath != path || currentID != identity {
		a.close()
		return nil, 0, ErrConflict
	}
	return a, status, nil
}

func (a *ownedAnchor) close() { _ = a.conn.CloseNow(); a.transport.CloseIdleConnections() }
func (a *ownedAnchor) write(ctx context.Context, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return ErrInvalid
	}
	if err = a.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		return ErrUnavailable
	}
	return nil
}
func (a *ownedAnchor) call(ctx context.Context, method string, params any, result any) error {
	a.next++
	id := fmt.Sprintf("owned-%d", a.next)
	if err := a.write(ctx, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	{
		kind, raw, err := a.conn.Read(ctx)
		if err != nil || kind != websocket.MessageText {
			return ErrUnavailable
		}
		frame, err := cb.NewPolicy().Decode(raw)
		if err != nil {
			return ErrUnavailable
		}
		// Unknown callbacks and notifications are blockers, never approvals. No
		// callback is answered and no unscoped payload leaves this private adapter.
		if frame.Kind != cb.Result || string(frame.ID) != fmt.Sprintf("%q", id) {
			return ErrUnavailable
		}
		if json.Unmarshal(frame.Payload, result) != nil {
			return ErrUnavailable
		}
		return nil
	}
}

func (u *OwnedUpstream) Inspect(ctx context.Context, l Launch, ref domain.RuntimeRef) (Evidence, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !ownedImageSupported() || verifyOwnedImage(ref, l.BinarySHA256) != nil {
		return Evidence{}, ErrUnavailable
	}
	a, ok := u.anchors[l.Incarnation]
	if ok && (a.ref != ref || a.launch.Request != l.Request || a.launch.CapabilitySHA256() != l.CapabilitySHA256()) {
		return Evidence{}, ErrConflict
	}
	var err error
	if !ok {
		a, err = u.connect(ctx, l, ref)
		if err != nil {
			return Evidence{}, err
		}
		u.anchors[l.Incarnation] = a
	}
	fail := func() (Evidence, error) {
		a.close()
		delete(u.anchors, l.Incarnation)
		return Evidence{}, ErrUnavailable
	}
	if _, id, e := ownedSocket(l.Endpoint); e != nil || id != a.identity {
		return fail()
	}
	// One owned primary means exactly one loaded thread. Never infer loading from
	// an on-disk thread/read response. Additional pages or threads fail closed.
	var loaded struct {
		Data []string        `json:"data"`
		Next json.RawMessage `json:"nextCursor"`
	}
	if a.call(ctx, "thread/loaded/list", map[string]any{"limit": 2}, &loaded) != nil || len(loaded.Data) != 1 || loaded.Data[0] != l.Request.NativeThreadID || string(loaded.Next) != "null" {
		return fail()
	}
	var read struct {
		Thread struct {
			ID     string `json:"id"`
			Cwd    string `json:"cwd"`
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if a.call(ctx, "thread/read", map[string]any{"threadId": l.Request.NativeThreadID, "includeTurns": false}, &read) != nil || read.Thread.ID != l.Request.NativeThreadID || read.Thread.Cwd != l.Request.Workspace || read.Thread.Status.Type != "idle" {
		return fail()
	}
	for _, method := range []string{"thread/queue/list", "thread/backgroundTerminals/list"} {
		var list struct {
			Data json.RawMessage `json:"data"`
			Next json.RawMessage `json:"nextCursor"`
		}
		if a.call(ctx, method, map[string]any{"threadId": l.Request.NativeThreadID, "limit": 1}, &list) != nil || string(list.Data) != "[]" || string(list.Next) != "null" {
			return fail()
		}
	}
	if verifyOwnedImage(ref, l.BinarySHA256) != nil {
		return fail()
	}
	t := l.Request.Target
	return Evidence{Agent: cb.AgentBinding{HostID: t.HostID, SessionID: t.SessionID, ExecutionID: t.ExecutionID, Generation: t.Generation, AppServerIncarnation: l.Incarnation, AppServerPID: ref.PID, AppServerBootID: ref.BootID, AppServerProcessStart: ref.ProcessStart, ProtocolVersion: ProtocolVersion, NativeThreadID: l.Request.NativeThreadID}, CapabilitySHA256: l.CapabilitySHA256(), SocketIdentity: a.identity, Idle: true, EmptyQueue: true, EmptyBackground: true}, nil
}
func (u *OwnedUpstream) Close(ctx context.Context, id domain.ID) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if a, ok := u.anchors[id]; ok {
		a.close()
		delete(u.anchors, id)
	}
	return nil
}
func ownedSocket(path string) (string, string, error) {
	// Codex 0.159.0 can put an owned alias at the requested endpoint. Pin both the
	// alias inode and canonical socket inode; no chmod or shared-path replacement.
	original, err := os.Lstat(path)
	if err != nil {
		return "", "", err
	}
	own, ok := original.Sys().(*syscall.Stat_t)
	if !ok || own.Uid != uint32(os.Geteuid()) {
		return "", "", ErrUnavailable
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", err
	}
	for _, p := range []string{filepath.Dir(path), filepath.Dir(canonical)} {
		info, e := os.Stat(p)
		if e != nil {
			return "", "", e
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Geteuid()) || !info.IsDir() || info.Mode().Perm() != 0700 {
			return "", "", ErrUnavailable
		}
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return "", "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		return "", "", ErrUnavailable
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d:%d", own.Dev, own.Ino, st.Dev, st.Ino)))
	return canonical, hex.EncodeToString(sum[:]), nil
}

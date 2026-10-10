package daemon_test

// This suite exercises the real Service.CodexAuthority, SQLite journal and
// Unix-WebSocket relay. Only process provenance, runtime and quiescence evidence
// are explicit fixtures; no fake Authority or fake transport delivery counter
// is used. It starts no Codex program, submits no model turn and proves no
// native authentication, real process ownership or remote machine behavior.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cb "github.com/seanheiney/agent-backplane/internal/codexbroker"
	"github.com/seanheiney/agent-backplane/internal/codexbroker/wire"
	"github.com/seanheiney/agent-backplane/internal/daemon"
	"github.com/seanheiney/agent-backplane/internal/domain"
	"github.com/seanheiney/agent-backplane/internal/runtime"
	"github.com/seanheiney/agent-backplane/internal/store"
)

type relayAcceptanceVerifier struct {
	mu          sync.Mutex
	agent       cb.AgentBinding
	writers     map[domain.ID]cb.WriterRequest
	connections map[domain.ID]cb.ConnectionBinding
}

func (v *relayAcceptanceVerifier) VerifyAgent(_ context.Context, a cb.AgentBinding) error {
	if a != v.agent {
		return errors.New("fixture primary differs")
	}
	return nil
}
func (v *relayAcceptanceVerifier) VerifyWriter(_ context.Context, r cb.WriterRequest) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.writers[r.OperationID] != r {
		return errors.New("fixture writer capability absent")
	}
	return nil
}
func (v *relayAcceptanceVerifier) VerifyConnection(_ context.Context, c cb.ConnectionBinding) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.connections[c.ID] != c {
		return errors.New("fixture launch capability absent")
	}
	return nil
}

type relayAcceptanceProbe struct{}

func (relayAcceptanceProbe) Inspect(_ context.Context, a cb.AgentBinding) (cb.Quiescence, error) {
	return cb.Quiescence{Agent: a, Idle: true, EmptyQueue: true, EmptyBackground: true, NativeWritersDetached: true, Source: "explicit_owned_quiescence_fixture", ObservedAt: time.Now().UTC()}, nil
}

type relayAcceptanceRuntime struct{}

func (relayAcceptanceRuntime) Launch(context.Context, domain.LaunchSpec) (domain.RuntimeRef, error) {
	return domain.RuntimeRef{}, errors.New("no native launch in relay fixture")
}
func (relayAcceptanceRuntime) Lookup(context.Context, domain.ID, domain.ID) (runtime.Observation, error) {
	return runtime.Observation{}, errors.New("no native lookup in relay fixture")
}
func (relayAcceptanceRuntime) Inspect(context.Context, domain.RuntimeRef) (runtime.Observation, error) {
	return runtime.Observation{}, errors.New("no native inspect in relay fixture")
}
func (relayAcceptanceRuntime) Stop(context.Context, domain.RuntimeRef) error {
	return errors.New("no native stop in relay fixture")
}
func (relayAcceptanceRuntime) AttachCommand(domain.RuntimeRef, bool) ([]string, error) {
	return nil, errors.New("no native terminal in relay fixture")
}

type relayAcceptanceSmallBuffer struct{ net.Listener }

func (l relayAcceptanceSmallBuffer) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		u, ok := c.(*net.UnixConn)
		if !ok {
			_ = c.Close()
			return nil, errors.New("fixture expected owned Unix connection")
		}
		if err = u.SetWriteBuffer(1024); err != nil {
			_ = c.Close()
			return nil, err
		}
	}
	return c, err
}

type relayAcceptancePair struct{ server, client *wire.Conn }

func relayAcceptanceUnixPair(t *testing.T, ctx context.Context, path string, smallBuffer bool) relayAcceptancePair {
	t.Helper()
	pairCtx, pairCancel := context.WithCancel(ctx)
	l, err := wire.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if smallBuffer {
		l = relayAcceptanceSmallBuffer{l}
	}
	accepted := make(chan *wire.Conn, 1)
	acceptError := make(chan error, 1)
	handlerDone := make(chan struct{})
	s := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		c, err := wire.Accept(w, r)
		if err != nil {
			acceptError <- err
			return
		}
		accepted <- c
		<-pairCtx.Done()
		_ = c.Close()
	})}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = s.Serve(l) }()
	client, err := wire.Dial(ctx, path)
	if err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	var server *wire.Conn
	select {
	case server = <-accepted:
	case err := <-acceptError:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	t.Cleanup(func() {
		pairCancel()
		_ = server.Close()
		_ = client.Close()
		_ = s.Close()
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("owned Unix HTTP server did not stop")
		}
		select {
		case <-handlerDone:
		case <-time.After(time.Second):
			t.Error("owned hijacked handler did not stop")
		}
	})
	return relayAcceptancePair{server, client}
}

type relayAcceptanceEnv struct {
	ctx        context.Context
	cancel     context.CancelFunc
	st         *store.Store
	dbPath     string
	session    domain.Session
	agent      cb.AgentBinding
	verifier   *relayAcceptanceVerifier
	authority  cb.Authority
	grant      cb.Grant
	binding    cb.ConnectionBinding
	channel    *cb.Channel
	up, native relayAcceptancePair
	frames     chan cb.Frame
	upCount    atomic.Int64
	runDone    chan error
}

func relayAcceptanceNew(t *testing.T, policy cb.Policy, smallBuffer bool) *relayAcceptanceEnv {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "bp-relay-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	e := &relayAcceptanceEnv{ctx: ctx, cancel: cancel, frames: make(chan cb.Frame, 64), runDone: make(chan error, 1)}
	e.dbPath = filepath.Join(dir, "state.db")
	st, err := store.Open(e.dbPath)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	e.st = st
	t.Cleanup(func() { _ = st.Close() })
	e.session = domain.NewSession("host_relay_fixture", "relay fixture", dir, "codex", "0.159.0")
	op := domain.Operation{ID: "record_fixture", Kind: "fixture.session", SessionID: e.session.ID, ExecutionID: e.session.Execution.ID}
	if _, _, err := st.Record(ctx, op, []byte(`{"fixture":true}`), &e.session); err != nil {
		t.Fatal(err)
	}
	svc, err := daemon.New(st, relayAcceptanceRuntime{}, e.session.HostID)
	if err != nil {
		t.Fatal(err)
	}
	e.agent = cb.AgentBinding{HostID: e.session.HostID, SessionID: e.session.ID, ExecutionID: e.session.Execution.ID, Generation: e.session.Execution.Generation,
		AppServerIncarnation: "app_fixture", AppServerPID: 1001, AppServerBootID: "boot_fixture", AppServerProcessStart: "app_start_fixture", ProtocolVersion: "0.159.0", NativeThreadID: "native_A"}
	e.verifier = &relayAcceptanceVerifier{agent: e.agent, writers: map[domain.ID]cb.WriterRequest{}, connections: map[domain.ID]cb.ConnectionBinding{}}
	baseline := cb.DefaultBaseline(dir)
	relay, err := cb.NewRelay(policy, baseline)
	if err != nil {
		t.Fatal(err)
	}
	e.authority = svc.CodexAuthority(policy, baseline, relay, relayAcceptanceProbe{}, e.verifier)
	req := cb.WriterRequest{Agent: e.agent, OperationID: "acquire_fixture", PrincipalID: "principal_fixture", ClientID: "client_fixture", Kind: cb.NativeWriter}
	e.verifier.writers[req.OperationID] = req
	e.grant, err = e.authority.Acquire(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	e.binding = cb.ConnectionBinding{ID: "connection_fixture", Agent: e.agent, Writer: e.grant.WriterRef, Role: cb.NativeLocal,
		NativeLaunchID: "launch_fixture", EndpointIncarnation: "endpoint_fixture", NativePID: 1002, NativeProcessStart: "native_start_fixture"}
	e.verifier.connections[e.binding.ID] = e.binding
	e.up = relayAcceptanceUnixPair(t, ctx, filepath.Join(dir, "u.sock"), false)
	e.native = relayAcceptanceUnixPair(t, ctx, filepath.Join(dir, "n.sock"), smallBuffer)
	e.channel, err = relay.Open(ctx, e.authority, e.binding, e.up.client)
	if err != nil {
		t.Fatal(err)
	}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			raw, err := e.up.server.Read(ctx)
			if err != nil {
				return
			}
			f, err := cb.NewPolicy().Decode(raw)
			if err != nil {
				return
			}
			e.upCount.Add(1)
			select {
			case e.frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { e.runDone <- e.channel.RunNative(ctx, e.native.server) }()
	t.Cleanup(func() {
		cancel()
		_ = e.channel.Close()
		select {
		case <-e.runDone:
		case <-time.After(time.Second):
			t.Error("owned native fixture loop did not stop")
		}
		select {
		case <-readerDone:
		case <-time.After(time.Second):
			t.Error("owned upstream fixture loop did not stop")
		}
	})
	return e
}

func relayAcceptanceJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func relayAcceptanceCanonicalJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal("owned fixture JSON is invalid")
	}
	return relayAcceptanceJSON(t, value)
}
func (e *relayAcceptanceEnv) write(t *testing.T, raw []byte) {
	t.Helper()
	if err := e.native.client.Write(e.ctx, raw); err != nil {
		t.Fatal(err)
	}
}
func (e *relayAcceptanceEnv) request(t *testing.T, id any, method string, params any) {
	t.Helper()
	e.write(t, relayAcceptanceJSON(t, map[string]any{"id": id, "method": method, "params": params}))
}
func (e *relayAcceptanceEnv) nextUpstream(t *testing.T) cb.Frame {
	t.Helper()
	select {
	case f := <-e.frames:
		return f
	case <-e.ctx.Done():
		t.Fatal("owned upstream frame absent", e.ctx.Err())
		return cb.Frame{}
	}
}
func (e *relayAcceptanceEnv) nextNative(t *testing.T) cb.Frame {
	t.Helper()
	raw, err := e.native.client.Read(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	f, err := cb.NewPolicy().Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (e *relayAcceptanceEnv) source(t *testing.T, frame cb.Frame) {
	t.Helper()
	raw, err := cb.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.up.server.Write(e.ctx, raw); err != nil {
		t.Fatal(err)
	}
}
func (e *relayAcceptanceEnv) threadReply(t *testing.T, id json.RawMessage, thread string) {
	t.Helper()
	e.source(t, cb.Frame{Kind: cb.Result, ID: id, Payload: relayAcceptanceJSON(t, map[string]any{"thread": map[string]any{"id": thread, "preview": "café λ", "cwd": e.session.Workspace, "turns": []any{}}})})
}
func (e *relayAcceptanceEnv) receipt(t *testing.T, id json.RawMessage, state cb.ReceiptState) cb.Receipt {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := e.st.Events(e.ctx, e.session.ThreadID, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range events {
			if ev.Kind != "codex.rpc.attempted" {
				continue
			}
			r, err := e.st.CodexReceipt(e.ctx, ev.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(r.Intent.Frame.ID, id) && r.State == state {
				return r
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("durable receipt did not reach %s", state)
	return cb.Receipt{}
}
func (e *relayAcceptanceEnv) handover(t *testing.T, op domain.ID) (cb.Grant, error) {
	t.Helper()
	h := cb.Handover{OperationID: op, Previous: e.grant.WriterRef, NextPrincipalID: "principal_fixture", NextClientID: "next_client_fixture", NextKind: cb.SemanticWriter}
	r := cb.WriterRequest{Agent: e.agent, OperationID: op, PrincipalID: h.NextPrincipalID, ClientID: h.NextClientID, Kind: h.NextKind}
	e.verifier.mu.Lock()
	e.verifier.writers[op] = r
	e.verifier.mu.Unlock()
	return e.authority.Handover(e.ctx, h)
}
func (e *relayAcceptanceEnv) blocked(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g, _, err := e.st.CodexGrant(e.ctx, e.grant.Lease.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		b, err := e.st.CodexBlocked(e.ctx, e.agent)
		if err != nil {
			t.Fatal(err)
		}
		if b && g.Active && g.Admission == cb.Fenced {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("source uncertainty did not durably reserve/fence shared writer")
}

// Read only the owned database: no second Store authority or alternate writer
// is created. This independently checks retained source bytes, not a wrapper's
// claimed RecordCallback counter.
func (e *relayAcceptanceEnv) callbacks(t *testing.T) []cb.CallbackRecord {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+e.dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(e.ctx, "SELECT data FROM codex_callbacks ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var records []cb.CallbackRecord
	for rows.Next() {
		var raw []byte
		var c cb.CallbackRecord
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		records = append(records, c)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestCodexRelayAcceptanceExactRoundTripAndDenialCounter(t *testing.T) {
	e := relayAcceptanceNew(t, cb.NewPolicy(), false)
	denials := []struct {
		method string
		params any
	}{
		{"future/method", map[string]any{}},
		{"thread/read", map[string]any{"threadId": "native_B"}},
		{"account/read", map[string]any{"refreshToken": true}},
		{"thread/resume", map[string]any{"threadId": "native_A", "cwd": e.session.Workspace, "history": []any{map[string]any{"fixture": true}}}},
		{"turn/start", map[string]any{"threadId": "native_A", "input": []any{map[string]any{"type": "text", "text": "fixture-do-not-execute"}}}},
	}
	for i, c := range denials {
		id := fmt.Sprintf("denied-%d", i)
		e.request(t, id, c.method, c.params)
		got := e.nextNative(t)
		if got.Kind != cb.RPCError || string(got.ID) != string(relayAcceptanceJSON(t, id)) {
			t.Fatal("policy refusal lost original typed client correlation")
		}
	}
	for _, raw := range []string{`{"id":7,"result":{"decision":"accept"}}`, `{"id":"auth_fixture","result":{"accessToken":"NON_SECRET_FIXTURE"}}`, `{"id":8,"error":{"code":-32000,"message":"fixture"}}`} {
		e.write(t, []byte(raw))
		if got := e.nextNative(t); got.Kind != cb.RPCError {
			t.Fatal("downstream reply was not refused")
		}
	}
	// The following allowed request is a FIFO transport barrier after refusals.
	e.request(t, "7", "thread/read", map[string]any{"threadId": "native_A"})
	u := e.nextUpstream(t)
	if u.Method != "thread/read" || string(u.ID) == `"7"` || e.upCount.Load() != 1 {
		t.Fatal("denied frame reached actual owned upstream, or ID was not remapped")
	}
	e.threadReply(t, u.ID, "native_A")
	got := e.nextNative(t)
	if got.Kind != cb.Result || string(got.ID) != `"7"` || !bytes.Contains(got.Payload, []byte("café λ")) {
		t.Fatal("actual relay lost useful Unicode payload or typed downstream ID")
	}
	e.receipt(t, u.ID, cb.Acknowledged)
	e.request(t, 7, "thread/read", map[string]any{"threadId": "native_A"})
	u2 := e.nextUpstream(t)
	if bytes.Equal(u.ID, u2.ID) || e.upCount.Load() != 2 {
		t.Fatal("mixed client ID types reused upstream identity")
	}
	e.threadReply(t, u2.ID, "native_A")
	if got := e.nextNative(t); string(got.ID) != "7" {
		t.Fatal("integer ID collided with string ID")
	}
}

func TestCodexRelayAcceptancePendingThenHistoricalAckAfterRevoke(t *testing.T) {
	e := relayAcceptanceNew(t, cb.NewPolicy(), false)
	e.request(t, 1, "thread/read", map[string]any{"threadId": "native_A"})
	u := e.nextUpstream(t)
	e.receipt(t, u.ID, cb.PendingAck)
	if _, err := e.handover(t, "pending_next"); err == nil {
		t.Fatal("unacknowledged actual upstream send allowed handover")
	}
	g, err := e.authority.Revoke(e.ctx, e.grant.WriterRef)
	if err != nil || !g.Active || g.Admission != cb.Fenced {
		t.Fatal("revoke did not reserve exact old writer slot", err)
	}
	e.request(t, 2, "thread/read", map[string]any{"threadId": "native_A"})
	if _, err := e.native.client.Read(e.ctx); err == nil {
		t.Fatal("revoked old UI received output")
	}
	e.threadReply(t, u.ID, "native_A")
	e.receipt(t, u.ID, cb.Acknowledged)
	if e.upCount.Load() != 1 {
		t.Fatal("revoked request reached actual upstream")
	}
	next, err := e.handover(t, "settled_next")
	if err != nil || next.Epoch <= e.grant.Epoch || next.Kind != cb.SemanticWriter {
		t.Fatal("historical exact ack failed to permit fresh fixture-idle handover", err)
	}
	if _, err := e.authority.Bind(e.ctx, e.binding); err == nil {
		t.Fatal("retired original native endpoint rebound")
	}
	if err := e.channel.RunNative(e.ctx, e.native.server); err == nil {
		t.Fatal("consumed endpoint accepted a second UI")
	}
}

func TestCodexRelayAcceptanceWrongSelectedThreadRetainsUnknownOutcome(t *testing.T) {
	for _, method := range []string{"thread/read", "thread/resume"} {
		t.Run(method, func(t *testing.T) {
			e := relayAcceptanceNew(t, cb.NewPolicy(), false)
			params := map[string]any{"threadId": "native_A"}
			if method == "thread/resume" {
				params["cwd"] = e.session.Workspace
				params["excludeTurns"] = true
				params["config"] = map[string]any{"features": map[string]any{"plugins": false, "apps": false}, "web_search": "cached"}
			}
			e.request(t, 1, method, params)
			u := e.nextUpstream(t)
			e.receipt(t, u.ID, cb.PendingAck)
			e.threadReply(t, u.ID, "native_B")
			e.blocked(t)
			if _, err := e.native.client.Read(e.ctx); err == nil {
				t.Fatal("wrong selected thread was projected")
			}
			r := e.receipt(t, u.ID, cb.PendingAck)
			if r.State == cb.Acknowledged {
				t.Fatal("response identity drift became completed idle evidence")
			}
			if _, err := e.handover(t, "drift_next"); err == nil {
				t.Fatal("wrong-thread source outcome permitted idle handover")
			}
			if e.upCount.Load() != 1 {
				t.Fatal("wrong-thread outcome was replayed")
			}
		})
	}
}

type relayAcceptanceObservedPolicy struct {
	cb.Policy
	entered chan struct{}
	once    sync.Once
}

func (p *relayAcceptanceObservedPolicy) NativeOutput(c cb.OutputContext, f cb.Frame) (cb.Frame, bool, error) {
	out, ok, err := p.Policy.NativeOutput(c, f)
	if f.Kind == cb.Notification && f.Method == "thread/started" && ok && err == nil {
		p.once.Do(func() { close(p.entered) })
	}
	return out, ok, err
}

func TestCodexRelayAcceptanceActualDownstreamWriteSharesRevokeGate(t *testing.T) {
	p := &relayAcceptanceObservedPolicy{Policy: cb.NewPolicy(), entered: make(chan struct{})}
	e := relayAcceptanceNew(t, p, true)
	e.request(t, 1, "thread/read", map[string]any{"threadId": "native_A"})
	u := e.nextUpstream(t)
	e.threadReply(t, u.ID, "native_A")
	e.nextNative(t)
	e.receipt(t, u.ID, cb.Acknowledged)
	// The owned server-side Unix write buffer is deliberately small. No native
	// fixture reader runs during this real large frame write; the observation
	// hook is nonblocking and never substitutes for authority serialization.
	text := strings.Repeat("λ", 16000)
	e.source(t, cb.Frame{Kind: cb.Notification, Method: "thread/started", Payload: relayAcceptanceJSON(t, map[string]any{"thread": map[string]any{"id": "native_A", "preview": text, "cwd": e.session.Workspace, "turns": []any{}}})})
	select {
	case <-p.entered:
	case <-e.ctx.Done():
		t.Fatal("large scoped notification did not reach gated projection")
	}
	revoked := make(chan error, 1)
	go func() { _, err := e.authority.Revoke(e.ctx, e.grant.WriterRef); revoked <- err }()
	select {
	case err := <-revoked:
		t.Fatal("revoke overtook real blocked downstream write", err)
	case <-time.After(75 * time.Millisecond):
	}
	got := e.nextNative(t)
	if got.Kind != cb.Notification || !bytes.Contains(got.Payload, []byte(text)) {
		t.Fatal("actual downstream frame was truncated")
	}
	select {
	case err := <-revoked:
		if err != nil {
			t.Fatal(err)
		}
	case <-e.ctx.Done():
		t.Fatal("revoke did not finish after actual downstream drain")
	}
	e.request(t, 2, "thread/read", map[string]any{"threadId": "native_A"})
	if _, err := e.native.client.Read(e.ctx); err == nil {
		t.Fatal("old UI still received after real revoke gate")
	}
	if e.upCount.Load() != 1 {
		t.Fatal("post-revoke request reached actual upstream")
	}
}

func TestCodexRelayAcceptanceMalformedClientHasZeroUpstreamEffect(t *testing.T) {
	for name, raw := range map[string][]byte{
		"whole_batch":      []byte(`[{"id":1,"method":"thread/read","params":{"threadId":"native_A"}}]`),
		"duplicate_method": []byte(`{"id":1,"method":"thread/read","method":"turn/start","params":{"threadId":"native_A"}}`),
		"nested_duplicate": []byte(`{"id":1,"method":"thread/read","params":{"threadId":"native_A","threadId":"native_B"}}`),
		"mixed_envelope":   []byte(`{"id":1,"method":"thread/read","result":{},"params":{"threadId":"native_A"}}`),
		"invalid_utf8":     append([]byte(`{"id":1,"method":"thread/read","params":{"threadId":"`), 0xff, '"', '}', '}'),
		"depth_bound":      []byte(`{"id":1,"method":"future/method","params":` + strings.Repeat("[", 80) + "0" + strings.Repeat("]", 80) + "}"),
	} {
		t.Run(name, func(t *testing.T) {
			e := relayAcceptanceNew(t, cb.NewPolicy(), false)
			e.write(t, raw)
			if _, err := e.native.client.Read(e.ctx); err == nil {
				t.Fatal("malformed frame kept native channel open")
			}
			if e.upCount.Load() != 0 {
				t.Fatal("malformed client frame reached actual upstream")
			}
			events, err := e.st.Events(e.ctx, e.session.ThreadID, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, ev := range events {
				if ev.Kind == "codex.rpc.attempted" {
					t.Fatal("malformed client created an attempted durable send")
				}
			}
		})
	}
}

func TestCodexRelayAcceptanceExactDuplicateNeverDeliversTwice(t *testing.T) {
	e := relayAcceptanceNew(t, cb.NewPolicy(), false)
	e.request(t, 1, "thread/read", map[string]any{"threadId": "native_A"})
	u := e.nextUpstream(t)
	e.threadReply(t, u.ID, "native_A")
	first := e.nextNative(t)
	if first.Kind != cb.Result || string(first.ID) != "1" {
		t.Fatal("known response control failed")
	}
	e.receipt(t, u.ID, cb.Acknowledged)
	e.threadReply(t, u.ID, "native_A")
	// The following notification is an upstream FIFO barrier. If the exact
	// duplicate was forwarded, it would be the next frame observed by the UI.
	e.source(t, cb.Frame{Kind: cb.Notification, Method: "thread/started", Payload: relayAcceptanceJSON(t, map[string]any{"thread": map[string]any{"id": "native_A", "preview": "duplicate barrier", "cwd": e.session.Workspace, "turns": []any{}}})})
	if got := e.nextNative(t); got.Kind != cb.Notification || !bytes.Contains(got.Payload, []byte("duplicate barrier")) {
		t.Fatal("exact duplicate was delivered a second time")
	}
	if len(e.callbacks(t)) != 0 {
		t.Fatal("exact duplicate manufactured uncertain source evidence")
	}
	if _, err := e.handover(t, "duplicate_next"); err != nil {
		t.Fatal("exact duplicate prevented settled fixture-idle handover", err)
	}
	if e.upCount.Load() != 1 {
		t.Fatal("duplicate source outcome caused a new upstream request")
	}
}

func TestCodexRelayAcceptanceUnknownChangedAndCallbackQuarantine(t *testing.T) {
	for _, name := range []string{"unknown_result", "unknown_error", "changed_settled_result", "native_callback"} {
		t.Run(name, func(t *testing.T) {
			e := relayAcceptanceNew(t, cb.NewPolicy(), false)
			frame := cb.Frame{Kind: cb.Result, ID: json.RawMessage(`"source_unmapped"`), Payload: json.RawMessage(`{"fixture":"unmapped source result"}`)}
			wantCount := int64(0)
			switch name {
			case "unknown_error":
				frame.Kind, frame.Payload = cb.RPCError, json.RawMessage(`{"code":-32000,"message":"source fixture"}`)
			case "changed_settled_result":
				e.request(t, 1, "thread/read", map[string]any{"threadId": "native_A"})
				u := e.nextUpstream(t)
				e.threadReply(t, u.ID, "native_A")
				e.nextNative(t)
				e.receipt(t, u.ID, cb.Acknowledged)
				frame.ID = u.ID
				frame.Payload = relayAcceptanceJSON(t, map[string]any{"thread": map[string]any{"id": "native_A", "preview": "changed outcome", "cwd": e.session.Workspace, "turns": []any{}}})
				wantCount = 1
			case "native_callback":
				frame.Kind, frame.Method = cb.Request, "item/commandExecution/requestApproval"
				frame.Payload = json.RawMessage(`{"threadId":"native_A","turnId":"turn_fixture","itemId":"item_fixture","command":"fixture command","availableDecisions":["accept","decline"]}`)
			}
			e.source(t, frame)
			e.blocked(t)
			records := e.callbacks(t)
			if len(records) != 1 {
				t.Fatal("source frame was not retained exactly once")
			}
			c := records[0]
			if c.Origin != e.binding || !bytes.Equal(c.Key.RequestID, frame.ID) || c.Key.UpstreamChannelID == "" || c.Attributed || c.Resolved || c.DecisionState != cb.Uncertain || len(c.Allowed) != 0 {
				t.Fatal("quarantined source became actionable or lost immutable identity")
			}
			stored, err := cb.NewPolicy().Decode(c.Payload)
			want, encodeErr := cb.Encode(frame)
			got, storedErr := cb.Encode(stored)
			if err != nil || encodeErr != nil || storedErr != nil || !bytes.Equal(relayAcceptanceCanonicalJSON(t, got), relayAcceptanceCanonicalJSON(t, want)) {
				t.Fatal("quarantine lost full source envelope")
			}
			if _, err := e.handover(t, "quarantine_next"); err == nil {
				t.Fatal("unattributed or changed outcome permitted idle handover")
			}
			if _, err := e.authority.Decide(e.ctx, cb.Decision{OperationID: "decide_fixture", CallbackID: c.ID, Key: c.Key, Actor: e.binding, Action: cb.Accept}); err == nil {
				t.Fatal("first profile accepted a native callback decision")
			}
			// Fenced source has not displayed the callback. The old UI's attempt
			// cannot answer it or reach upstream; its refused output also fails
			// the real revoked delivery gate and closes that disposable channel.
			e.write(t, []byte(`{"id":"source_unmapped","result":{"decision":"accept"}}`))
			if _, err := e.native.client.Read(e.ctx); err == nil {
				t.Fatal("quarantined callback/source frame reached old UI")
			}
			if e.upCount.Load() != wantCount {
				t.Fatal("source quarantine or downstream reply reached actual upstream")
			}
		})
	}
}

func TestCodexRelayAcceptanceMalformedOwnedSourceCannotBecomeIdleEvidence(t *testing.T) {
	e := relayAcceptanceNew(t, cb.NewPolicy(), false)
	// There is no outstanding request whose PendingAck happens to block
	// handover. The owned source itself became untrustworthy; UI disposal alone
	// must not erase that unresolved protocol outcome from home authority.
	raw := []byte(`{"id":"source_malformed","result":{},"error":{"code":-32000,"message":"fixture"}}`)
	if err := e.up.server.Write(e.ctx, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := e.native.client.Read(e.ctx); err == nil {
		t.Fatal("malformed owned source did not close disposable UI")
	}
	if _, err := e.handover(t, "malformed_source_next"); err == nil {
		t.Fatal("malformed owned source became resolved fixture-idle handover evidence")
	}
	e.blocked(t)
	if e.upCount.Load() != 0 {
		t.Fatal("malformed source recovery created an upstream request")
	}
}

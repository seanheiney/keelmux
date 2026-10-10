package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

// The fixture proves ownership from private host registrations, rather than
// accepting otherwise-public IDs. It performs no process or provider work.
type actorVerifier struct {
	mu          sync.Mutex
	agent       cb.AgentBinding
	writers     map[string]bool
	connections map[string]bool
	fail        bool
}

func (v *actorVerifier) VerifyAgent(_ context.Context, a cb.AgentBinding) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fail || !identical(v.agent, a) {
		return errors.New("private-verifier-secret")
	}
	return nil
}
func (v *actorVerifier) VerifyWriter(_ context.Context, r cb.WriterRequest) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fail || !v.writers[string(payload(r))] {
		return errors.New("private-verifier-secret")
	}
	return nil
}
func (v *actorVerifier) VerifyConnection(_ context.Context, c cb.ConnectionBinding) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fail || !v.connections[string(payload(c))] {
		return errors.New("private-verifier-secret")
	}
	return nil
}
func (v *actorVerifier) ownWriter(r cb.WriterRequest) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.writers[string(payload(r))] = true
}
func (v *actorVerifier) ownConnection(c cb.ConnectionBinding) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.connections[string(payload(c))] = true
}

type actorPolicy struct{}

func (actorPolicy) Decode(b []byte) (cb.Frame, error) {
	var x struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&x); err != nil {
		return cb.Frame{}, err
	}
	if x.JSONRPC != "2.0" || x.Method == "" {
		return cb.Frame{}, errors.New("invalid fixture request")
	}
	kind := cb.Notification
	if len(x.ID) != 0 {
		kind = cb.Request
	}
	return cb.Frame{Kind: kind, ID: x.ID, Method: x.Method, Payload: x.Params}, nil
}
func (actorPolicy) Classify(c cb.ConnectionBinding, f cb.Frame, _ cb.Baseline) (cb.EffectClass, error) {
	if !bytes.Equal(f.Payload, payload(map[string]string{"threadId": c.Agent.NativeThreadID})) {
		return cb.Denied, errors.New("scope mismatch")
	}
	switch f.Method {
	case "initialize", "initialized", "thread/resume", "account/read":
		return cb.NativeBootstrap, nil
	case "fixture/read":
		return cb.ScopedRead, nil
	case "fixture/unsubscribe":
		return cb.ViewerLocal, nil
	case "turn/start":
		return cb.Mutation, nil
	}
	return cb.Denied, nil
}
func (actorPolicy) NativeOutput(_ cb.OutputContext, f cb.Frame) (cb.Frame, bool, error) {
	return f, true, nil
}

type actorSender struct {
	mu    sync.Mutex
	calls int
	hook  func(context.Context, domain.ID, []byte) error
}

func (s *actorSender) WriteFrame(ctx context.Context, c domain.ID, b []byte) error {
	s.mu.Lock()
	s.calls++
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		return hook(ctx, c, b)
	}
	return nil
}
func (s *actorSender) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

type actorProbe struct {
	fn func(cb.AgentBinding) (cb.Quiescence, error)
}

func (p *actorProbe) Inspect(_ context.Context, a cb.AgentBinding) (cb.Quiescence, error) {
	if p.fn != nil {
		return p.fn(a)
	}
	return cb.Quiescence{Agent: a, Idle: true, EmptyQueue: true, EmptyBackground: true, NativeWritersDetached: true, Source: "owned-fixture-primary", ObservedAt: time.Now()}, nil
}

type actorEnv struct {
	e        *env
	v        domain.Session
	agent    cb.AgentBinding
	verifier *actorVerifier
	sender   *actorSender
	probe    *actorProbe
	a        cb.Authority
}

func actorSetup(t *testing.T) *actorEnv {
	t.Helper()
	e := setup(t)
	v := e.launch(t).Session
	a := cb.AgentBinding{HostID: v.HostID, SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation, AppServerIncarnation: "owned-primary", AppServerPID: 4321, AppServerBootID: "owned-boot", AppServerProcessStart: "owned-start", ProtocolVersion: "fixture-v1", NativeThreadID: "owned-thread"}
	x := &actorEnv{e: e, v: v, agent: a, sender: &actorSender{}, probe: &actorProbe{}, verifier: &actorVerifier{agent: a, writers: map[string]bool{}, connections: map[string]bool{}}}
	x.reset()
	return x
}
func (x *actorEnv) reset() {
	x.a = x.e.service.CodexAuthority(actorPolicy{}, cb.Baseline{ProtocolVersion: x.agent.ProtocolVersion, Workspace: x.e.workspace}, x.sender, x.probe, x.verifier)
}
func (x *actorEnv) request(op domain.ID, kind cb.WriterKind, ro bool) cb.WriterRequest {
	r := cb.WriterRequest{Agent: x.agent, OperationID: op, PrincipalID: "alice", ClientID: "owned-client-" + string(op), Kind: kind, ReadOnly: ro}
	x.verifier.ownWriter(r)
	return r
}
func (x *actorEnv) acquire(t *testing.T, op domain.ID, kind cb.WriterKind, ro bool) cb.Grant {
	t.Helper()
	g, err := x.a.Acquire(context.Background(), x.request(op, kind, ro))
	if err != nil {
		t.Fatal(err)
	}
	return g
}
func (x *actorEnv) connection(g cb.Grant, id domain.ID) cb.ConnectionBinding {
	role := cb.NativeLocal
	if g.Kind == cb.SemanticWriter {
		role = cb.TypedWriter
	}
	if g.ReadOnly {
		role = cb.TypedObserver
	}
	c := cb.ConnectionBinding{ID: id, Agent: x.agent, Writer: g.WriterRef, Role: role, NativeLaunchID: domain.ID("launch-" + string(id)), EndpointIncarnation: domain.ID("endpoint-" + string(id)), NativePID: 5432, NativeProcessStart: "owned-native-start"}
	x.verifier.ownConnection(c)
	return c
}
func (x *actorEnv) bind(t *testing.T, g cb.Grant, id domain.ID) cb.ConnectionBinding {
	t.Helper()
	c := x.connection(g, id)
	bound, err := x.a.Bind(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return bound
}
func actorIntent(c cb.ConnectionBinding, op domain.ID, method string) cb.Intent {
	return cb.Intent{OperationID: op, Origin: c, UpstreamChannelID: "owned-channel", DownstreamRequestID: json.RawMessage(`"downstream-1"`), Frame: cb.Frame{Kind: cb.Request, ID: json.RawMessage("123"), Method: method, Payload: payload(map[string]string{"threadId": c.Agent.NativeThreadID})}}
}
func (x *actorEnv) handover(g cb.Grant, op domain.ID, kind cb.WriterKind) cb.Handover {
	h := cb.Handover{OperationID: op, Previous: g.WriterRef, NextPrincipalID: "alice", NextClientID: "owned-client-" + string(op), NextKind: kind}
	x.verifier.ownWriter(cb.WriterRequest{Agent: x.agent, OperationID: op, PrincipalID: h.NextPrincipalID, ClientID: h.NextClientID, Kind: kind})
	return h
}

func TestCodexSharedWriterSlotBothDirections(t *testing.T) {
	x := actorSetup(t)
	legacy, err := x.e.service.Attach(context.Background(), acquisition(x.v, "legacy", "native-client", false))
	if err != nil {
		t.Fatal(err)
	}
	_, err = x.a.Acquire(context.Background(), x.request("broker-denied", cb.NativeWriter, false))
	assertConflict(t, err)
	if err = x.e.service.Release(context.Background(), release(legacy)); err != nil {
		t.Fatal(err)
	}
	g := x.acquire(t, "broker", cb.NativeWriter, false)
	raw, err := x.e.store.Grant(context.Background(), g.Lease.OperationID)
	if err != nil || raw.Lease != g.Lease || !raw.Active {
		t.Fatal("not the same grant row", err)
	}
	_, err = x.e.service.Attach(context.Background(), acquisition(x.v, "legacy-denied", "native-client", false))
	assertConflict(t, err)
	l := domain.Lease{SessionID: x.v.ID, ExecutionID: x.v.Execution.ID, Generation: x.v.Execution.Generation, OperationID: "store-denied", ClientID: "client", Token: "token"}
	_, err = x.e.store.Acquire(context.Background(), l, false, []byte("owned-test"))
	assertConflict(t, err)
	if err = x.e.store.Release(context.Background(), g.Lease); err == nil {
		t.Fatal("direct legacy release freed broker slot")
	}
	ro := x.acquire(t, "observer", cb.SemanticWriter, true)
	if !ro.ReadOnly || !g.Active {
		t.Fatal("bad observer")
	}
	_, err = x.a.Acquire(context.Background(), x.request("competitor", cb.SemanticWriter, false))
	assertConflict(t, err)
}
func TestCodexConcurrentNativeSemanticAcquisitionOneSlot(t *testing.T) {
	x := actorSetup(t)
	var wg sync.WaitGroup
	winners := make(chan cb.Grant, 16)
	for n := range 16 {
		kind := cb.NativeWriter
		if n%2 != 0 {
			kind = cb.SemanticWriter
		}
		r := x.request(domain.ID(fmt.Sprintf("concurrent-%d", n)), kind, false)
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := x.a.Acquire(context.Background(), r)
			if err == nil {
				winners <- g
				return
			}
			var c domain.ConflictError
			if !errors.As(err, &c) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatal("multiple cooperating writers", len(winners))
	}
	g := <-winners
	if !g.Active || g.Epoch != 1 {
		t.Fatal("losing acquisition altered epoch", g.Epoch)
	}
}
func TestCodexConcurrentDuplicateIntentOneSend(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.NativeWriter, false)
	c := x.bind(t, g, "conn")
	in := actorIntent(c, "same-operation", "fixture/read")
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := x.a.Dispatch(context.Background(), in)
			if err != nil || r.State != cb.PendingAck {
				t.Error("duplicate changed receipt", r.State, err)
			}
		}()
	}
	wg.Wait()
	if x.sender.count() != 1 {
		t.Fatal("idempotent operation forwarded more than once")
	}
}

func TestCodexOwnedProofAndImmutableConnections(t *testing.T) {
	x := actorSetup(t)
	r := x.request("acquire", cb.NativeWriter, false)
	forged := r
	forged.PrincipalID = "public-ids-forger"
	_, err := x.a.Acquire(context.Background(), forged)
	assertConflict(t, err)
	g, err := x.a.Acquire(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	c := x.bind(t, g, "conn")
	altered := c
	altered.Writer.PrincipalID = "bob"
	x.verifier.ownConnection(altered)
	_, err = x.a.Bind(context.Background(), altered)
	assertConflict(t, err)
	altered = c
	altered.EndpointIncarnation = "different-endpoint"
	x.verifier.ownConnection(altered)
	_, err = x.a.Bind(context.Background(), altered)
	assertConflict(t, err)
	x.verifier.mu.Lock()
	x.verifier.fail = true
	x.verifier.mu.Unlock()
	_, err = x.a.Dispatch(context.Background(), actorIntent(c, "no-proof", "fixture/read"))
	assertConflict(t, err)
	if x.sender.count() != 0 || bytes.Contains([]byte(err.Error()), []byte("private-verifier-secret")) {
		t.Fatal("ownership bypass or secret exposure")
	}
	nilActor := x.e.service.CodexAuthority(nil, cb.Baseline{}, nil, nil, nil)
	_, err = nilActor.Acquire(context.Background(), r)
	assertConflict(t, err)
}

func TestCodexAdmissionAttemptSendRevokeAndCancellation(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.NativeWriter, false)
	c := x.bind(t, g, "conn")
	in := actorIntent(c, "send", "initialize")
	entered := make(chan struct{})
	x.sender.hook = func(ctx context.Context, _ domain.ID, _ []byte) error {
		r, err := x.e.store.CodexReceipt(context.Background(), in.OperationID)
		if err != nil || r.State != cb.Attempted {
			t.Error("write precedes durable attempt", err)
		}
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	sent := make(chan cb.Receipt, 1)
	failed := make(chan error, 1)
	go func() { r, err := x.a.Dispatch(ctx, in); sent <- r; failed <- err }()
	<-entered
	revoked := make(chan cb.Grant, 1)
	go func() {
		r, err := x.a.Revoke(context.Background(), g.WriterRef)
		if err != nil {
			t.Error(err)
		}
		revoked <- r
	}()
	select {
	case <-revoked:
		t.Fatal("revoke crossed an admitted write")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case r := <-sent:
		if r.State != cb.Uncertain {
			t.Fatal(r.State)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled write did not leave gate")
	}
	if err := <-failed; err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-revoked:
		if !r.Active || r.Admission != cb.Fenced {
			t.Fatal("revoke freed reservation")
		}
	case <-time.After(time.Second):
		t.Fatal("revoke blocked after send cancellation")
	}
	r, err := x.a.Dispatch(context.Background(), in)
	if err != nil || r.State != cb.Uncertain || x.sender.count() != 1 {
		t.Fatal("unknown effect replay", err)
	}
	_, err = x.a.Dispatch(context.Background(), actorIntent(c, "late", "fixture/read"))
	assertConflict(t, err)
	_, err = x.a.Handover(context.Background(), x.handover(g, "unsafe-next", cb.SemanticWriter))
	assertConflict(t, err)
}

func TestCodexPartialLostAckRestartNeverDuplicates(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost-ack", true: "partial-write"}[partial], func(t *testing.T) {
			x := actorSetup(t)
			g := x.acquire(t, "grant", cb.NativeWriter, false)
			c := x.bind(t, g, "conn")
			in := actorIntent(c, "send", "fixture/read")
			if partial {
				x.sender.hook = func(context.Context, domain.ID, []byte) error { return errors.New("secret-partial-write") }
			}
			r, err := x.a.Dispatch(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			want := cb.PendingAck
			if partial {
				want = cb.Uncertain
			}
			if r.State != want {
				t.Fatal(r.State)
			}
			// Simulate both a lost caller response and a daemon restart.
			x.e.restart(t)
			x.reset()
			r, err = x.a.Dispatch(context.Background(), in)
			if err != nil || r.State != cb.Uncertain || x.sender.count() != 1 {
				t.Fatal("restart resent unknown effect", r.State, err)
			}
			again, err := x.a.Acquire(context.Background(), x.request("grant", cb.NativeWriter, false))
			if err != nil || again.Lease != g.Lease || again.Admission != cb.Fenced || !again.Active {
				t.Fatal("acquisition retry promoted an old endpoint", err)
			}
			_, err = x.a.Bind(context.Background(), c)
			assertConflict(t, err)
			changed := in
			changed.Frame.Payload = payload(map[string]string{"threadId": "other"})
			changed.CanonicalHash = in.CanonicalHash
			_, err = x.a.Dispatch(context.Background(), changed)
			assertConflict(t, err)
			if x.sender.count() != 1 {
				t.Fatal("payload collision reached sender")
			}
		})
	}
}

func TestCodexOneShotZeroTurnAndCanonicalHash(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.NativeWriter, false)
	c := x.bind(t, g, "conn")
	in := actorIntent(c, "first", "initialize")
	in.CanonicalHash[0] = 99
	r, err := x.a.Dispatch(context.Background(), in)
	if err != nil || r.Intent.CanonicalHash == in.CanonicalHash {
		t.Fatal("caller hash trusted", err)
	}
	_, err = x.a.Dispatch(context.Background(), actorIntent(c, "second", "initialize"))
	assertConflict(t, err)
	_, err = x.a.Dispatch(context.Background(), actorIntent(c, "model", "turn/start"))
	assertConflict(t, err)
	_, err = x.a.Dispatch(context.Background(), actorIntent(c, "unknown", "unknown/mutator"))
	assertConflict(t, err)
	local, err := x.a.Dispatch(context.Background(), actorIntent(c, "unsubscribe", "fixture/unsubscribe"))
	if err != nil || local.State != cb.Completed || x.sender.count() != 1 {
		t.Fatal("viewer bookkeeping forwarded", err)
	}
}

func TestCodexHandoverFreshEpochAndHistoricalSettlement(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.NativeWriter, false)
	c := x.bind(t, g, "old-endpoint")
	r, err := x.a.Dispatch(context.Background(), actorIntent(c, "sent", "fixture/read"))
	if err != nil {
		t.Fatal(err)
	}
	bad := r
	bad.Intent.Frame.ID = json.RawMessage(`"123"`)
	bad.State = cb.Completed
	_, err = x.a.Settle(context.Background(), bad)
	assertConflict(t, err)
	bad = r
	bad.Intent.UpstreamChannelID = "other-channel"
	bad.State = cb.Completed
	_, err = x.a.Settle(context.Background(), bad)
	assertConflict(t, err)
	bad = r
	bad.Intent.Origin.Agent.AppServerBootID = "other-boot"
	bad.State = cb.Completed
	_, err = x.a.Settle(context.Background(), bad)
	assertConflict(t, err)
	r.State = cb.Acknowledged
	settled, err := x.a.Settle(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	h := x.handover(g, "handover", cb.SemanticWriter)
	next, err := x.a.Handover(context.Background(), h)
	if err != nil || next.Epoch <= g.Epoch || next.Lease.Token == g.Lease.Token || next.PrincipalID != g.PrincipalID {
		t.Fatal("bad epoch exchange", err)
	}
	retry, err := x.a.Handover(context.Background(), h)
	if err != nil || !identical(retry, next) {
		t.Fatal("handover retry changed epoch", err)
	}
	// Owning the same principal cannot rearm its former native process.
	fresh := c
	fresh.Writer = next.WriterRef
	fresh.Role = cb.TypedWriter
	x.verifier.ownConnection(fresh)
	_, err = x.a.Bind(context.Background(), fresh)
	assertConflict(t, err)
	_, err = x.a.Dispatch(context.Background(), actorIntent(c, "late-old", "fixture/read"))
	assertConflict(t, err)
	x.bind(t, next, "fresh-endpoint")
	// Historical receipt may be read/settled without projecting onto new owner.
	x.verifier.mu.Lock()
	delete(x.verifier.connections, string(payload(c))) // Original native UI died.
	x.verifier.mu.Unlock()
	settled.State = cb.Completed
	_, err = x.a.Settle(context.Background(), settled)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := x.e.store.CodexGrant(context.Background(), next.Lease.OperationID)
	if err != nil || !identical(current, next) {
		t.Fatal("historical receipt changed current owner", err)
	}
	old, _, err := x.e.store.CodexGrant(context.Background(), g.Lease.OperationID)
	if err != nil || old.Active || old.Admission != cb.Retired {
		t.Fatal("old row not retired", err)
	}
}

func TestCodexRepeatedNativeMetadataAndRequestIdentity(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.NativeWriter, false)
	c := x.bind(t, g, "conn")
	first := actorIntent(c, "account-one", "account/read")
	r, err := x.a.Dispatch(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	r.State = cb.Acknowledged
	if _, err = x.a.Settle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	second := actorIntent(c, "account-two", "account/read")
	second.Frame.ID = json.RawMessage("124")
	if _, err = x.a.Dispatch(context.Background(), second); err != nil {
		t.Fatal("safe metadata reload was made one-shot", err)
	}
	collision := actorIntent(c, "late-response-alias", "fixture/read")
	_, err = x.a.Dispatch(context.Background(), collision)
	assertConflict(t, err)
	moved := actorIntent(c, "moved-channel", "fixture/read")
	moved.Frame.ID = json.RawMessage("125")
	moved.UpstreamChannelID = "other-owned-channel"
	_, err = x.a.Dispatch(context.Background(), moved)
	assertConflict(t, err)
	if x.sender.count() != 2 {
		t.Fatal("reused ID/channel reached sender")
	}
}

func TestCodexNativeProcessAndEndpointNeverRearm(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.NativeWriter, false)
	old := x.bind(t, g, "old-connection")
	next, err := x.a.Handover(context.Background(), x.handover(g, "next-native", cb.NativeWriter))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"endpoint", "launch", "process"} {
		c := x.connection(next, domain.ID("new-"+kind))
		c.NativePID = 6000
		c.NativeProcessStart = "fresh-process"
		switch kind {
		case "endpoint":
			c.EndpointIncarnation = old.EndpointIncarnation
		case "launch":
			c.NativeLaunchID = old.NativeLaunchID
		case "process":
			c.NativePID = old.NativePID
			c.NativeProcessStart = old.NativeProcessStart
		}
		x.verifier.ownConnection(c)
		_, err = x.a.Bind(context.Background(), c)
		assertConflict(t, err)
	}
	fresh := x.connection(next, "fresh")
	fresh.NativePID = 6001
	fresh.NativeProcessStart = "fresh-process"
	x.verifier.ownConnection(fresh)
	if _, err = x.a.Bind(context.Background(), fresh); err != nil {
		t.Fatal("genuinely fresh native UI refused", err)
	}
}

func TestCodexUnknownCallbackTargetsAndWireBoundRemainBlocking(t *testing.T) {
	for _, shape := range []string{"unscoped", "foreign", "large-valid", "oversize", "malformed", "malformed-no-id"} {
		t.Run(shape, func(t *testing.T) {
			x := actorSetup(t)
			g := x.acquire(t, "grant", cb.NativeWriter, false)
			c := x.bind(t, g, "conn")
			key := cb.CallbackKey{UpstreamChannelID: "owned-channel", RequestID: json.RawMessage("7")}
			body := json.RawMessage(`{"method":"unknown/privileged","id":7,"params":{}}`)
			switch shape {
			case "foreign":
				key.ThreadID = "foreign-native-target"
			case "large-valid":
				body = payload(map[string]any{"method": "unknown/privileged", "id": 7, "params": map[string]string{"data": strings.Repeat("x", 96<<10)}})
			case "oversize":
				body = payload(map[string]string{"data": strings.Repeat("x", (1<<20)+1)})
			case "malformed":
				body = json.RawMessage("{unfinished")
			case "malformed-no-id":
				key.RequestID = nil
				body = json.RawMessage("{unfinished")
			}
			// Caller-supplied attribution must never make quarantine actionable.
			got, err := x.a.RecordCallback(context.Background(), cb.CallbackRecord{ID: "callback", Origin: c, Key: key, Payload: body, Attributed: true, Allowed: []cb.DecisionAction{cb.Accept}, Resolved: true, DecisionState: cb.Completed, DecisionOperationID: "forged-decision", DisplayConnectionID: c.ID})
			if err != nil || got.Resolved || got.Attributed || got.Key.ThreadID != key.ThreadID {
				t.Fatal("unknown callback disappeared or invented target", err)
			}
			if len(got.Allowed) != 0 || got.DecisionState != cb.Uncertain || got.DecisionOperationID != "" || got.DisplayConnectionID != "" || !bytes.Equal(got.Key.RequestID, key.RequestID) {
				t.Fatal("callback acquired a request ID or decision authority")
			}
			if shape == "malformed-no-id" && (!json.Valid(got.Payload) || !bytes.Contains(got.Payload, []byte("malformed owned callback retained as blocker"))) {
				t.Fatal("missing-ID malformed bytes were not retained as digest quarantine")
			}
			if _, err = x.a.RecordCallback(context.Background(), cb.CallbackRecord{ID: "callback", Origin: c, Key: key, Payload: body}); err != nil {
				t.Fatal("exact callback retry did not return its quarantine", err)
			}
			_, err = x.a.Handover(context.Background(), x.handover(g, "next", cb.SemanticWriter))
			assertConflict(t, err)
			x.e.restart(t)
			x.reset()
			_, err = x.a.Handover(context.Background(), x.handover(g, "next-after-restart", cb.SemanticWriter))
			assertConflict(t, err)
			if x.sender.count() != 0 {
				t.Fatal("quarantine sent an upstream decision")
			}
		})
	}
}

func TestCodexCallbackWaitingDuringProbeBlocksHandover(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.NativeWriter, false)
	c := x.bind(t, g, "conn")
	callbackDone := make(chan error, 1)
	x.probe.fn = func(agent cb.AgentBinding) (cb.Quiescence, error) {
		go func() {
			_, err := x.a.RecordCallback(context.Background(), cb.CallbackRecord{ID: "waiting-callback", Origin: c, Key: cb.CallbackKey{UpstreamChannelID: "owned-channel", RequestID: json.RawMessage("7")}, Payload: json.RawMessage(`{"method":"unknown/privileged"}`)})
			callbackDone <- err
		}()
		deadline := time.Now().Add(time.Second)
		for x.e.service.codexCallbacks.Load() == 0 {
			if time.Now().After(deadline) {
				return cb.Quiescence{}, fmt.Errorf("callback registration never announced")
			}
			time.Sleep(time.Millisecond)
		}
		return cb.Quiescence{Agent: agent, Idle: true, EmptyQueue: true, EmptyBackground: true, NativeWritersDetached: true, Source: "coherent-fixture", ObservedAt: time.Now()}, nil
	}
	_, err := x.a.Handover(context.Background(), x.handover(g, "next", cb.SemanticWriter))
	assertConflict(t, err)
	select {
	case err := <-callbackDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback failed to persist after probe")
	}
	old, _, err := x.e.store.CodexGrant(context.Background(), g.Lease.OperationID)
	if err != nil || !old.Active || old.Admission != cb.Fenced {
		t.Fatal("callback let handover overtake persistence", err)
	}
}

func TestCodexSenderDeadlineWithoutCallerCancellation(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.NativeWriter, false)
	c := x.bind(t, g, "conn")
	x.sender.hook = func(ctx context.Context, _ domain.ID, _ []byte) error { <-ctx.Done(); return ctx.Err() }
	start := time.Now()
	r, err := x.a.Dispatch(context.Background(), actorIntent(c, "bounded", "fixture/read"))
	if err != nil || r.State != cb.Uncertain || time.Since(start) > 3*time.Second {
		t.Fatal("sender was not bounded", err)
	}
	if _, err = x.a.Revoke(context.Background(), g.WriterRef); err != nil {
		t.Fatal("deadline did not release gate", err)
	}
}
func TestCodexRevokeUnavailablePrimaryStaysFencedOnReturn(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.NativeWriter, false)
	c := x.bind(t, g, "conn")
	x.verifier.mu.Lock()
	x.verifier.fail = true
	x.verifier.mu.Unlock()
	fenced, err := x.a.Revoke(context.Background(), g.WriterRef)
	if err != nil || !fenced.Active || fenced.Admission != cb.Fenced {
		t.Fatal("unavailable primary prevented durable fencing", err)
	}
	x.verifier.mu.Lock()
	x.verifier.fail = false
	x.verifier.mu.Unlock()
	_, err = x.a.Dispatch(context.Background(), actorIntent(c, "late-after-primary-return", "fixture/read"))
	assertConflict(t, err)
	if x.sender.count() != 0 {
		t.Fatal("resumed primary rearmed old UI")
	}
}
func TestCodexRevokeMissingOptionalDependenciesFencesEarlierActor(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.NativeWriter, false)
	c := x.bind(t, g, "conn")
	fallback := x.e.service.CodexAuthority(nil, cb.Baseline{}, nil, nil, nil)
	forged := g.WriterRef
	forged.Lease.Token = "public-identities-alone"
	_, err := fallback.Revoke(context.Background(), forged)
	assertConflict(t, err)
	fenced, err := fallback.Revoke(context.Background(), g.WriterRef)
	if err != nil || !fenced.Active || fenced.Admission != cb.Fenced {
		t.Fatal("missing optional dependencies prevented fencing", err)
	}
	_, err = x.a.Dispatch(context.Background(), actorIntent(c, "old-live-actor", "fixture/read"))
	assertConflict(t, err)
	if x.sender.count() != 0 {
		t.Fatal("earlier live actor bypassed common fencing")
	}
}

func TestCodexUnknownQuiescenceAndCallbacksBlock(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.NativeWriter, false)
	c := x.bind(t, g, "conn")
	h := x.handover(g, "next", cb.SemanticWriter)
	for _, bad := range []string{"unavailable", "stale", "wrong-agent", "queue", "background", "writer", "active"} {
		x.probe.fn = func(a cb.AgentBinding) (cb.Quiescence, error) {
			q := cb.Quiescence{Agent: a, Idle: true, EmptyQueue: true, EmptyBackground: true, NativeWritersDetached: true, Source: "owned-primary", ObservedAt: time.Now()}
			switch bad {
			case "unavailable":
				return q, errors.New("private-probe-secret")
			case "stale":
				q.ObservedAt = time.Now().Add(-time.Minute)
			case "wrong-agent":
				q.Agent.NativeThreadID = "other"
			case "queue":
				q.EmptyQueue = false
			case "background":
				q.EmptyBackground = false
			case "writer":
				q.NativeWritersDetached = false
			case "active":
				q.Idle = false
			}
			return q, nil
		}
		_, err := x.a.Handover(context.Background(), h)
		assertConflict(t, err)
	}
	x.probe.fn = nil
	callback := cb.CallbackRecord{ID: "callback", Key: cb.CallbackKey{UpstreamChannelID: "owned-channel", RequestID: json.RawMessage("7"), ThreadID: x.agent.NativeThreadID, ApprovalID: "offered-approval"}, Origin: c, Payload: json.RawMessage(`{"availableDecisions":["accept","decline"]}`), Allowed: []cb.DecisionAction{cb.Accept}, Attributed: true, Resolved: true}
	got, err := x.a.RecordCallback(context.Background(), callback)
	if err != nil || got.Attributed || got.Resolved || len(got.Allowed) != 0 || got.DecisionState != cb.Uncertain {
		t.Fatal("callback became actionable", err)
	}
	_, err = x.a.Decide(context.Background(), cb.Decision{OperationID: "answer", CallbackID: got.ID, Key: got.Key, Actor: c, Action: cb.Accept})
	assertConflict(t, err)
	_, err = x.a.Handover(context.Background(), h)
	assertConflict(t, err)
	if x.sender.count() != 0 {
		t.Fatal("callback automatically answered")
	}
	changed := callback
	changed.Key.RequestID = json.RawMessage(`"7"`)
	_, err = x.a.RecordCallback(context.Background(), changed)
	assertConflict(t, err)
}

func TestCodexProjectionLossNeverReleasesPrimary(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "grant", cb.SemanticWriter, false)
	x.e.backend.mu.Lock()
	delete(x.e.backend.refs, x.agent.ExecutionID)
	x.e.backend.mu.Unlock()
	l := g.Lease
	err := x.e.service.Release(context.Background(), protocol.LeaseRequest{SessionID: l.SessionID, ExecutionID: l.ExecutionID, Generation: l.Generation, OperationID: l.OperationID, ClientID: l.ClientID, Token: l.Token})
	assertConflict(t, err)
	err = x.e.service.Stop(context.Background(), protocol.SessionRequest{SessionID: l.SessionID, ExecutionID: l.ExecutionID, Generation: l.Generation, OperationID: "stop"})
	assertConflict(t, err)
	if err = x.e.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, _, err := x.e.store.CodexGrant(context.Background(), l.OperationID)
	if err != nil || !current.Active || current.Admission != cb.Fenced {
		t.Fatal("projection exit cleared primary", err)
	}
	v, err := x.e.store.Session(context.Background(), l.SessionID)
	if err != nil || v.Execution.ID != l.ExecutionID || v.Execution.Generation != l.Generation || v.Execution.State != "recovery_required" {
		t.Fatal("pane inferred primary lifecycle", v.Execution.State, err)
	}
	if _, stops := x.e.backend.counts(); stops != 0 {
		t.Fatal("native stop substituted for primary stop")
	}
	events, err := x.e.store.Events(context.Background(), v.ThreadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload(events), []byte(l.Token)) {
		t.Fatal("private lease leaked into journal")
	}
}

func TestCodexDeliverSerializedWithRevoke(t *testing.T) {
	x := actorSetup(t)
	g := x.acquire(t, "observer", cb.SemanticWriter, true)
	c := x.bind(t, g, "observer-conn")
	entered := make(chan struct{})
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		done <- x.a.Deliver(ctx, c, func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() })
	}()
	<-entered
	revoked := make(chan struct{})
	go func() {
		_, err := x.a.Revoke(context.Background(), g.WriterRef)
		if err != nil {
			t.Error(err)
		}
		close(revoked)
	}()
	select {
	case <-revoked:
		t.Fatal("revoke crossed final check and delivery")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("delivery failed to cancel")
	}
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("revoke did not complete")
	}
	called := false
	err := x.a.Deliver(context.Background(), c, func(context.Context) error { called = true; return nil })
	assertConflict(t, err)
	if called {
		t.Fatal("fenced delivery reached downstream")
	}
}

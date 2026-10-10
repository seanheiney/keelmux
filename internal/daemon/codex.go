package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"time"

	cb "github.com/seanheiney/agent-backplane/internal/codexbroker"
	"github.com/seanheiney/agent-backplane/internal/domain"
	"github.com/seanheiney/agent-backplane/internal/store"
)

const codexWriteLimit = 2 * time.Second
const codexEvidenceAge = 2 * time.Second

type codexAuthority struct {
	service  *Service
	policy   cb.Policy
	baseline cb.Baseline
	sender   cb.Sender
	probe    cb.QuiescenceProbe
	verifier cb.Verifier
}

// CodexAuthority is a home-internal actor, not an HTTP or native RPC endpoint.
// Its grants are the existing cooperating writer rows. No semantic capability
// or app-server lifecycle is advertised by constructing this actor.
func (s *Service) CodexAuthority(policy cb.Policy, baseline cb.Baseline, sender cb.Sender, probe cb.QuiescenceProbe, verifier cb.Verifier) cb.Authority {
	// Freeze caller-owned configuration; changing a map after construction must
	// not widen an admitted connection's bootstrap.
	b := cb.Baseline{ProtocolVersion: baseline.ProtocolVersion, Workspace: baseline.Workspace, WorkspaceAliases: append([]string(nil), baseline.WorkspaceAliases...), NativeBootstrap: map[string]json.RawMessage{}}
	for k, v := range baseline.NativeBootstrap {
		b.NativeBootstrap[k] = append(json.RawMessage(nil), v...)
	}
	return &codexAuthority{service: s, policy: policy, baseline: b, sender: sender, probe: probe, verifier: verifier}
}
func unavailable(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Interface, reflect.Func, reflect.Chan, reflect.Slice:
		return r.IsNil()
	}
	return false
}
func (a *codexAuthority) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.service == nil || a.service.store == nil || unavailable(a.policy) || unavailable(a.sender) || unavailable(a.probe) || unavailable(a.verifier) || a.baseline.ProtocolVersion == "" || a.baseline.Workspace == "" {
		return conflict("broker dependencies or verified baseline unavailable")
	}
	return nil
}
func (a *codexAuthority) noWaitingCallback() error {
	if a.service.codexCallbacks.Load() != 0 {
		return conflict("owned callback registration pending; mutation, delivery and handover fenced")
	}
	return nil
}
func validKind(k cb.WriterKind) bool { return k == cb.NativeWriter || k == cb.SemanticWriter }
func validAgent(v cb.AgentBinding) bool {
	return v.HostID != "" && v.SessionID != "" && v.ExecutionID != "" && v.Generation > 0 && v.AppServerIncarnation != "" && v.AppServerPID > 0 && v.AppServerBootID != "" && v.AppServerProcessStart != "" && v.ProtocolVersion != "" && v.NativeThreadID != ""
}
func identical(x, y any) bool { return bytes.Equal(payload(x), payload(y)) }
func (a *codexAuthority) verifyAgent(ctx context.Context, v cb.AgentBinding, current bool) error {
	if !validAgent(v) || v.HostID != a.service.hostID || v.ProtocolVersion != a.baseline.ProtocolVersion {
		return conflict("incomplete or mismatched primary binding")
	}
	// Verifier diagnostics can contain capabilities or paths. Never propagate.
	if err := a.verifier.VerifyAgent(ctx, v); err != nil {
		return conflict("owned primary verification unavailable or rejected")
	}
	if current {
		s, err := a.service.current(ctx, v.SessionID, v.ExecutionID, v.Generation)
		if err != nil {
			return err
		}
		if s.HostID != v.HostID || s.Mode != domain.Managed || s.Workspace != a.baseline.Workspace {
			return conflict("primary does not match the managed home baseline")
		}
	}
	return nil
}
func (a *codexAuthority) verifyWriter(ctx context.Context, r cb.WriterRequest) error {
	if r.OperationID == "" || r.PrincipalID == "" || r.ClientID == "" || !validKind(r.Kind) {
		return invalid("writer requires trusted principal, client, operation and kind")
	}
	if err := a.verifyAgent(ctx, r.Agent, true); err != nil {
		return err
	}
	if err := a.verifier.VerifyWriter(ctx, r); err != nil {
		return conflict("verified home writer request rejected")
	}
	return nil
}
func (a *codexAuthority) Acquire(ctx context.Context, r cb.WriterRequest) (cb.Grant, error) {
	s := a.service
	if err := a.ready(ctx); err != nil {
		return cb.Grant{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := a.noWaitingCallback(); err != nil {
		return cb.Grant{}, err
	}
	if err := a.verifyWriter(ctx, r); err != nil {
		return cb.Grant{}, err
	}
	return s.store.CodexAcquire(ctx, r)
}
func (a *codexAuthority) verifyConnection(ctx context.Context, c cb.ConnectionBinding, live bool) (cb.Grant, error) {
	if c.ID == "" || c.NativeLaunchID == "" || c.EndpointIncarnation == "" {
		return cb.Grant{}, conflict("owned connection incarnation required")
	}
	if c.Role != cb.NativeLocal && c.Role != cb.TypedObserver && c.Role != cb.TypedWriter {
		return cb.Grant{}, conflict("unsupported connection role")
	}
	if c.Role == cb.NativeLocal && (c.NativePID <= 0 || c.NativeProcessStart == "") {
		return cb.Grant{}, conflict("native projection process lifetime required")
	}
	if err := a.verifyAgent(ctx, c.Agent, live); err != nil {
		return cb.Grant{}, err
	}
	// Initial Bind proved per-launch ownership. Historical native receipts keep
	// that persisted identity after the disposable UI exits; they do not need
	// to manufacture a live native process to settle the exact primary result.
	if live {
		if err := a.verifier.VerifyConnection(ctx, c); err != nil {
			return cb.Grant{}, conflict("owned connection verification unavailable or rejected")
		}
	}
	g, agent, err := a.service.store.CodexGrant(ctx, c.Writer.Lease.OperationID)
	if err != nil {
		return g, err
	}
	if !identical(g.WriterRef, c.Writer) || !identical(agent, c.Agent) {
		return g, conflict("connection differs from original grant or primary")
	}
	if live && (!g.Active || g.Admission != cb.Admitting) {
		return g, conflict("connection writer is fenced or retired")
	}
	if c.Role == cb.NativeLocal && (g.ReadOnly || g.Kind != cb.NativeWriter) || c.Role == cb.TypedWriter && (g.ReadOnly || g.Kind != cb.SemanticWriter) {
		return g, conflict("connection role differs from original writer kind")
	}
	return g, nil
}
func (a *codexAuthority) storedConnection(ctx context.Context, c cb.ConnectionBinding, live bool) (cb.Grant, error) {
	old, retired, err := a.service.store.CodexConnection(ctx, c.ID)
	if err != nil {
		return cb.Grant{}, err
	}
	if !identical(old, c) || live && retired {
		return cb.Grant{}, conflict("old connection never rearms or changes provenance")
	}
	return a.verifyConnection(ctx, c, live)
}
func (a *codexAuthority) Bind(ctx context.Context, c cb.ConnectionBinding) (cb.ConnectionBinding, error) {
	if err := a.ready(ctx); err != nil {
		return c, err
	}
	a.service.mu.Lock()
	defer a.service.mu.Unlock()
	if err := a.noWaitingCallback(); err != nil {
		return c, err
	}
	if _, err := a.verifyConnection(ctx, c, true); err != nil {
		return c, err
	}
	return a.service.store.CodexBind(ctx, c)
}

func canonicalJSON(b json.RawMessage, optional bool) (json.RawMessage, error) {
	if len(b) == 0 && optional {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, invalid("invalid bounded JSON")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, invalid("JSON must contain exactly one value")
	}
	out, err := json.Marshal(v)
	return out, err
}
func canonicalID(b json.RawMessage, optional bool) (json.RawMessage, error) {
	v, err := canonicalJSON(b, optional)
	if err != nil || len(v) == 0 {
		return v, err
	}
	var x any
	d := json.NewDecoder(bytes.NewReader(v))
	d.UseNumber()
	_ = d.Decode(&x)
	switch n := x.(type) {
	case string:
		return v, nil
	case json.Number:
		if _, err = n.Int64(); err == nil {
			return v, nil
		}
	}
	return nil, invalid("request ID must be a string or bounded integer")
}
func encodeFrame(f cb.Frame) ([]byte, error) {
	if f.Kind != cb.Request && f.Kind != cb.Notification {
		return nil, conflict("raw results and errors cannot bypass the decision gate")
	}
	m := map[string]any{"jsonrpc": "2.0", "method": f.Method}
	if f.Kind == cb.Request {
		m["id"] = f.ID
	} else if len(f.ID) != 0 {
		return nil, invalid("notification cannot carry a request ID")
	}
	if len(f.Payload) != 0 {
		m["params"] = f.Payload
	}
	b, err := json.Marshal(m)
	if err != nil || len(b) > 64<<10 {
		return nil, invalid("broker frame exceeds bounded envelope")
	}
	return b, nil
}
func (a *codexAuthority) normalize(in cb.Intent) (cb.Intent, []byte, error) {
	if in.OperationID == "" || in.UpstreamChannelID == "" || in.Frame.Method == "" {
		return in, nil, invalid("intent requires operation, owned channel and method")
	}
	var err error
	in.Frame.ID, err = canonicalID(in.Frame.ID, in.Frame.Kind == cb.Notification)
	if err != nil {
		return in, nil, err
	}
	in.DownstreamRequestID, err = canonicalID(in.DownstreamRequestID, in.Frame.Kind == cb.Notification)
	if err != nil {
		return in, nil, err
	}
	if in.Frame.Kind == cb.Notification && len(in.DownstreamRequestID) != 0 {
		return in, nil, invalid("notification cannot carry downstream request identity")
	}
	// Decode the original nested payload before canonicalizing: policy decoding
	// must see and reject duplicate keys rather than silently collapse them.
	wire, err := encodeFrame(in.Frame)
	if err != nil {
		return in, nil, err
	}
	f, err := a.policy.Decode(wire)
	if err != nil {
		return in, nil, conflict("broker policy rejected the frame envelope")
	}
	in.Frame.Payload, err = canonicalJSON(in.Frame.Payload, true)
	if err != nil {
		return in, nil, err
	}
	f.ID, err = canonicalID(f.ID, f.Kind == cb.Notification)
	if err != nil {
		return in, nil, err
	}
	f.Payload, err = canonicalJSON(f.Payload, true)
	if err != nil {
		return in, nil, err
	}
	if !identical(f, in.Frame) {
		return in, nil, conflict("decoded frame differs from typed intent")
	}
	// Only transport correlation IDs are excluded; the exact target, class,
	// native method/payload and client message identity participate in hashing.
	in.CanonicalHash = sha256.Sum256(payload(struct {
		Agent               cb.AgentBinding
		Kind                cb.FrameKind
		Method              string
		Payload             json.RawMessage
		ClientUserMessageID string
	}{in.Origin.Agent, in.Frame.Kind, in.Frame.Method, in.Frame.Payload, in.ClientUserMessageID}))
	wire, err = encodeFrame(in.Frame)
	return in, wire, err
}
func (a *codexAuthority) Dispatch(ctx context.Context, in cb.Intent) (cb.Receipt, error) {
	if err := a.ready(ctx); err != nil {
		return cb.Receipt{}, err
	}
	a.service.mu.Lock()
	defer a.service.mu.Unlock()
	in, wire, err := a.normalize(in)
	if err != nil {
		return cb.Receipt{}, err
	}
	// Known original operations can be read after fencing, without forwarding.
	if old, e := a.service.store.CodexReceipt(ctx, in.OperationID); e == nil {
		if !identical(old.Intent, in) {
			return old, conflict("operation ID differs in original provenance, channel or payload")
		}
		if _, e = a.storedConnection(ctx, in.Origin, false); e != nil {
			return old, e
		}
		return old, nil
	} else if !errors.Is(e, store.ErrNotFound) {
		return cb.Receipt{}, e
	}
	g, err := a.storedConnection(ctx, in.Origin, true)
	if err != nil {
		return cb.Receipt{}, err
	}
	if err = a.noWaitingCallback(); err != nil {
		return cb.Receipt{}, err
	}
	class, err := a.policy.Classify(in.Origin, in.Frame, a.baseline)
	if err != nil {
		return cb.Receipt{}, conflict("broker policy rejected method, scope or parameters")
	}
	switch class {
	case cb.NativeBootstrap:
		if in.Origin.Role != cb.NativeLocal || g.ReadOnly || g.Kind != cb.NativeWriter {
			return cb.Receipt{}, conflict("bootstrap requires original native writer")
		}
	case cb.ScopedRead, cb.ViewerLocal:
	case cb.Mutation:
		return cb.Receipt{}, conflict("model work and semantic mutations are unsupported by the zero-turn profile")
	default:
		return cb.Receipt{}, conflict("unknown broker effects are denied")
	}
	if err = ctx.Err(); err != nil {
		return cb.Receipt{}, err
	}
	r, claimed, err := a.service.store.CodexAttempt(ctx, in, class)
	if err != nil || !claimed {
		return r, err
	}
	if class == cb.ViewerLocal {
		r.State = cb.Completed
		r.Detail = "local viewer bookkeeping; authoritative anchor unchanged"
	} else {
		// Keep the common gate until the bounded write returns. This waits for
		// no acknowledgement or model turn, and holds no database transaction.
		wc, cancel := context.WithTimeout(ctx, codexWriteLimit)
		err = a.sender.WriteFrame(wc, in.UpstreamChannelID, wire)
		cancel()
		r.State = cb.PendingAck
		r.Detail = "frame written; exact native acknowledgement pending"
		if err != nil {
			r.State = cb.Uncertain
			r.Detail = "upstream write invoked without certain acknowledgement; no replay"
		}
	}
	dc, cancel := durableContext()
	defer cancel()
	out, saveErr := a.service.store.CodexSettle(dc, r)
	if saveErr != nil {
		return r, saveErr
	}
	// The uncertain receipt, rather than an unsafe retry instruction or secret
	// transport diagnostic, is the outcome of an invoked sender error.
	return out, nil
}
func (a *codexAuthority) Settle(ctx context.Context, r cb.Receipt) (cb.Receipt, error) {
	if err := a.ready(ctx); err != nil {
		return cb.Receipt{}, err
	}
	a.service.mu.Lock()
	defer a.service.mu.Unlock()
	in, _, err := a.normalize(r.Intent)
	if err != nil {
		return cb.Receipt{}, err
	}
	r.Intent = in
	if _, err = a.storedConnection(ctx, r.Intent.Origin, false); err != nil {
		return cb.Receipt{}, err
	}
	if r.State != cb.Acknowledged && r.State != cb.Completed && r.State != cb.Failed && r.State != cb.Uncertain {
		return cb.Receipt{}, conflict("unsupported native settlement state")
	}
	if r.NativeTurnID != "" || r.NativeItemID != "" {
		return cb.Receipt{}, conflict("zero-turn profile cannot settle model work")
	}
	r.Detail = "trusted exact native receipt; no model acceptance or current-owner projection"
	return a.service.store.CodexSettle(ctx, r)
}
func (a *codexAuthority) Revoke(ctx context.Context, ref cb.WriterRef) (cb.Grant, error) {
	if err := ctx.Err(); err != nil {
		return cb.Grant{}, err
	}
	if a.service == nil || a.service.store == nil {
		return cb.Grant{}, conflict("home authority store unavailable")
	}
	a.service.mu.Lock()
	defer a.service.mu.Unlock()
	g, _, err := a.service.store.CodexGrant(ctx, ref.Lease.OperationID)
	if err != nil {
		return g, err
	}
	if !identical(g.WriterRef, ref) {
		return g, conflict("revoke requires exact original writer")
	}
	// Fencing needs the original private capability, not a responsive primary.
	// It admits no new effect and cannot release the slot. A suspended primary
	// must not revive a connection whose owner already revoked its grant.
	return a.service.store.CodexFence(ctx, ref)
}
func coherent(q cb.Quiescence, a cb.AgentBinding) bool {
	age := time.Since(q.ObservedAt)
	return identical(q.Agent, a) && q.Source != "" && !q.ObservedAt.IsZero() && age >= -100*time.Millisecond && age <= codexEvidenceAge
}
func (a *codexAuthority) Handover(ctx context.Context, h cb.Handover) (cb.Grant, error) {
	if err := a.ready(ctx); err != nil {
		return cb.Grant{}, err
	}
	a.service.mu.Lock()
	defer a.service.mu.Unlock()
	if h.OperationID == "" || h.NextPrincipalID == "" || h.NextClientID == "" || !validKind(h.NextKind) {
		return cb.Grant{}, invalid("handover requires operation and verified next writer")
	}
	g, agent, err := a.service.store.CodexGrant(ctx, h.Previous.Lease.OperationID)
	if err != nil {
		return g, err
	}
	if !identical(g.WriterRef, h.Previous) {
		return g, conflict("handover requires exact original writer")
	}
	next := cb.WriterRequest{Agent: agent, OperationID: h.OperationID, PrincipalID: h.NextPrincipalID, ClientID: h.NextClientID, Kind: h.NextKind}
	if err = a.verifyWriter(ctx, next); err != nil {
		return g, err
	}
	if op, p, e := a.service.store.Operation(ctx, h.OperationID); e == nil {
		if op.Kind != "codex.handover" || !bytes.Equal(p, payload(h)) {
			return g, conflict("handover operation ID collision")
		}
		out, _, e := a.service.store.CodexGrant(ctx, h.OperationID)
		return out, e
	} else if !errors.Is(e, store.ErrNotFound) {
		return g, e
	}
	if !g.Active || g.ReadOnly {
		return g, conflict("original writer is not reserved")
	}
	blocked, err := a.service.store.CodexBlocked(ctx, agent)
	if err != nil {
		return g, err
	}
	if blocked {
		return g, conflict("pending sends or privileged callbacks block handover")
	}
	pc, cancel := context.WithTimeout(ctx, codexWriteLimit)
	q, err := a.probe.Inspect(pc, agent)
	cancel()
	if err != nil || !coherent(q, agent) || !q.Idle || !q.EmptyQueue || !q.EmptyBackground || !q.NativeWritersDetached {
		return g, conflict("coherent idle, queue, background and detached-writer evidence required")
	}
	// No registration may slip between the final check and durable retirement.
	// Readers behind this barrier linearize after the handover, then quarantine
	// their original source and fence the replacement under the same actor gate.
	a.service.codexRegistration.Lock()
	defer a.service.codexRegistration.Unlock()
	if err = a.noWaitingCallback(); err != nil {
		return g, err
	}
	if err = ctx.Err(); err != nil {
		return g, err
	}
	return a.service.store.CodexHandover(ctx, h)
}
func (a *codexAuthority) Observe(ctx context.Context, c cb.ConnectionBinding) (cb.Observation, error) {
	if err := a.ready(ctx); err != nil {
		return cb.Observation{}, err
	}
	a.service.mu.Lock()
	defer a.service.mu.Unlock()
	if err := a.noWaitingCallback(); err != nil {
		return cb.Observation{}, err
	}
	if _, err := a.storedConnection(ctx, c, true); err != nil {
		return cb.Observation{}, err
	}
	pc, cancel := context.WithTimeout(ctx, codexWriteLimit)
	defer cancel()
	q, err := a.probe.Inspect(pc, c.Agent)
	if err != nil || !coherent(q, c.Agent) {
		return cb.Observation{}, conflict("owned primary observation unavailable")
	}
	status := "active"
	if q.Idle && q.EmptyQueue && q.EmptyBackground {
		status = "idle"
	}
	return cb.Observation{SessionID: c.Agent.SessionID, ExecutionID: c.Agent.ExecutionID, Generation: c.Agent.Generation, NativeThreadID: c.Agent.NativeThreadID, Status: status, Source: q.Source, ObservedAt: q.ObservedAt}, nil
}

// Deliver makes the final scope check, projection and owned downstream write
// indivisible with revoke/handover. The caller owns only this bounded closure.
func (a *codexAuthority) Deliver(ctx context.Context, c cb.ConnectionBinding, write func(context.Context) error) error {
	if err := a.ready(ctx); err != nil {
		return err
	}
	if write == nil {
		return invalid("owned delivery writer required")
	}
	a.service.mu.Lock()
	defer a.service.mu.Unlock()
	if err := a.noWaitingCallback(); err != nil {
		return err
	}
	if _, err := a.storedConnection(ctx, c, true); err != nil {
		return err
	}
	wc, cancel := context.WithTimeout(ctx, codexWriteLimit)
	defer cancel()
	if err := write(wc); err != nil {
		return conflict("owned downstream delivery failed or cancelled")
	}
	return nil
}
func (a *codexAuthority) RecordCallback(ctx context.Context, c cb.CallbackRecord) (cb.CallbackRecord, error) {
	if err := a.ready(ctx); err != nil {
		return cb.CallbackRecord{}, err
	}
	a.service.codexRegistration.Lock()
	a.service.codexCallbacks.Add(1)
	a.service.codexRegistration.Unlock()
	defer a.service.codexCallbacks.Add(-1)
	a.service.mu.Lock()
	defer a.service.mu.Unlock()
	if c.ID == "" || c.Key.UpstreamChannelID == "" {
		return c, conflict("callback requires the original owned source channel")
	}
	if _, err := a.storedConnection(ctx, c.Origin, false); err != nil {
		return c, err
	}
	var err error
	// A malformed owned frame may have no native request ID. Keep that absence
	// as private quarantine evidence; never fabricate an ID or approval target.
	// Every record in this profile is forced non-actionable below and in store.
	c.Key.RequestID, err = canonicalID(c.Key.RequestID, true)
	if err != nil {
		return c, err
	}
	// Unknown, unscoped and foreign targets must remain visibly blocking; never
	// relabel them as belonging to the granted thread. No callback is actionable.
	if len(c.Payload) > 1<<20 {
		digest := sha256.Sum256(c.Payload)
		c.Payload = payload(struct {
			Quarantine string
			Bytes      int
			SHA256     [32]byte
		}{"oversize owned callback retained as blocker", len(c.Payload), digest})
	} else if checked, e := canonicalJSON(c.Payload, false); e == nil {
		c.Payload = checked
	} else {
		digest := sha256.Sum256(c.Payload)
		c.Payload = payload(struct {
			Quarantine string
			Bytes      int
			SHA256     [32]byte
		}{"malformed owned callback retained as blocker", len(c.Payload), digest})
	}
	if c.OriginOperationID != "" {
		r, e := a.service.store.CodexReceipt(ctx, c.OriginOperationID)
		if e != nil || !identical(r.Intent.Origin, c.Origin) || r.Intent.UpstreamChannelID != c.Key.UpstreamChannelID {
			return c, conflict("callback differs from original owned operation/channel")
		}
	}
	c.CanonicalHash = sha256.Sum256(payload(struct {
		Origin    cb.ConnectionBinding
		Operation domain.ID
		Key       cb.CallbackKey
		Payload   json.RawMessage
	}{c.Origin, c.OriginOperationID, c.Key, c.Payload}))
	// This profile never turns a native callback into approval authority.
	c.Attributed = false
	c.Allowed = nil
	c.DecisionState = cb.Uncertain
	c.Resolved = false
	c.DisplayConnectionID = ""
	c.DecisionOperationID = ""
	return a.service.store.CodexCallback(ctx, c)
}
func (a *codexAuthority) Decide(ctx context.Context, _ cb.Decision) (cb.Receipt, error) {
	if err := a.ready(ctx); err != nil {
		return cb.Receipt{}, err
	}
	return cb.Receipt{}, conflict("approval decisions are unsupported; callback requires explicit repair")
}

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"os"
	"path/filepath"
	"strings"

	cc "github.com/seanheiney/keelmux/internal/codexcontroller"
	"github.com/seanheiney/keelmux/internal/domain"
	bpruntime "github.com/seanheiney/keelmux/internal/runtime"
	"github.com/seanheiney/keelmux/internal/store"
)

// PrimarySelection is a trusted host-local explicit selection. It is deliberately
// absent from HTTP/native RPC: public IDs and model-supplied realm labels cannot
// authorize a primary. The configured realm must match NewCodexPrimary's realm.
type PrimarySelection struct {
	OperationID                                    domain.ID
	Name, Workspace, NativeThreadID, RealmRevision string
}

type CodexPrimary struct {
	service    *Service
	controller *cc.Controller
	realm      string
}

type PrimaryCapabilities struct {
	OwnedPrimaryVerified                                               bool
	NativeProjection, SemanticPrompt, RemoteApprovals, AccountIdentity bool
	Explanation                                                        string
}
type PrimaryStatus struct {
	Primary      cc.View
	Capabilities PrimaryCapabilities
}

// NewCodexPrimary composes the existing durable Service authority with the
// primary controller. It neither launches nor advertises product capabilities.
func (s *Service) NewCodexPrimary(j *cc.Journal, supervisor cc.Supervisor, upstream cc.Upstream, options cc.Options) (*CodexPrimary, error) {
	if s == nil || s.store == nil || unavailable(supervisor) {
		return nil, cc.ErrUnavailable
	}
	c, err := cc.New(j, &primarySupervisorGate{service: s, supervisor: supervisor}, upstream, &primaryGate{service: s, realm: options.RealmRevision}, options)
	if err != nil {
		return nil, err
	}
	return &CodexPrimary{service: s, controller: c, realm: options.RealmRevision}, nil
}

// PrepareCodexPrimary persists selection before any external effect. A retry
// returns the original stable target; another operation creates a separate lane.
func (s *Service) PrepareCodexPrimary(ctx context.Context, in PrimarySelection) (cc.StartRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.OperationID == "" || strings.TrimSpace(in.Name) == "" || in.NativeThreadID == "" || in.RealmRevision == "" || !filepath.IsAbs(in.Workspace) || filepath.Clean(in.Workspace) != in.Workspace || strings.ContainsAny(in.Workspace, "\x00\r\n") {
		return cc.StartRequest{}, invalid("primary requires explicit operation, name, workspace, native thread and realm selection")
	}
	if op, raw, err := s.store.Operation(ctx, in.OperationID); err == nil {
		var old store.PrimaryReservation
		if op.Kind != "codex.primary.reserve" || json.Unmarshal(raw, &old) != nil || old.Name != in.Name || old.Request.Workspace != in.Workspace || old.Request.NativeThreadID != in.NativeThreadID || old.Request.RealmRevision != in.RealmRevision {
			return cc.StartRequest{}, conflict("operation ID names another primary selection")
		}
		return old.Request, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return cc.StartRequest{}, err
	}
	if info, err := os.Stat(in.Workspace); err != nil || !info.IsDir() {
		return cc.StartRequest{}, invalid("primary workspace unavailable")
	}
	// This profile has no replacement transition. Even after stop, a second
	// reservation for the same native thread needs an explicit future migration.
	operations, err := s.store.Operations(ctx)
	if err != nil {
		return cc.StartRequest{}, err
	}
	for _, op := range operations {
		if op.Kind != "codex.primary.reserve" {
			continue
		}
		_, raw, err := s.store.Operation(ctx, op.ID)
		var old store.PrimaryReservation
		if err != nil || json.Unmarshal(raw, &old) != nil {
			return cc.StartRequest{}, cc.ErrStorage
		}
		if old.Request.NativeThreadID == in.NativeThreadID {
			return cc.StartRequest{}, conflict("native thread already reserved; replacement transition unavailable")
		}
	}
	v := domain.NewSession(s.hostID, in.Name, in.Workspace, "codex", cc.ProtocolVersion)
	v.Capabilities = domain.Capabilities{Explanation: "Experimental owned primary reservation. Native projection, semantic prompt delivery and remote approvals are unavailable."}
	v.Execution.State = "primary_reserved"
	req := cc.StartRequest{OperationID: in.OperationID, Target: cc.Target{HostID: v.HostID, SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation}, Workspace: in.Workspace, NativeThreadID: in.NativeThreadID, RealmRevision: in.RealmRevision}
	return req, s.store.ReserveCodexPrimary(ctx, v, store.PrimaryReservation{Name: in.Name, Request: req})
}

type primaryGate struct {
	service *Service
	realm   string
}

// Lock order is common Service gate -> store/journal. The durable stop fence is
// committed before the callback and survives restart, errors and lost replies.
func (g *primaryGate) WithPrimaryLifecycle(ctx context.Context, req cc.LifecycleRequest, effect func(context.Context) error) error {
	if g == nil || g.service == nil || g.service.store == nil || effect == nil {
		return cc.ErrUnavailable
	}
	if deadline, ok := ctx.Deadline(); !ok || deadline.IsZero() {
		return cc.ErrInvalid
	}
	s := g.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.codexCallbacks.Load() != 0 {
		return conflict("callback persistence pending")
	}
	r, err := s.store.CodexPrimaryReservation(ctx, req.Target.SessionID, req.Target.ExecutionID)
	if err != nil {
		return err
	}
	v, err := s.current(ctx, req.Target.SessionID, req.Target.ExecutionID, req.Target.Generation)
	if err != nil {
		return err
	}
	if v.HostID != s.hostID || req.Target != r.Request.Target || v.Mode != domain.Managed || v.Execution.Provider != "codex" || v.Workspace != req.Workspace || req.Workspace != r.Request.Workspace || req.NativeThreadID != r.Request.NativeThreadID || req.RealmRevision != r.Request.RealmRevision || req.RealmRevision != g.realm {
		return conflict("primary differs from durable home selection")
	}
	switch req.Action {
	case cc.StartPrimary:
		if req.OperationID != r.Request.OperationID || req.Agent != (cb.AgentBinding{}) {
			return conflict("primary start operation differs from reservation")
		}
		claimed, err := s.store.ClaimCodexPrimaryStart(ctx, req)
		if err != nil {
			return err
		}
		if !claimed {
			return cc.ErrUncertain // An existing claim never authorizes a second effect.
		}
	case cc.StopPrimary:
		s.codexRegistration.Lock()
		if s.codexCallbacks.Load() != 0 {
			s.codexRegistration.Unlock()
			return conflict("callback persistence pending")
		}
		err = s.store.FenceCodexPrimaryStop(ctx, req)
		s.codexRegistration.Unlock()
		if err != nil {
			return err
		}
	default:
		return cc.ErrInvalid
	}
	return effect(ctx)
}

func primaryStatus(v cc.View) PrimaryStatus {
	return PrimaryStatus{Primary: v, Capabilities: PrimaryCapabilities{OwnedPrimaryVerified: v.State == cc.Ready, Explanation: "Host-internal primary lifecycle only. Product launcher, native projection, semantic delivery, remote approval and account identity are unqualified."}}
}
func primaryResult(v cc.View, err error) (PrimaryStatus, error) {
	out := primaryStatus(v)
	if err != nil {
		out.Capabilities.OwnedPrimaryVerified = false
	}
	return out, err
}
func (p *CodexPrimary) Start(ctx context.Context, request cc.StartRequest) (PrimaryStatus, error) {
	v, err := p.controller.Start(ctx, request)
	if err != nil || v.State != cc.Ready {
		return primaryResult(v, err)
	}
	// Re-enter the common authority before verifying and publishing. Never hold
	// the controller journal while waiting for Service.mu.
	p.service.mu.Lock()
	defer p.service.mu.Unlock()
	a, err := p.controller.Binding(ctx, request.Target)
	if err == nil {
		err = p.service.store.PublishCodexPrimary(ctx, a)
	}
	return primaryResult(v, err)
}
func (p *CodexPrimary) Reconcile(ctx context.Context, target cc.Target) (PrimaryStatus, error) {
	p.service.mu.Lock()
	defer p.service.mu.Unlock()
	current, err := p.service.current(ctx, target.SessionID, target.ExecutionID, target.Generation)
	if err != nil || current.HostID != target.HostID {
		return PrimaryStatus{}, cc.ErrConflict
	}
	v, err := p.controller.Reconcile(ctx, target.ExecutionID)
	if err == nil && v.State == cc.Ready {
		var aErr error
		a, aErr := p.controller.Binding(ctx, target)
		if aErr == nil {
			aErr = p.service.store.PublishCodexPrimary(ctx, a)
		}
		err = aErr
	}
	if err == nil && v.State == cc.Stopped {
		err = p.service.store.CompleteCodexPrimaryStop(ctx, cc.StopRequest{OperationID: v.StopOperationID, Target: v.Target, Incarnation: v.Incarnation})
	}
	return primaryResult(v, err)
}
func (p *CodexPrimary) Stop(ctx context.Context, request cc.StopRequest) (PrimaryStatus, error) {
	v, err := p.controller.Stop(ctx, request)
	if err == nil && v.State == cc.Stopped {
		p.service.mu.Lock()
		err = p.service.store.CompleteCodexPrimaryStop(ctx, request)
		p.service.mu.Unlock()
	}

	return primaryResult(v, err)
}

// The Service mutex excludes new admitted work; this final reader barrier
// prevents an already announced callback from being overtaken between the
// controller's native quiescence check and the bounded lifecycle effect. Reader
// registration never holds this barrier while acquiring Service.mu.
type primarySupervisorGate struct {
	service    *Service
	supervisor cc.Supervisor
}

func (g *primarySupervisorGate) effect(ctx context.Context, run func() error) error {
	g.service.codexRegistration.Lock()
	defer g.service.codexRegistration.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if g.service.codexCallbacks.Load() != 0 {
		return cc.ErrUnavailable
	}
	return run()
}
func (g *primarySupervisorGate) LaunchProcess(ctx context.Context, spec domain.LaunchSpec) (domain.RuntimeRef, error) {
	var ref domain.RuntimeRef
	err := g.effect(ctx, func() error { var err error; ref, err = g.supervisor.LaunchProcess(ctx, spec); return err })
	return ref, err
}
func (g *primarySupervisorGate) Stop(ctx context.Context, ref domain.RuntimeRef) error {
	return g.effect(ctx, func() error { return g.supervisor.Stop(ctx, ref) })
}
func (g *primarySupervisorGate) Lookup(ctx context.Context, e, o domain.ID) (bpruntime.Observation, error) {
	return g.supervisor.Lookup(ctx, e, o)
}
func (g *primarySupervisorGate) Inspect(ctx context.Context, ref domain.RuntimeRef) (bpruntime.Observation, error) {
	return g.supervisor.Inspect(ctx, ref)
}

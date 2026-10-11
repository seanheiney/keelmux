package daemon

import (
	"context"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	cc "github.com/seanheiney/keelmux/internal/codexcontroller"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
)

// ExperimentalStdioPrimary is a host-internal integration boundary. There is
// no HTTP, CLI, inventory capability or native writer route to this controller.
// Fixture protocol success does not qualify a native provider or confinement.
type ExperimentalStdioPrimary struct{ controller *cc.StdioController }

type StdioPrimaryStatus struct {
	Primary                 cc.View
	Transport               string
	ChannelVerified         bool
	NativeProviderQualified bool
	Explanation             string
}

func (s *Service) NewExperimentalStdioPrimary(j *cc.Journal, supervisor cc.StdioSupervisor, options cc.StdioOptions) (*ExperimentalStdioPrimary, error) {
	if s == nil || s.store == nil || unavailable(supervisor) {
		return nil, cc.ErrUnavailable
	}
	gate := &primaryStdioGate{primaryGate: primaryGate{service: s, realm: options.RealmRevision}}
	c, err := cc.NewStdio(j, &primaryStdioSupervisorGate{StdioSupervisor: supervisor, service: s}, &primaryStdioStore{service: s}, gate, options)
	if err != nil {
		return nil, err
	}
	return &ExperimentalStdioPrimary{controller: c}, nil
}

func stdioPrimaryResult(v cc.View, err error) (StdioPrimaryStatus, error) {
	return StdioPrimaryStatus{Primary: v, Transport: "supervisor_stdio", ChannelVerified: err == nil && v.State == cc.Ready, Explanation: "Experimental host-local channel evidence. Native provider, isolation, product launcher, writer, native projection, semantic delivery and remote approval remain unqualified."}, err
}
func (p *ExperimentalStdioPrimary) Start(ctx context.Context, r cc.StartRequest) (StdioPrimaryStatus, error) {
	v, err := p.controller.Start(ctx, r)
	return stdioPrimaryResult(v, err)
}
func (p *ExperimentalStdioPrimary) Reconcile(ctx context.Context, t cc.Target) (StdioPrimaryStatus, error) {
	v, err := p.controller.Reconcile(ctx, t)
	return stdioPrimaryResult(v, err)
}
func (p *ExperimentalStdioPrimary) Stop(ctx context.Context, r cc.StopRequest) (StdioPrimaryStatus, error) {
	v, err := p.controller.Stop(ctx, r)
	return stdioPrimaryResult(v, err)
}

type primaryStdioGate struct{ primaryGate }

// Read/import admission uses the same Service gate as lifecycle and broker
// admission. It never claims another start, reopens a writer or clears a stop.
func (g *primaryStdioGate) WithPrimaryStdioAdmission(ctx context.Context, b ps.Binding, effect func(context.Context) error) error {
	if g == nil || g.service == nil || g.service.store == nil || effect == nil || ps.ValidateBinding(b) != nil {
		return cc.ErrUnavailable
	}
	if _, ok := ctx.Deadline(); !ok {
		return cc.ErrInvalid
	}
	s := g.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.codexCallbacks.Load() != 0 {
		return cc.ErrUnavailable
	}
	r, err := s.store.CodexPrimaryReservation(ctx, b.SessionID, b.ExecutionID)
	if err != nil {
		return err
	}
	v, err := s.current(ctx, b.SessionID, b.ExecutionID, b.Generation)
	if err != nil {
		return err
	}
	if b.HostID != s.hostID || v.HostID != b.HostID || r.Start == nil || r.Start.Incarnation != b.PrimaryIncarnation || r.Request.OperationID != b.StartOperationID || r.Request.Target.HostID != b.HostID || r.Request.Target.SessionID != b.SessionID || r.Request.Target.ExecutionID != b.ExecutionID || r.Request.Target.Generation != b.Generation || r.Request.NativeThreadID != b.NativeThreadID || r.Request.RealmRevision != b.RealmRevision || b.RealmRevision != g.realm || v.Workspace != r.Request.Workspace {
		return cc.ErrConflict
	}
	return effect(ctx)
}

type primaryStdioStore struct{ service *Service }

type primaryStdioSupervisorGate struct {
	cc.StdioSupervisor
	service *Service
}

func (g *primaryStdioSupervisorGate) CheckStdioReady() error {
	if readiness, ok := g.StdioSupervisor.(interface{ CheckStdioReady() error }); ok {
		return readiness.CheckStdioReady()
	}
	return nil
}
func (g *primaryStdioSupervisorGate) LaunchStdio(ctx context.Context, spec ps.LaunchSpec) (ps.ChannelRef, error) {
	var ref ps.ChannelRef
	barrier := primarySupervisorGate{service: g.service}
	err := barrier.effect(ctx, func() error { var err error; ref, err = g.StdioSupervisor.LaunchStdio(ctx, spec); return err })
	return ref, err
}
func (g *primaryStdioSupervisorGate) BootstrapStdioOnce(ctx context.Context, ref ps.ChannelRef) (ps.Observation, error) {
	var ob ps.Observation
	barrier := primarySupervisorGate{service: g.service}
	err := barrier.effect(ctx, func() error { var err error; ob, err = g.StdioSupervisor.BootstrapStdioOnce(ctx, ref); return err })
	return ob, err
}
func (g *primaryStdioSupervisorGate) StopStdio(ctx context.Context, ref ps.ChannelRef, r ps.StopRequest) error {
	barrier := primarySupervisorGate{service: g.service}
	return barrier.effect(ctx, func() error { return g.StdioSupervisor.StopStdio(ctx, ref, r) })
}

func (s *primaryStdioStore) BindCodexPrimaryStdio(ctx context.Context, c ps.Channel) (ps.Ledger, error) {
	return s.service.store.BindCodexPrimaryStdio(ctx, c)
}
func (s *primaryStdioStore) ImportCodexPrimaryStdio(ctx context.Context, b ps.ImportBatch) (ps.ImportReceipt, error) {
	return s.service.store.ImportCodexPrimaryStdio(ctx, b)
}
func (s *primaryStdioStore) CodexPrimaryStdio(ctx context.Context, b ps.Binding) (ps.Ledger, error) {
	return s.service.store.CodexPrimaryStdio(ctx, b)
}
func (s *primaryStdioStore) CompleteCodexPrimaryStop(ctx context.Context, r cc.StopRequest) error {
	return s.service.store.CompleteCodexPrimaryStop(ctx, r)
}
func (s *primaryStdioStore) InvalidateCodexPrimaryStdio(ctx context.Context, b ps.Binding) error {
	return s.service.store.InvalidateCodexPrimaryStdio(ctx, b)
}
func (s *primaryStdioStore) PublishCodexPrimary(ctx context.Context, a cb.AgentBinding) error {
	// Native helper receive registration is held by WithStdioQuiescence here.
	// This shorter barrier orders existing broker callback registration too.
	s.service.codexRegistration.Lock()
	defer s.service.codexRegistration.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.service.codexCallbacks.Load() != 0 {
		return cc.ErrUnavailable
	}
	return s.service.store.PublishCodexPrimary(ctx, a)
}

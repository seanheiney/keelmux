package codexcontroller

import (
	"context"
	"time"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"github.com/seanheiney/keelmux/internal/domain"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
	runtime "github.com/seanheiney/keelmux/internal/runtime"
)

// StdioGate extends the existing common lifecycle gate with read/import
// admission. It must hold the same Service gate and validate the original
// reservation and claimed incarnation. It grants no new writer authority.
type StdioGate interface {
	Gate
	WithPrimaryStdioAdmission(context.Context, ps.Binding, func(context.Context) error) error
}

type StdioStore interface {
	BindCodexPrimaryStdio(context.Context, ps.Channel) (ps.Ledger, error)
	ImportCodexPrimaryStdio(context.Context, ps.ImportBatch) (ps.ImportReceipt, error)
	CodexPrimaryStdio(context.Context, ps.Binding) (ps.Ledger, error)
	PublishCodexPrimary(context.Context, cb.AgentBinding) error
	CompleteCodexPrimaryStop(context.Context, StopRequest) error
	InvalidateCodexPrimaryStdio(context.Context, ps.Binding) error
}

type StdioSupervisor interface {
	ps.Supervisor
	Inspect(context.Context, domain.RuntimeRef) (runtime.Observation, error)
}

// StdioOptions is reviewed local installation configuration. There is no
// native image verifier injection, arbitrary argv or arbitrary native method.
type StdioOptions struct {
	Binary, BinarySHA256, SchemaSHA256, RealmRevision string
	Environment                                       ps.CleanEnvironment
	Timeout                                           time.Duration
}

type stdioDirectory struct {
	Path     string
	Identity directoryIdentity
}

// A tagged record cannot be adopted by the old WebSocket controller. Its
// snapshot is historical; live publication always needs fresh import + guard.
type stdioRecord struct {
	Version     int
	Spec        ps.LaunchSpec
	Ref         ps.ChannelRef
	Directories []stdioDirectory
	Snapshot    ps.Snapshot
}

type StdioController struct {
	journal    *Journal
	supervisor StdioSupervisor
	store      StdioStore
	gate       StdioGate
	options    StdioOptions
}

func NewStdio(j *Journal, supervisor StdioSupervisor, store StdioStore, gate StdioGate, options StdioOptions) (*StdioController, error) {
	if j == nil || absent(supervisor) || absent(store) || absent(gate) {
		return nil, ErrUnavailable
	}
	if readiness, ok := supervisor.(interface{ CheckStdioReady() error }); ok && readiness.CheckStdioReady() != nil {
		return nil, ErrUnavailable
	}
	if !cleanPath(options.Binary) || !digest(options.BinarySHA256) || options.SchemaSHA256 != ps.CodecSchemaSHA256 || !validText(options.RealmRevision) {
		return nil, ErrInvalid
	}
	if options.Timeout == 0 {
		options.Timeout = 10 * time.Second
	}
	if options.Timeout < time.Second || options.Timeout > 30*time.Second {
		return nil, ErrInvalid
	}
	for _, path := range stdioPaths(options.Environment) {
		canonical, _, err := ownedDirectory(path)
		if err != nil || canonical != path {
			return nil, ErrInvalid
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.available() {
		return nil, ErrStorage
	}
	return &StdioController{journal: j, supervisor: supervisor, store: store, gate: gate, options: options}, nil
}

func stdioPaths(e ps.CleanEnvironment) []string {
	return []string{e.Home, e.CodexHome, e.XDGConfigHome, e.XDGCacheHome, e.XDGDataHome, e.TempDir}
}

func validStdioEntry(e entry) bool {
	s := e.Stdio
	if s == nil || s.Version != ps.Version || s.Spec.SchemaSHA256 != ps.CodecSchemaSHA256 || !validRequest(e.Request) || !validText(string(e.Incarnation)) || s.Spec.Binding != stdioBinding(e) || ps.ValidateLaunchSpec(s.Spec) != nil || e.Capability != "" || e.Endpoint != "" || e.Evidence != (Evidence{}) || e.Binary != s.Spec.Binary || e.BinarySHA256 != s.Spec.BinarySHA256 || e.Home != s.Spec.Environment.Home || e.CodexHome != s.Spec.Environment.CodexHome || len(s.Directories) != 7 {
		return false
	}
	paths := append(stdioPaths(s.Spec.Environment), e.Request.Workspace)
	for i, p := range s.Directories {
		if p.Path != paths[i] || p.Identity.Inode == 0 {
			return false
		}
	}
	if e.StopOperation != "" && (!validText(string(e.StopOperation)) || e.StopOperation == e.Request.OperationID) {
		return false
	}
	if e.UpdatedAt.IsZero() || e.UpdatedAt.Year() < 1970 || e.UpdatedAt.Year() > 9999 {
		return false
	}
	switch e.State {
	case Recorded, Attempted, ProcessOwned, ResumeAttempted, Ready, StopAttempted, Stopped, Uncertain:
	default:
		return false
	}
	if s.Ref != (ps.ChannelRef{}) && (ps.ValidateChannel(ps.Channel{Version: ps.Version, Spec: s.Spec, Ref: s.Ref}) != nil || e.Process != s.Ref.Runtime) {
		return false
	}
	if s.Ref == (ps.ChannelRef{}) {
		if e.Process != (domain.RuntimeRef{}) {
			return false
		}
		switch e.State {
		case ProcessOwned, ResumeAttempted, Ready, StopAttempted, Stopped:
			return false
		}
	}
	if e.State == Ready && !stdioReady(e, s.Snapshot) {
		return false
	}
	return (e.State != StopAttempted && e.State != Stopped) || e.StopOperation != ""
}

func stdioBinding(e entry) ps.Binding {
	t := e.Request.Target
	return ps.Binding{HostID: t.HostID, SessionID: t.SessionID, ExecutionID: t.ExecutionID, Generation: t.Generation, StartOperationID: e.Request.OperationID, PrimaryIncarnation: e.Incarnation, NativeThreadID: e.Request.NativeThreadID, RealmRevision: e.Request.RealmRevision}
}

func stdioAgent(e entry) cb.AgentBinding {
	b, r := stdioBinding(e), e.Process
	return cb.AgentBinding{HostID: b.HostID, SessionID: b.SessionID, ExecutionID: b.ExecutionID, Generation: b.Generation, AppServerIncarnation: b.PrimaryIncarnation, AppServerPID: r.PID, AppServerBootID: r.BootID, AppServerProcessStart: r.ProcessStart, ProtocolVersion: ProtocolVersion, NativeThreadID: b.NativeThreadID}
}

func stdioReady(e entry, s ps.Snapshot) bool {
	return e.Stdio != nil && s.Channel == e.Stdio.Ref && s.Binding == stdioBinding(e) && ps.ValidateCursor(s.Cursor) == nil && s.Cursor.ChannelIncarnation == e.Stdio.Ref.ChannelIncarnation && s.SnapshotSHA256 == ps.SnapshotDigest(s) && s.Continuity == ps.ContinuityComplete && s.Bootstrap == ps.BootstrapComplete && !s.Pending && s.ProcessAlive && s.ExactImage && s.ExactDirectories && s.ExactLoadedThread && s.Idle && s.EmptyQueue && s.EmptyBackground
}

func (c *StdioController) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.options.Timeout)
}
func (c *StdioController) same(e entry) bool {
	if e.Stdio == nil || e.Stdio.Spec.Binary != c.options.Binary || e.Stdio.Spec.BinarySHA256 != c.options.BinarySHA256 || e.Stdio.Spec.SchemaSHA256 != c.options.SchemaSHA256 || e.Stdio.Spec.Environment != c.options.Environment || e.Request.RealmRevision != c.options.RealmRevision {
		return false
	}
	for _, d := range e.Stdio.Directories {
		if !sameDirectory(d.Path, d.Identity) {
			return false
		}
	}
	return true
}
func (c *StdioController) transition(e *entry, s State, r string) error {
	e.State, e.Reason, e.UpdatedAt = s, r, time.Now().UTC()
	return c.journal.save(*e)
}
func (c *StdioController) uncertain(ctx context.Context, e *entry, r string) (View, error) {
	if err := c.transition(e, Uncertain, r); err != nil {
		return e.view(), err
	}
	if err := c.store.InvalidateCodexPrimaryStdio(ctx, stdioBinding(*e)); err != nil {
		return e.view(), ErrStorage
	}
	return e.view(), ErrUncertain
}

// Start claims the common-store incarnation once. Repeated Start goes through
// fresh reconciliation and never repeats launch or bootstrap.
func (c *StdioController) Start(ctx context.Context, r StartRequest) (View, error) {
	if !validRequest(r) || r.RealmRevision != c.options.RealmRevision {
		return View{}, ErrInvalid
	}
	ctx, cancel := c.bounded(ctx)
	defer cancel()
	j := c.journal
	j.mu.Lock()
	if !j.available() {
		j.mu.Unlock()
		return View{}, ErrStorage
	}
	e, exists := j.entries[r.Target.ExecutionID]
	if exists {
		if e.Request != r || !c.same(e) {
			j.mu.Unlock()
			return e.view(), ErrConflict
		}
		j.mu.Unlock()
		return c.Reconcile(ctx, r.Target)
	}
	j.mu.Unlock()
	inc, err := nonce(16)
	if err != nil {
		return View{}, err
	}
	var out View
	legacy := Controller{gate: c.gate, options: Options{Timeout: c.options.Timeout}}
	err = legacy.gateRun(ctx, LifecycleRequest{Action: StartPrimary, OperationID: r.OperationID, Target: r.Target, NativeThreadID: r.NativeThreadID, RealmRevision: r.RealmRevision, Workspace: r.Workspace, StartIncarnation: domain.ID(inc)}, func(ctx context.Context) error {
		j.mu.Lock()
		defer j.mu.Unlock()
		if !j.available() {
			return ErrStorage
		}
		if _, exists := j.entries[r.Target.ExecutionID]; exists || j.operationUsed(r.OperationID) {
			return ErrConflict
		}
		for _, old := range j.entries {
			if old.Request.Target.SessionID == r.Target.SessionID || old.Request.NativeThreadID == r.NativeThreadID {
				return ErrConflict
			}
		}
		if err := verifyBinary(c.options.Binary, c.options.BinarySHA256); err != nil {
			return err
		}
		e := entry{Request: r, Incarnation: domain.ID(inc), Binary: c.options.Binary, BinarySHA256: c.options.BinarySHA256, Home: c.options.Environment.Home, CodexHome: c.options.Environment.CodexHome, State: Recorded, Reason: "stdio_intent_recorded", UpdatedAt: time.Now().UTC()}
		e.Stdio = &stdioRecord{Version: ps.Version, Spec: ps.LaunchSpec{Binding: stdioBinding(e), Workspace: r.Workspace, Binary: c.options.Binary, BinarySHA256: c.options.BinarySHA256, SchemaSHA256: c.options.SchemaSHA256, Profile: ps.Codex0159, Environment: c.options.Environment}}
		for _, path := range append(stdioPaths(c.options.Environment), r.Workspace) {
			canonical, id, err := ownedDirectory(path)
			if err != nil || canonical != path {
				return ErrConflict
			}
			e.Stdio.Directories = append(e.Stdio.Directories, stdioDirectory{Path: path, Identity: id})
		}
		if ps.ValidateLaunchSpec(e.Stdio.Spec) != nil {
			return ErrInvalid
		}
		if err := j.save(e); err != nil {
			return err
		}
		if err := c.transition(&e, Attempted, "stdio_launch_attempted"); err != nil {
			return err
		}
		ref, err := c.supervisor.LaunchStdio(ctx, e.Stdio.Spec)
		if err != nil {
			out, err = c.uncertain(ctx, &e, "stdio_launch_unconfirmed")
			return err
		}
		e.Stdio.Ref, e.Process = ref, ref.Runtime
		channel := ps.Channel{Version: ps.Version, Spec: e.Stdio.Spec, Ref: ref}
		if ps.ValidateChannel(channel) != nil {
			out, err = c.uncertain(ctx, &e, "stdio_channel_identity_unconfirmed")
			return err
		}
		if err := c.transition(&e, ProcessOwned, "stdio_process_owned"); err != nil {
			return err
		}
		if _, err = c.store.BindCodexPrimaryStdio(ctx, channel); err != nil {
			out, err = c.uncertain(ctx, &e, "stdio_common_binding_unconfirmed")
			return err
		}
		if err := c.transition(&e, ResumeAttempted, "stdio_bootstrap_attempted"); err != nil {
			return err
		}
		ob, err := c.supervisor.BootstrapStdioOnce(ctx, ref)
		if err != nil {
			out, err = c.uncertain(ctx, &e, "stdio_bootstrap_unconfirmed")
			return err
		}
		if err = c.importObservation(ctx, e, ob); err != nil {
			out, err = c.uncertain(ctx, &e, "stdio_bootstrap_import_unconfirmed")
			return err
		}
		out, err = c.refresh(ctx, &e)
		return err
	})
	return out, err
}

func (c *StdioController) importObservation(ctx context.Context, e entry, ob ps.Observation) error {
	channel := ps.Channel{Version: ps.Version, Spec: e.Stdio.Spec, Ref: e.Stdio.Ref}
	if ps.ValidateObservation(channel, ob) != nil {
		return ErrUncertain
	}
	importID := domain.NewID("stdio_import")
	r, err := c.store.ImportCodexPrimaryStdio(ctx, ps.ImportBatch{ImportID: importID, Channel: channel, Observation: ob})
	if err != nil || r.ImportID != importID || r.Channel != channel.Ref || r.From != ob.From || r.Through != ob.Through || r.BatchSHA256 != ob.BatchSHA256 {
		return ErrUncertain
	}
	if err = c.supervisor.AckStdioImported(ctx, e.Stdio.Ref, ps.ImportAck{From: r.From, Through: r.Through, BatchSHA256: r.BatchSHA256}); err != nil {
		return ErrUncertain
	}
	return nil
}

func (c *StdioController) admission(ctx context.Context, b ps.Binding, effect func(context.Context) error) error {
	called, repeated := false, false
	var effectErr error
	err := c.gate.WithPrimaryStdioAdmission(ctx, b, func(inner context.Context) error {
		if called || inner == nil {
			repeated = true
			return ErrUnavailable
		}
		called = true
		deadline, ok := ctx.Deadline()
		if !ok {
			effectErr = ErrUnavailable
			return effectErr
		}
		bound, cancel := context.WithDeadline(inner, deadline)
		defer cancel()
		if err := bound.Err(); err != nil {
			effectErr = err
			return effectErr
		}
		effectErr = effect(bound)
		return effectErr
	})
	if repeated || !called || err != nil || effectErr != nil {
		return ErrUncertain
	}
	return nil
}

// refresh is always under common admission and journal mutex. Receive evidence
// is imported before helper acknowledgement. There is no automatic resume here.
func (c *StdioController) refresh(ctx context.Context, e *entry) (View, error) {
	if !c.same(*e) || e.Stdio.Ref == (ps.ChannelRef{}) {
		return c.uncertain(ctx, e, "stdio_identity_unconfirmed")
	}
	if e.StopOperation != "" {
		if err := c.importRetained(ctx, *e); err != nil {
			return c.uncertain(ctx, e, "stdio_stop_import_unconfirmed")
		}
		o, err := c.supervisor.Inspect(ctx, e.Process)
		if err != nil || o.Ref != e.Process || o.Alive {
			return c.uncertain(ctx, e, "stdio_stop_unconfirmed")
		}
		if err = c.store.CompleteCodexPrimaryStop(ctx, StopRequest{OperationID: e.StopOperation, Target: e.Request.Target, Incarnation: e.Incarnation}); err != nil {
			return c.uncertain(ctx, e, "stdio_stop_commit_unconfirmed")
		}
		if err = c.transition(e, Stopped, "stdio_stop_observed"); err != nil {
			return e.view(), err
		}
		return e.view(), nil
	}
	for i := 0; i < ps.MaxEvents+1; i++ {
		ledger, err := c.store.CodexPrimaryStdio(ctx, stdioBinding(*e))
		if err != nil || !ledger.Bound || ledger.Channel != (ps.Channel{Version: ps.Version, Spec: e.Stdio.Spec, Ref: e.Stdio.Ref}) {
			return c.uncertain(ctx, e, "stdio_common_binding_unconfirmed")
		}
		ob, err := c.supervisor.ObserveStdio(ctx, e.Stdio.Ref, ledger.Cursor)
		if err != nil {
			return c.uncertain(ctx, e, "stdio_observation_unconfirmed")
		}
		if err = c.importObservation(ctx, *e, ob); err != nil {
			return c.uncertain(ctx, e, "stdio_import_unconfirmed")
		}
		if ob.More {
			continue
		}
		ledger, err = c.store.CodexPrimaryStdio(ctx, stdioBinding(*e))
		if err != nil || ledger.Cursor != ob.Snapshot.Cursor || ledger.Pending || ledger.Continuity != ps.ContinuityComplete || !stdioReady(*e, ob.Snapshot) {
			return c.uncertain(ctx, e, "stdio_quiescence_unconfirmed")
		}
		e.Stdio.Snapshot = ob.Snapshot
		quiescence := ps.Quiescence{Cursor: ob.Snapshot.Cursor, SnapshotSHA256: ob.Snapshot.SnapshotSHA256}
		guardCalls := 0
		var commitErr error
		err = c.supervisor.WithStdioQuiescence(ctx, e.Stdio.Ref, quiescence, func(inner context.Context) error {
			guardCalls++
			if guardCalls != 1 || inner == nil {
				commitErr = ErrUnavailable
				return commitErr
			}
			deadline, ok := ctx.Deadline()
			if !ok {
				commitErr = ErrUnavailable
				return commitErr
			}
			bound, cancel := context.WithDeadline(inner, deadline)
			defer cancel()
			if err := bound.Err(); err != nil {
				commitErr = err
				return commitErr
			}
			if err := c.store.PublishCodexPrimary(bound, stdioAgent(*e)); err != nil {
				commitErr = err
				return commitErr
			}
			commitErr = c.transition(e, Ready, "supervisor_stdio_verified")
			return commitErr
		})
		if err != nil || guardCalls != 1 || commitErr != nil {
			return c.uncertain(ctx, e, "stdio_publication_guard_unconfirmed")
		}
		return e.view(), nil
	}
	return c.uncertain(ctx, e, "stdio_import_bound_exhausted")
}

func (c *StdioController) importRetained(ctx context.Context, e entry) error {
	channel := ps.Channel{Version: ps.Version, Spec: e.Stdio.Spec, Ref: e.Stdio.Ref}
	for i := 0; i < ps.MaxEvents+1; i++ {
		ledger, err := c.store.CodexPrimaryStdio(ctx, stdioBinding(e))
		if err != nil || !ledger.Bound || ledger.Channel != channel {
			return ErrUncertain
		}
		ob, err := c.supervisor.RetainedStdio(ctx, e.Stdio.Ref, ledger.Cursor)
		if err != nil {
			return ErrUncertain
		}
		if err = c.importObservation(ctx, e, ob); err != nil {
			return ErrUncertain
		}
		if !ob.More {
			return nil
		}
	}
	return ErrUncertain
}

func (c *StdioController) Reconcile(ctx context.Context, target Target) (View, error) {
	if !validTarget(target) {
		return View{}, ErrInvalid
	}
	ctx, cancel := c.bounded(ctx)
	defer cancel()
	j := c.journal
	j.mu.Lock()
	if !j.available() {
		j.mu.Unlock()
		return View{}, ErrStorage
	}
	e, ok := j.entries[target.ExecutionID]
	if !ok {
		j.mu.Unlock()
		return View{}, ErrNotFound
	}
	if e.Request.Target != target || !c.same(e) {
		j.mu.Unlock()
		return e.view(), ErrConflict
	}
	b := stdioBinding(e)
	j.mu.Unlock()
	var out View
	err := c.admission(ctx, b, func(ctx context.Context) error {
		j.mu.Lock()
		defer j.mu.Unlock()
		if !j.available() {
			return ErrStorage
		}
		current, ok := j.entries[target.ExecutionID]
		if !ok || current.Request != e.Request || current.Incarnation != e.Incarnation {
			return ErrConflict
		}
		var err error
		out, err = c.refresh(ctx, &current)
		return err
	})
	if err != nil {
		return out, ErrUncertain
	}
	return out, nil
}

// Stop first obtains current imported evidence, then commits the existing
// common stop fence. The helper finally checks its own current receive frontier
// and records a signal attempt before touching the retained direct child handle.
func (c *StdioController) Stop(ctx context.Context, r StopRequest) (View, error) {
	if !validTarget(r.Target) || !validText(string(r.OperationID)) || !validText(string(r.Incarnation)) {
		return View{}, ErrInvalid
	}
	ctx, cancel := c.bounded(ctx)
	defer cancel()
	j := c.journal
	j.mu.Lock()
	if !j.available() {
		j.mu.Unlock()
		return View{}, ErrStorage
	}
	e, ok := j.entries[r.Target.ExecutionID]
	if !ok {
		j.mu.Unlock()
		return View{}, ErrNotFound
	}
	if e.Request.Target != r.Target || e.Incarnation != r.Incarnation || !c.same(e) || r.OperationID == e.Request.OperationID || (e.StopOperation != "" && e.StopOperation != r.OperationID) {
		j.mu.Unlock()
		return e.view(), ErrConflict
	}
	if e.StopOperation != "" {
		j.mu.Unlock()
		return c.Reconcile(ctx, r.Target)
	}
	j.mu.Unlock()
	if _, err := c.Reconcile(ctx, r.Target); err != nil {
		return e.view(), err
	}
	j.mu.Lock()
	e = j.entries[r.Target.ExecutionID]
	j.mu.Unlock()
	var out View
	legacy := Controller{gate: c.gate, options: Options{Timeout: c.options.Timeout}}
	err := legacy.gateRun(ctx, LifecycleRequest{Action: StopPrimary, OperationID: r.OperationID, Target: r.Target, Agent: stdioAgent(e), NativeThreadID: e.Request.NativeThreadID, RealmRevision: e.Request.RealmRevision, Workspace: e.Request.Workspace}, func(ctx context.Context) error {
		j.mu.Lock()
		defer j.mu.Unlock()
		if !j.available() {
			return ErrStorage
		}
		current, ok := j.entries[r.Target.ExecutionID]
		if !ok || current.Request != e.Request || current.Incarnation != e.Incarnation || !c.same(current) || current.StopOperation != "" || j.operationUsed(r.OperationID) {
			return ErrConflict
		}
		current.StopOperation = r.OperationID
		if err := c.transition(&current, StopAttempted, "stdio_stop_attempted"); err != nil {
			return err
		}
		q := ps.Quiescence{Cursor: current.Stdio.Snapshot.Cursor, SnapshotSHA256: current.Stdio.Snapshot.SnapshotSHA256}
		if err := c.supervisor.StopStdio(ctx, current.Stdio.Ref, ps.StopRequest{OperationID: r.OperationID, Quiescence: q}); err != nil {
			out, err = c.uncertain(ctx, &current, "stdio_stop_unconfirmed")
			return err
		}
		var refreshErr error
		out, refreshErr = c.refresh(ctx, &current)
		return refreshErr
	})
	return out, err
}

// Inspect is historical only and cannot publish or prove a current channel.
func (c *StdioController) Inspect(ctx context.Context, id domain.ID) (View, error) {
	if err := ctx.Err(); err != nil {
		return View{}, err
	}
	j := c.journal
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.available() {
		return View{}, ErrStorage
	}
	e, ok := j.entries[id]
	if !ok {
		return View{}, ErrNotFound
	}
	if e.Stdio == nil {
		return e.view(), ErrConflict
	}
	return e.view(), nil
}

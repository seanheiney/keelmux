package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	cc "github.com/seanheiney/keelmux/internal/codexcontroller"
	"github.com/seanheiney/keelmux/internal/domain"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
	runtime "github.com/seanheiney/keelmux/internal/runtime"
)

// This fixture models helper protocol evidence, not a native provider. The
// independent nativepty lane exercises real helper/pipe ownership separately.
type stdioSupervisorFixture struct {
	mu                                            sync.Mutex
	channel                                       ps.Channel
	events                                        []ps.Event
	snapshot                                      ps.Snapshot
	alive                                         bool
	launches, bootstraps, stops, publications     int
	lostLaunch, lostBootstrap, lostAck, lostGuard bool
	guardBehavior                                 string
	beforeGuard, beforeStop                       func(*stdioSupervisorFixture)
}

func (f *stdioSupervisorFixture) LaunchStdio(_ context.Context, s ps.LaunchSpec) (ps.ChannelRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launches++
	f.alive = true
	r := ps.ChannelRef{PrimaryIncarnation: s.Binding.PrimaryIncarnation, ChannelIncarnation: "11111111111111111111111111111111", Runtime: domain.RuntimeRef{Backend: "native", Socket: "/owned/stdio.sock", Session: "owned", PID: 1234, BootID: "boot", ProcessStart: "start", ExecutionID: s.Binding.ExecutionID, OperationID: s.Binding.StartOperationID}}
	f.channel = ps.Channel{Version: ps.Version, Spec: s, Ref: r}
	f.snapshot = ps.Snapshot{Channel: r, Binding: s.Binding, Cursor: ps.Genesis(r.ChannelIncarnation), Continuity: ps.ContinuityComplete, Bootstrap: ps.BootstrapUnsent, ProcessAlive: true, ExactImage: true, ExactDirectories: true}
	if f.lostLaunch {
		return ps.ChannelRef{}, ps.ErrUncertain
	}
	return r, nil
}
func (f *stdioSupervisorFixture) frame(kind ps.EventKind, reason ps.ReasonCode) {
	e := ps.Event{Kind: kind, Reason: reason, FrameSHA256: fixtureHash("private fixture frame"), FrameBytes: 21}
	n, err := ps.AdvanceCursor(f.snapshot.Cursor, e)
	if err != nil {
		panic(err)
	}
	e.Cursor = n
	f.events = append(f.events, e)
	f.snapshot.Cursor = n
	if ps.EventBlocks(e) {
		f.snapshot.Pending = true
	}
}
func fixtureHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (f *stdioSupervisorFixture) batch(from ps.Cursor) ps.Observation {
	f.snapshot.SnapshotSHA256 = ps.SnapshotDigest(f.snapshot)
	o := ps.Observation{Snapshot: f.snapshot, From: from, Through: f.snapshot.Cursor, Events: []ps.Event{}}
	for _, e := range f.events {
		if e.Cursor.Seq > from.Seq {
			o.Events = append(o.Events, e)
		}
	}
	o.BatchSHA256 = ps.BatchDigest(o)
	return o
}
func (f *stdioSupervisorFixture) BootstrapStdioOnce(_ context.Context, r ps.ChannelRef) (ps.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r != f.channel.Ref || f.bootstraps != 0 {
		return ps.Observation{}, ps.ErrConflict
	}
	f.bootstraps++
	f.snapshot.Bootstrap = ps.BootstrapComplete
	f.frame(ps.EventResult, ps.ReasonNone)
	if f.lostBootstrap {
		return ps.Observation{}, ps.ErrUncertain
	}
	return f.batch(ps.Genesis(r.ChannelIncarnation)), nil
}
func (f *stdioSupervisorFixture) ObserveStdio(_ context.Context, r ps.ChannelRef, from ps.Cursor) (ps.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r != f.channel.Ref || !f.alive {
		return ps.Observation{}, ps.ErrUnavailable
	}
	f.frame(ps.EventResult, ps.ReasonNone)
	f.snapshot.ExactLoadedThread, f.snapshot.Idle, f.snapshot.EmptyQueue, f.snapshot.EmptyBackground = true, true, true, true
	return f.batch(from), nil
}
func (f *stdioSupervisorFixture) RetainedStdio(_ context.Context, r ps.ChannelRef, from ps.Cursor) (ps.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r != f.channel.Ref {
		return ps.Observation{}, ps.ErrConflict
	}
	f.snapshot.ProcessAlive = f.alive
	return f.batch(from), nil
}
func (f *stdioSupervisorFixture) AckStdioImported(_ context.Context, r ps.ChannelRef, a ps.ImportAck) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r != f.channel.Ref || a.Through != f.snapshot.Cursor {
		return ps.ErrConflict
	}
	if f.lostAck {
		f.lostAck = false
		return ps.ErrUncertain
	}
	return nil
}
func (f *stdioSupervisorFixture) WithStdioQuiescence(ctx context.Context, r ps.ChannelRef, q ps.Quiescence, publish func(context.Context) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeGuard != nil {
		hook := f.beforeGuard
		f.beforeGuard = nil
		hook(f)
	}
	if r != f.channel.Ref || q.Cursor != f.snapshot.Cursor || q.SnapshotSHA256 != f.snapshot.SnapshotSHA256 || f.snapshot.Pending {
		return ps.ErrUncertain
	}
	switch f.guardBehavior {
	case "skip":
		return nil
	case "nil":
		_ = publish(nil)
		return nil
	case "duplicate":
		_ = publish(ctx)
		_ = publish(ctx)
		return nil
	}
	if err := publish(ctx); err != nil {
		return err
	}
	f.publications++
	if f.lostGuard {
		f.lostGuard = false
		return ps.ErrUncertain
	}
	return nil
}
func (f *stdioSupervisorFixture) StopStdio(_ context.Context, r ps.ChannelRef, s ps.StopRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeStop != nil {
		hook := f.beforeStop
		f.beforeStop = nil
		hook(f)
	}
	if r != f.channel.Ref || s.Quiescence.Cursor != f.snapshot.Cursor || s.Quiescence.SnapshotSHA256 != f.snapshot.SnapshotSHA256 || f.snapshot.Pending {
		return ps.ErrUncertain
	}
	f.stops++
	f.alive = false
	return nil
}
func (f *stdioSupervisorFixture) Inspect(_ context.Context, r domain.RuntimeRef) (runtime.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r != f.channel.Ref.Runtime {
		return runtime.Observation{}, ps.ErrConflict
	}
	return runtime.Observation{Ref: r, Alive: f.alive}, nil
}

type stdioFixture struct {
	e       *env
	p       *ExperimentalStdioPrimary
	j       *cc.Journal
	helper  *stdioSupervisorFixture
	r       cc.StartRequest
	options cc.StdioOptions
	dir     string
}

func stdioSetup(t *testing.T) *stdioFixture {
	t.Helper()
	x := &stdioFixture{e: setup(t), helper: &stdioSupervisorFixture{}}
	var err error
	x.dir, err = os.MkdirTemp("/tmp", "ks-")
	if err != nil {
		t.Fatal(err)
	}
	x.dir, err = filepath.EvalSymlinks(x.dir)
	if err != nil {
		t.Fatal(err)
	}
	x.e.workspace, err = filepath.EvalSymlinks(x.e.workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { x.j.Close(); os.RemoveAll(x.dir) })
	x.j, err = cc.OpenJournal(filepath.Join(x.dir, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(x.dir, "fixture")
	data := []byte("fixture executable never executed")
	if err = os.WriteFile(binary, data, 0700); err != nil {
		t.Fatal(err)
	}
	x.options = cc.StdioOptions{Binary: binary, BinarySHA256: fixtureHash(string(data)), SchemaSHA256: ps.CodecSchemaSHA256, RealmRevision: "realm", Environment: ps.CleanEnvironment{Home: x.dir, CodexHome: x.dir, XDGConfigHome: x.dir, XDGCacheHome: x.dir, XDGDataHome: x.dir, TempDir: x.dir, Path: "/usr/bin", Locale: "C"}, Timeout: time.Second}
	x.p, err = x.e.service.NewExperimentalStdioPrimary(x.j, x.helper, x.options)
	if err != nil {
		t.Fatal(err)
	}
	x.r, err = x.e.service.PrepareCodexPrimary(context.Background(), PrimarySelection{OperationID: "stdio-primary", Name: "Stdio", Workspace: x.e.workspace, NativeThreadID: "native-thread", RealmRevision: "realm"})
	if err != nil {
		t.Fatal(err)
	}
	return x
}
func (x *stdioFixture) start(t *testing.T) StdioPrimaryStatus {
	t.Helper()
	v, err := x.p.Start(context.Background(), x.r)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func (x *stdioFixture) restart(t *testing.T) {
	t.Helper()
	if err := x.j.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	x.j, err = cc.OpenJournal(filepath.Join(x.dir, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	x.e.service, err = New(x.e.store, x.e.backend, x.r.Target.HostID)
	if err != nil {
		t.Fatal(err)
	}
	x.p, err = x.e.service.NewExperimentalStdioPrimary(x.j, x.helper, x.options)
	if err != nil {
		t.Fatal(err)
	}
}

func TestStdioPrimaryCommonStoreReconcileNeverRepeatsBootstrap(t *testing.T) {
	x := stdioSetup(t)
	first := x.start(t)
	if !first.ChannelVerified || first.NativeProviderQualified || first.Transport != "supervisor_stdio" {
		t.Fatal("incorrect qualification")
	}
	ref := x.helper.channel.Ref
	x.restart(t)
	second := x.start(t)
	if second.Primary.Incarnation != first.Primary.Incarnation || x.helper.channel.Ref != ref || x.helper.launches != 1 || x.helper.bootstraps != 1 || x.helper.publications != 2 {
		t.Fatal("restart replaced native lifecycle")
	}
	ledger, err := x.e.store.CodexPrimaryStdio(context.Background(), x.helper.channel.Spec.Binding)
	if err != nil || !ledger.Bound || ledger.Cursor != x.helper.snapshot.Cursor || ledger.Pending {
		t.Fatal("common import frontier missing", err)
	}
	r := cc.StopRequest{OperationID: "stdio-stop", Target: x.r.Target, Incarnation: first.Primary.Incarnation}
	stopped, err := x.p.Stop(context.Background(), r)
	if err != nil || stopped.Primary.State != cc.Stopped || stopped.ChannelVerified || x.helper.stops != 1 {
		t.Fatal("stop not completed", err)
	}
	if _, err = x.p.Stop(context.Background(), r); err != nil || x.helper.stops != 1 {
		t.Fatal("stop replayed", err)
	}
	if _, err = x.p.Start(context.Background(), x.r); err != nil || x.helper.launches != 1 || x.helper.bootstraps != 1 {
		t.Fatal("stopped primary rearmed", err)
	}
}

func TestStdioLostAcknowledgementsReconcileEvidenceOnly(t *testing.T) {
	for _, where := range []string{"bootstrap", "import", "guard"} {
		t.Run(where, func(t *testing.T) {
			x := stdioSetup(t)
			switch where {
			case "bootstrap":
				x.helper.lostBootstrap = true
			case "import":
				x.helper.lostAck = true
			case "guard":
				x.helper.lostGuard = true
			}
			v, err := x.p.Start(context.Background(), x.r)
			if err == nil || v.ChannelVerified || x.helper.launches != 1 || x.helper.bootstraps != 1 {
				t.Fatal("lost acknowledgement claimed readiness")
			}
			common, commonErr := x.e.store.Session(context.Background(), x.r.Target.SessionID)
			if commonErr != nil || common.Execution.State != "primary_uncertain" {
				t.Fatal("lost guard/ack left current-ready common state", commonErr)
			}
			x.restart(t)
			v, err = x.p.Start(context.Background(), x.r)
			if err != nil || !v.ChannelVerified || x.helper.launches != 1 || x.helper.bootstraps != 1 {
				t.Fatal("evidence-only recovery failed", err)
			}
		})
	}
}

func TestStdioCallbacksBeforePublicationAreDurableWithoutGrant(t *testing.T) {
	x := stdioSetup(t)
	x.helper.beforeGuard = func(f *stdioSupervisorFixture) { f.frame(ps.EventCallback, ps.ReasonPendingCallback) }
	v, err := x.p.Start(context.Background(), x.r)
	if err == nil || v.ChannelVerified || x.helper.publications != 0 {
		t.Fatal("callback lost publication race")
	}
	x.restart(t)
	if v, err = x.p.Start(context.Background(), x.r); err == nil || v.ChannelVerified {
		t.Fatal("callback cleared after recovery")
	}
	ledger, err := x.e.store.CodexPrimaryStdio(context.Background(), x.helper.channel.Spec.Binding)
	if err != nil || !ledger.Pending || ledger.Cursor != x.helper.snapshot.Cursor {
		t.Fatal("grantless callback not durably imported", err)
	}
	if x.helper.bootstraps != 1 || x.helper.stops != 0 {
		t.Fatal("blocked callback caused lifecycle effect")
	}
}

func TestStdioCallbackImmediatelyBeforeSignalRefusesAndDoesNotRetry(t *testing.T) {
	x := stdioSetup(t)
	v := x.start(t)
	x.helper.beforeStop = func(f *stdioSupervisorFixture) { f.frame(ps.EventCallback, ps.ReasonPendingCallback) }
	r := cc.StopRequest{OperationID: "stop-race", Target: x.r.Target, Incarnation: v.Primary.Incarnation}
	if _, err := x.p.Stop(context.Background(), r); err == nil || x.helper.stops != 0 {
		t.Fatal("callback overtaken by signal")
	}
	x.restart(t)
	if _, err := x.p.Stop(context.Background(), r); err == nil || x.helper.stops != 0 || !x.helper.alive {
		t.Fatal("uncertain stop repeated signal")
	}
	ledger, err := x.e.store.CodexPrimaryStdio(context.Background(), x.helper.channel.Spec.Binding)
	if err != nil || !ledger.Pending || ledger.Cursor != x.helper.snapshot.Cursor {
		t.Fatal("stop-winning callback never imported", err)
	}
	stored, err := x.e.store.Session(context.Background(), x.r.Target.SessionID)
	if err != nil || stored.Execution.State != "primary_stopping" {
		t.Fatal("stop fence lost", err)
	}
}

func TestStdioGuardMustInvokeOneBoundedPublicationClosure(t *testing.T) {
	for _, behavior := range []string{"skip", "nil", "duplicate"} {
		t.Run(behavior, func(t *testing.T) {
			x := stdioSetup(t)
			x.start(t)
			x.helper.guardBehavior = behavior
			if v, err := x.p.Start(context.Background(), x.r); err == nil || v.ChannelVerified || v.Primary.State != cc.Uncertain {
				t.Fatal("invalid guard retained current-ready claim")
			}
			if x.helper.launches != 1 || x.helper.bootstraps != 1 {
				t.Fatal("guard failure replayed native lifecycle")
			}
		})
	}
}

func TestStdioLostJournalAndLostLaunchDoNotRearm(t *testing.T) {
	for _, lostLaunch := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "launch_ack"}[lostLaunch], func(t *testing.T) {
			x := stdioSetup(t)
			x.helper.lostLaunch = lostLaunch
			_, err := x.p.Start(context.Background(), x.r)
			if (err != nil) != lostLaunch {
				t.Fatal("unexpected initial result", err)
			}
			x.j.Close()
			if err = os.Remove(filepath.Join(x.dir, "journal", "primaries.json")); err != nil {
				t.Fatal(err)
			}
			x.restart(t)
			if v, err := x.p.Start(context.Background(), x.r); err == nil || v.ChannelVerified || x.helper.launches != 1 {
				t.Fatal("missing journal rearmed common claim")
			}
		})
	}
}

func TestStdioHistoricalReadyAndWebSocketRecordNeverAuthorizeControl(t *testing.T) {
	x := stdioSetup(t)
	x.start(t)
	x.helper.alive = false
	if v, err := x.p.Start(context.Background(), x.r); err == nil || v.ChannelVerified {
		t.Fatal("historical Ready was treated as current")
	}
	oldSupervisor := &primarySupervisor{}
	old, err := x.e.service.NewCodexPrimary(x.j, oldSupervisor, &primaryUpstream{}, cc.Options{Binary: x.options.Binary, BinarySHA256: x.options.BinarySHA256, Home: x.dir, CodexHome: x.dir, RealmRevision: "realm", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.Start(context.Background(), x.r); err == nil || oldSupervisor.launches != 0 {
		t.Fatal("stdio entry adopted as WebSocket evidence")
	}
}

func TestStdioExistingCallbackRegistrationBlocksPublication(t *testing.T) {
	x := stdioSetup(t)
	x.helper.beforeGuard = func(*stdioSupervisorFixture) { x.e.service.codexCallbacks.Add(1) }
	defer x.e.service.codexCallbacks.Store(0)
	if v, err := x.p.Start(context.Background(), x.r); err == nil || v.ChannelVerified || x.helper.publications != 0 {
		t.Fatal("waiting broker callback overtaken")
	}
}

func TestStdioControllerRejectsUnreviewedCodecBeforeAdmission(t *testing.T) {
	x := stdioSetup(t)
	bad := x.options
	bad.SchemaSHA256 = fixtureHash("unreviewed schema")
	if _, err := x.e.service.NewExperimentalStdioPrimary(x.j, x.helper, bad); err == nil || x.helper.launches != 0 {
		t.Fatal("unreviewed profile admitted")
	}
	r, err := x.e.store.CodexPrimaryReservation(context.Background(), x.r.Target.SessionID, x.r.Target.ExecutionID)
	if err != nil || r.Start != nil {
		t.Fatal("failed construction consumed common start", err)
	}
}

type stdioRegistrationRaceGate struct {
	*primaryStdioGate
	action cc.Action
}

func (g *stdioRegistrationRaceGate) WithPrimaryLifecycle(ctx context.Context, r cc.LifecycleRequest, effect func(context.Context) error) error {
	return g.primaryStdioGate.WithPrimaryLifecycle(ctx, r, func(ctx context.Context) error {
		if r.Action == g.action {
			g.service.codexRegistration.Lock()
			g.service.codexCallbacks.Add(1)
			g.service.codexRegistration.Unlock()
		}
		return effect(ctx)
	})
}

func TestStdioBrokerCallbackAfterCommonFenceCannotReachLifecycleEffect(t *testing.T) {
	for _, action := range []cc.Action{cc.StartPrimary, cc.StopPrimary} {
		t.Run(string(action), func(t *testing.T) {
			x := stdioSetup(t)
			gate := &stdioRegistrationRaceGate{primaryStdioGate: &primaryStdioGate{primaryGate: primaryGate{service: x.e.service, realm: "realm"}}, action: action}
			controller, err := cc.NewStdio(x.j, &primaryStdioSupervisorGate{StdioSupervisor: x.helper, service: x.e.service}, &primaryStdioStore{service: x.e.service}, gate, x.options)
			if err != nil {
				t.Fatal(err)
			}
			x.p = &ExperimentalStdioPrimary{controller: controller}
			defer x.e.service.codexCallbacks.Store(0)
			if action == cc.StartPrimary {
				if v, err := x.p.Start(context.Background(), x.r); err == nil || v.ChannelVerified || x.helper.launches != 0 {
					t.Fatal("registered callback overtaken by launch")
				}
				return
			}
			v := x.start(t)
			if _, err = x.p.Stop(context.Background(), cc.StopRequest{OperationID: "registered-stop", Target: x.r.Target, Incarnation: v.Primary.Incarnation}); err == nil || x.helper.stops != 0 {
				t.Fatal("registered callback overtaken by signal")
			}
			stored, err := x.e.store.Session(context.Background(), x.r.Target.SessionID)
			if err != nil || stored.Execution.State != "primary_stopping" {
				t.Fatal("fence disappeared after callback refusal", err)
			}
		})
	}
}

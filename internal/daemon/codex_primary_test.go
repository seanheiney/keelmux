package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	cc "github.com/seanheiney/keelmux/internal/codexcontroller"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	bpruntime "github.com/seanheiney/keelmux/internal/runtime"
)

type primarySupervisor struct {
	mu              sync.Mutex
	ref             domain.RuntimeRef
	alive           bool
	launches, stops int
	stopErr         error
	beforeStop      func()
}

func (f *primarySupervisor) LaunchProcess(_ context.Context, s domain.LaunchSpec) (domain.RuntimeRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launches++
	f.alive = true
	f.ref = domain.RuntimeRef{Backend: "native", Socket: "/owned/runtime.sock", Session: "owned", PID: 1234, BootID: "boot", ProcessStart: "start", ExecutionID: s.Session.Execution.ID, OperationID: s.OperationID}
	return f.ref, nil
}
func (f *primarySupervisor) Lookup(context.Context, domain.ID, domain.ID) (bpruntime.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return bpruntime.Observation{Ref: f.ref, Alive: f.alive}, nil
}
func (f *primarySupervisor) Inspect(ctx context.Context, _ domain.RuntimeRef) (bpruntime.Observation, error) {
	return f.Lookup(ctx, "", "")
}
func (f *primarySupervisor) Stop(context.Context, domain.RuntimeRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeStop != nil {
		f.beforeStop()
	}
	f.stops++
	f.alive = false
	return f.stopErr
}

type primaryUpstream struct {
	mu               sync.Mutex
	resumes          int
	failResume       bool
	entered, release chan struct{}
	beforeInspect    func()
}

func (u *primaryUpstream) Resume(ctx context.Context, l cc.Launch, r domain.RuntimeRef) (cc.Evidence, error) {
	u.mu.Lock()
	u.resumes++
	fail := u.failResume
	u.mu.Unlock()
	if u.entered != nil {
		close(u.entered)
		select {
		case <-u.release:
		case <-ctx.Done():
			return cc.Evidence{}, ctx.Err()
		}
	}
	if fail {
		return cc.Evidence{}, errors.New("lost native acknowledgement")
	}
	return u.Inspect(ctx, l, r)
}
func (u *primaryUpstream) Inspect(_ context.Context, l cc.Launch, r domain.RuntimeRef) (cc.Evidence, error) {
	if u.beforeInspect != nil {
		u.beforeInspect()
	}
	t := l.Request.Target
	return cc.Evidence{Agent: cb.AgentBinding{HostID: t.HostID, SessionID: t.SessionID, ExecutionID: t.ExecutionID, Generation: t.Generation, AppServerIncarnation: l.Incarnation, AppServerPID: r.PID, AppServerBootID: r.BootID, AppServerProcessStart: r.ProcessStart, ProtocolVersion: cc.ProtocolVersion, NativeThreadID: l.Request.NativeThreadID}, CapabilitySHA256: l.CapabilitySHA256(), SocketIdentity: "owned-socket", Idle: true, EmptyQueue: true, EmptyBackground: true}, nil
}
func (*primaryUpstream) Close(context.Context, domain.ID) error { return nil }

type primaryFixture struct {
	e       *env
	p       *CodexPrimary
	j       *cc.Journal
	s       *primarySupervisor
	u       *primaryUpstream
	r       cc.StartRequest
	options cc.Options
	dir     string
}

func primarySetup(t *testing.T) *primaryFixture {
	t.Helper()
	x := &primaryFixture{e: setup(t), s: &primarySupervisor{}, u: &primaryUpstream{}}
	var err error
	x.dir, err = os.MkdirTemp("/tmp", "kp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(x.dir) })
	x.j, err = cc.OpenJournal(filepath.Join(x.dir, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { x.j.Close() })
	executable := filepath.Join(x.dir, "codex")
	data := []byte("fixture executable never executed")
	if err = os.WriteFile(executable, data, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	x.options = cc.Options{Binary: executable, BinarySHA256: hex.EncodeToString(sum[:]), Home: x.dir, CodexHome: x.dir, RealmRevision: "realm", Timeout: time.Second}
	x.p, err = x.e.service.NewCodexPrimary(x.j, x.s, x.u, x.options)
	if err != nil {
		t.Fatal(err)
	}
	x.r, err = x.e.service.PrepareCodexPrimary(context.Background(), PrimarySelection{OperationID: "primary", Name: "Primary", Workspace: x.e.workspace, NativeThreadID: "native-thread", RealmRevision: "realm"})
	if err != nil {
		t.Fatal(err)
	}
	return x
}
func (x *primaryFixture) start(t *testing.T) PrimaryStatus {
	t.Helper()
	out, err := x.p.Start(context.Background(), x.r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPrimaryReservationBlocksTerminalFallbackBeforeLaunch(t *testing.T) {
	x := primarySetup(t)
	ctx := context.Background()
	target := x.r.Target
	if _, err := x.e.service.Attach(ctx, protocol.LeaseRequest{SessionID: target.SessionID, ExecutionID: target.ExecutionID, Generation: target.Generation, ClientID: "native", OperationID: "attach"}); err == nil {
		t.Fatal("reserved primary admitted terminal attachment")
	}
	if err := x.e.service.Stop(ctx, protocol.SessionRequest{SessionID: target.SessionID, ExecutionID: target.ExecutionID, Generation: target.Generation, OperationID: "stop"}); err == nil {
		t.Fatal("reserved primary admitted terminal stop")
	}
	if _, err := x.e.store.Acquire(ctx, domain.Lease{SessionID: target.SessionID, ExecutionID: target.ExecutionID, Generation: target.Generation, OperationID: "store-attach", ClientID: "native", Token: "token"}, false, nil); err == nil {
		t.Fatal("store bypass admitted terminal writer")
	}
	if x.s.launches != 0 {
		t.Fatal("reservation launched process")
	}
	out := x.start(t)
	if !out.Capabilities.OwnedPrimaryVerified || out.Capabilities.SemanticPrompt || out.Capabilities.NativeProjection || out.Capabilities.AccountIdentity {
		t.Fatal("incorrect capability projection")
	}
	v, err := x.e.store.Session(ctx, target.SessionID)
	if err != nil || v.Capabilities.NativeAttach || v.Capabilities.NativeResume || v.Capabilities.SemanticPrompt {
		t.Fatal("inventory overclaimed primary capability")
	}
}
func TestPrimaryDurableSelectionRejectsForgedFields(t *testing.T) {
	for _, field := range []string{"workspace", "thread", "realm", "generation", "host", "operation"} {
		t.Run(field, func(t *testing.T) {
			x := primarySetup(t)
			r := x.r
			switch field {
			case "workspace":
				r.Workspace = x.dir
			case "thread":
				r.NativeThreadID = "other"
			case "realm":
				r.RealmRevision = "other"
			case "generation":
				r.Target.Generation++
			case "host":
				r.Target.HostID = "foreign"
			case "operation":
				r.OperationID = "other"
			}
			if _, err := x.p.Start(context.Background(), r); err == nil || x.s.launches != 0 {
				t.Fatal("forged selection reached supervisor")
			}
		})
	}
}
func TestPrimaryReservationRetryAndDuplicateThread(t *testing.T) {
	x := primarySetup(t)
	in := PrimarySelection{OperationID: "primary", Name: "Primary", Workspace: x.e.workspace, NativeThreadID: "native-thread", RealmRevision: "realm"}
	same, err := x.e.service.PrepareCodexPrimary(context.Background(), in)
	if err != nil || same != x.r {
		t.Fatal("reservation retry changed stable identity")
	}
	in.OperationID = "another"
	in.RealmRevision = "different-label"
	if _, err = x.e.service.PrepareCodexPrimary(context.Background(), in); err == nil {
		t.Fatal("realm relabel bypassed reserved thread")
	}
}
func TestPrimaryLostResumeAckSurvivesDaemonAndJournalRestart(t *testing.T) {
	x := primarySetup(t)
	x.u.failResume = true
	if _, err := x.p.Start(context.Background(), x.r); !errors.Is(err, cc.ErrUncertain) {
		t.Fatal("lost ack not uncertain", err)
	}
	x.j.Close()
	var err error
	x.j, err = cc.OpenJournal(filepath.Join(x.dir, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	x.e.restart(t)
	x.p, err = x.e.service.NewCodexPrimary(x.j, x.s, x.u, x.options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = x.p.Start(context.Background(), x.r); !errors.Is(err, cc.ErrUncertain) {
		t.Fatal("restart replayed uncertain start", err)
	}
	if x.s.launches != 1 || x.u.resumes != 1 {
		t.Fatal("effect replayed")
	}
}
func TestPrimaryStopFenceIsDurableBeforeEffectAndAfterRestart(t *testing.T) {
	x := primarySetup(t)
	out := x.start(t)
	ctx := context.Background()
	x.s.beforeStop = func() {
		v, err := x.e.store.Session(ctx, x.r.Target.SessionID)
		if err != nil || v.Execution.State != "primary_stopping" {
			t.Error("stop effect preceded durable fence")
		}
	}
	x.s.stopErr = errors.New("lost stop reply")
	stop := cc.StopRequest{OperationID: "primary-stop", Target: x.r.Target, Incarnation: out.Primary.Incarnation}
	if _, err := x.p.Stop(ctx, stop); !errors.Is(err, cc.ErrUncertain) {
		t.Fatal("lost stop reply not uncertain", err)
	}
	x.e.restart(t)
	// Inventory reconciliation is not authority and cannot erase the stop fence.
	if _, err := x.e.store.SetState(ctx, x.r.Target.SessionID, x.r.Target.ExecutionID, "primary_ready"); err != nil {
		t.Fatal(err)
	}
	a, _ := x.u.Inspect(ctx, cc.Launch{Request: x.r, Incarnation: out.Primary.Incarnation}, x.s.ref)
	if _, err := x.e.store.CodexAcquire(ctx, cb.WriterRequest{Agent: a.Agent, OperationID: "new-writer", PrincipalID: "owner", ClientID: "client", Kind: cb.SemanticWriter}); err == nil {
		t.Fatal("restart bypassed durable primary fence")
	}
	if err := x.e.store.PublishCodexPrimary(ctx, a.Agent); err == nil {
		t.Fatal("stopped primary republished")
	}
}
func TestPrimaryStopRejectsActiveGrantAndOutstandingEffects(t *testing.T) {
	x := primarySetup(t)
	out := x.start(t)
	ctx := context.Background()
	a, err := x.p.controller.Binding(ctx, x.r.Target)
	if err != nil {
		t.Fatal(err)
	}
	_, err = x.e.store.CodexAcquire(ctx, cb.WriterRequest{Agent: a, OperationID: "writer", PrincipalID: "owner", ClientID: "client", Kind: cb.SemanticWriter})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = x.p.Stop(ctx, cc.StopRequest{OperationID: "stop", Target: x.r.Target, Incarnation: out.Primary.Incarnation}); err == nil || x.s.stops != 0 {
		t.Fatal("active grant failed to block stop")
	}
}
func TestPrimaryLifecycleSerializesWithCommonService(t *testing.T) {
	x := primarySetup(t)
	x.u.entered = make(chan struct{})
	x.u.release = make(chan struct{})
	started := make(chan error, 1)
	go func() { _, err := x.p.Start(context.Background(), x.r); started <- err }()
	<-x.u.entered
	attempted := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		close(attempted)
		_, _ = x.e.service.Attach(context.Background(), protocol.LeaseRequest{SessionID: x.r.Target.SessionID, ExecutionID: x.r.Target.ExecutionID, Generation: x.r.Target.Generation, ClientID: "client", OperationID: "attach"})
		close(finished)
	}()
	<-attempted
	select {
	case <-finished:
		t.Fatal("terminal admission escaped lifecycle gate")
	case <-time.After(20 * time.Millisecond):
	}
	close(x.u.release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	<-finished
}
func TestPrimaryCallbackAnnouncementBlocksLaunch(t *testing.T) {
	x := primarySetup(t)
	x.e.service.codexCallbacks.Add(1)
	defer x.e.service.codexCallbacks.Add(-1)
	if _, err := x.p.Start(context.Background(), x.r); err == nil || x.s.launches != 0 {
		t.Fatal("pending callback overtaken")
	}
}
func TestUnqualifiedOwnedAdapterRefusesBeforeSupervisorLaunch(t *testing.T) {
	x := primarySetup(t)
	if _, err := x.e.service.NewCodexPrimary(x.j, x.s, cc.NewOwnedUpstream(), x.options); !errors.Is(err, cc.ErrUnavailable) || x.s.launches != 0 {
		t.Fatal("unqualified native adapter admitted launch")
	}
}

func TestPrimaryStopRetryRechecksDurableCallback(t *testing.T) {
	x := primarySetup(t)
	out := x.start(t)
	ctx := context.Background()
	a, err := x.p.controller.Binding(ctx, x.r.Target)
	if err != nil {
		t.Fatal(err)
	}
	request := cc.LifecycleRequest{Action: cc.StopPrimary, OperationID: "stop", Target: x.r.Target, Agent: a, NativeThreadID: x.r.NativeThreadID, RealmRevision: x.r.RealmRevision, Workspace: x.r.Workspace}
	if err = x.e.store.FenceCodexPrimaryStop(ctx, request); err != nil {
		t.Fatal(err)
	}
	_, err = x.e.store.CodexCallback(ctx, cb.CallbackRecord{ID: "late-callback", Origin: cb.ConnectionBinding{Agent: a}, DecisionState: cb.Uncertain})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = x.p.Stop(ctx, cc.StopRequest{OperationID: "stop", Target: x.r.Target, Incarnation: out.Primary.Incarnation}); err == nil || x.s.stops != 0 {
		t.Fatal("stop retry skipped newly durable callback")
	}
}
func TestPrimaryReservationSurvivesRestartWithoutProviderEffect(t *testing.T) {
	x := primarySetup(t)
	x.e.restart(t)
	target := x.r.Target
	if broker, err := x.e.store.CodexBound(context.Background(), target.SessionID, target.ExecutionID); err != nil || !broker {
		t.Fatal("reservation lost after restart")
	}
	if x.s.launches != 0 {
		t.Fatal("restart launched reserved primary")
	}
}

func TestPrimaryReconcileSettlesLostStopWithoutReplay(t *testing.T) {
	x := primarySetup(t)
	out := x.start(t)
	ctx := context.Background()
	x.s.stopErr = errors.New("lost reply")
	stop := cc.StopRequest{OperationID: "stop", Target: x.r.Target, Incarnation: out.Primary.Incarnation}
	if _, err := x.p.Stop(ctx, stop); !errors.Is(err, cc.ErrUncertain) {
		t.Fatal(err)
	}
	reconciled, err := x.p.Reconcile(ctx, x.r.Target)
	if err != nil || reconciled.Primary.State != cc.Stopped || reconciled.Capabilities.OwnedPrimaryVerified {
		t.Fatal("stop reconciliation failed", err)
	}
	op, _, err := x.e.store.Operation(ctx, stop.OperationID)
	if err != nil || op.Outcome != domain.Completed {
		t.Fatal("common durable stop not settled")
	}
	v, err := x.e.store.Session(ctx, x.r.Target.SessionID)
	if err != nil || v.Execution.State != "primary_stopped" {
		t.Fatal("inventory stop not settled")
	}
	if _, err = x.p.Stop(ctx, stop); err != nil || x.s.stops != 1 {
		t.Fatal("stop replayed after reconciliation", err)
	}
}
func TestPrimaryRepeatedStartDoesNotEmitIdleEvents(t *testing.T) {
	x := primarySetup(t)
	x.start(t)
	ctx := context.Background()
	v, err := x.e.store.Session(ctx, x.r.Target.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := x.e.store.Events(ctx, v.ThreadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	x.start(t)
	after, err := x.e.store.Events(ctx, v.ThreadID, 0)
	if err != nil || len(before) != len(after) || x.s.launches != 1 || x.u.resumes != 1 {
		t.Fatal("idempotent start generated repeated effects/events")
	}
}

func TestPrimaryCallbackArrivingDuringQuiescenceFencesStopEffect(t *testing.T) {
	x := primarySetup(t)
	out := x.start(t)
	x.u.beforeInspect = func() { x.e.service.codexCallbacks.Add(1) }
	if _, err := x.p.Stop(context.Background(), cc.StopRequest{OperationID: "late-stop", Target: x.r.Target, Incarnation: out.Primary.Incarnation}); err == nil || x.s.stops != 0 {
		t.Fatal("callback announced during inspection was overtaken")
	}
	x.e.service.codexCallbacks.Store(0)
}

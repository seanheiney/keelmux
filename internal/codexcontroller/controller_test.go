package codexcontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"github.com/seanheiney/keelmux/internal/domain"
	runtime "github.com/seanheiney/keelmux/internal/runtime"
)

// These fixtures verify lifecycle ordering/fault handling only. They are not
// native process, capability authentication, realm or wire integration evidence.
type testGate struct {
	mu      sync.Mutex
	in      atomic.Bool
	deny    bool
	entered chan struct{}
}

func (g *testGate) WithPrimaryLifecycle(ctx context.Context, r LifecycleRequest, f func(context.Context) error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.deny {
		return ErrUnavailable
	}
	g.in.Store(true)
	defer g.in.Store(false)
	if g.entered != nil {
		close(g.entered)
		g.entered = nil
	}
	return f(ctx)
}

type testSupervisor struct {
	gate                                      *testGate
	ref                                       domain.RuntimeRef
	alive                                     bool
	launches, stops                           int
	launchErr, lookupErr, inspectErr, stopErr error
	wrongRef, stopAlive                       bool
	writers                                   int
	spec                                      domain.LaunchSpec
}

func (s *testSupervisor) LaunchProcess(ctx context.Context, spec domain.LaunchSpec) (domain.RuntimeRef, error) {
	if !s.gate.in.Load() {
		panic("launch escaped common gate")
	}
	s.launches++
	s.spec = spec
	s.alive = true
	s.ref = domain.RuntimeRef{Backend: "native", Socket: "/owned/supervisor.sock", Session: "supervisor-1", PID: 1234, BootID: "boot-1", ProcessStart: "start-1", ExecutionID: spec.Session.Execution.ID, OperationID: spec.OperationID}
	if s.wrongRef {
		s.ref.ExecutionID = "other"
	}
	return s.ref, s.launchErr
}
func (s *testSupervisor) Lookup(context.Context, domain.ID, domain.ID) (runtime.Observation, error) {
	return runtime.Observation{Ref: s.ref, Alive: s.alive, WritableClients: s.writers}, s.lookupErr
}
func (s *testSupervisor) Inspect(context.Context, domain.RuntimeRef) (runtime.Observation, error) {
	return runtime.Observation{Ref: s.ref, Alive: s.alive, WritableClients: s.writers}, s.inspectErr
}
func (s *testSupervisor) Stop(context.Context, domain.RuntimeRef) error {
	if !s.gate.in.Load() {
		panic("stop escaped common gate")
	}
	s.stops++
	if !s.stopAlive {
		s.alive = false
	}
	return s.stopErr
}

type testUpstream struct {
	resumes, inspects, closes int
	err                       error
	mutate                    func(*Evidence)
	wait                      chan struct{}
	started                   chan struct{}
	journal                   *Journal
}

func (u *testUpstream) evidence(l Launch, r domain.RuntimeRef) Evidence {
	t := l.Request.Target
	v := Evidence{Agent: cb.AgentBinding{HostID: t.HostID, SessionID: t.SessionID, ExecutionID: t.ExecutionID, Generation: t.Generation, AppServerIncarnation: l.Incarnation, AppServerPID: r.PID, AppServerBootID: r.BootID, AppServerProcessStart: r.ProcessStart, ProtocolVersion: ProtocolVersion, NativeThreadID: l.Request.NativeThreadID}, CapabilitySHA256: l.CapabilitySHA256(), SocketIdentity: "inode-1", Idle: true, EmptyQueue: true, EmptyBackground: true}
	if u.mutate != nil {
		u.mutate(&v)
	}
	return v
}
func (u *testUpstream) Resume(ctx context.Context, l Launch, r domain.RuntimeRef) (Evidence, error) {
	u.resumes++
	// Read the durable bytes, not the in-memory map, before any simulated IO.
	b, err := os.ReadFile(filepath.Join(u.journal.dir, "primaries.json"))
	if err != nil {
		panic(err)
	}
	var d disk
	if json.Unmarshal(b, &d) != nil || d.Entries[l.Request.Target.ExecutionID].State != ResumeAttempted {
		panic("resume before durable attempted intent")
	}
	if u.started != nil {
		close(u.started)
	}
	if u.wait != nil {
		select {
		case <-u.wait:
		case <-ctx.Done():
			return Evidence{}, ctx.Err()
		}
	}
	return u.evidence(l, r), u.err
}
func (u *testUpstream) Inspect(ctx context.Context, l Launch, r domain.RuntimeRef) (Evidence, error) {
	u.inspects++
	return u.evidence(l, r), u.err
}
func (u *testUpstream) Close(context.Context, domain.ID) error { u.closes++; return nil }

type fixture struct {
	c *Controller
	j *Journal
	s *testSupervisor
	u *testUpstream
	g *testGate
	r StartRequest
	o Options
}

func setup(t *testing.T) *fixture {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "kc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	binary := filepath.Join(d, "codex")
	data := []byte("fixture executable; never executed")
	if err := os.WriteFile(binary, data, 0700); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(data)
	j, err := OpenJournal(filepath.Join(d, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	g := &testGate{}
	s := &testSupervisor{gate: g}
	u := &testUpstream{journal: j}
	o := Options{Binary: binary, BinarySHA256: hex.EncodeToString(h[:]), Home: d, CodexHome: d, RealmRevision: "realm-1", Timeout: time.Second}
	c, err := New(j, s, u, g, o)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{c: c, j: j, s: s, u: u, g: g, r: StartRequest{OperationID: "start-1", Target: Target{HostID: "host-1", SessionID: "session-1", ExecutionID: "execution-1", Generation: 1}, NativeThreadID: "thread-1", Workspace: d, RealmRevision: "realm-1"}, o: o}
}
func (f *fixture) start(t *testing.T) View {
	t.Helper()
	v, err := f.c.Start(context.Background(), f.r)
	if err != nil || v.State != Ready {
		t.Fatalf("start=%+v %v", v, err)
	}
	return v
}
func (f *fixture) stop(v View) StopRequest {
	return StopRequest{OperationID: "stop-1", Target: f.r.Target, Incarnation: v.Incarnation}
}
func (f *fixture) reopen(t *testing.T) {
	t.Helper()
	f.j.Close()
	j, err := OpenJournal(f.j.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	f.j = j
	f.u.journal = j
	f.c, err = New(j, f.s, f.u, f.g, f.o)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOneShotStartAndStopAcrossRestart(t *testing.T) {
	f := setup(t)
	v := f.start(t)
	f.reopen(t)
	for i := 0; i < 3; i++ {
		got, err := f.c.Start(context.Background(), f.r)
		if err != nil || got.Incarnation != v.Incarnation {
			t.Fatal(got, err)
		}
	}
	if f.s.launches != 1 || f.u.resumes != 1 || !f.s.alive {
		t.Fatal("restart replayed primary or lost lifetime")
	}
	rec, err := f.c.Reconcile(context.Background(), f.r.Target.ExecutionID)
	if err != nil || rec.State != Ready {
		t.Fatal(rec, err)
	}
	stopped, err := f.c.Stop(context.Background(), f.stop(v))
	if err != nil || stopped.State != Stopped {
		t.Fatal(stopped, err)
	}
	f.reopen(t)
	_, err = f.c.Stop(context.Background(), f.stop(v))
	if err != nil || f.s.stops != 1 {
		t.Fatal("stop replayed", err)
	}
	if f.u.closes != 1 {
		t.Fatal("anchor not independently closed")
	}
}

func TestLaunchLostAcknowledgementNeverReplays(t *testing.T) {
	f := setup(t)
	f.s.launchErr = errors.New("private native diagnostic")
	v, err := f.c.Start(context.Background(), f.r)
	if !errors.Is(err, ErrUncertain) || v.State != Uncertain || f.u.resumes != 0 {
		t.Fatal(v, err)
	}
	f.reopen(t)
	f.s.launchErr = nil
	_, err = f.c.Start(context.Background(), f.r)
	if !errors.Is(err, ErrUncertain) || f.s.launches != 1 {
		t.Fatal("uncertain launch replay", err)
	}
	f.u.err = ErrUnavailable
	_, err = f.c.Reconcile(context.Background(), f.r.Target.ExecutionID)
	if !errors.Is(err, ErrUncertain) || f.u.resumes != 0 {
		t.Fatal("reconcile resumed thread", err)
	}
	f.u.err = nil
	v, err = f.c.Reconcile(context.Background(), f.r.Target.ExecutionID)
	if err != nil || v.State != Ready || f.s.launches != 1 || f.u.resumes != 0 {
		t.Fatal(v, err)
	}
}

func TestResumeLostAcknowledgementRequiresExactReadEvidence(t *testing.T) {
	f := setup(t)
	f.u.err = errors.New("connection lost")
	_, err := f.c.Start(context.Background(), f.r)
	if !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	f.reopen(t)
	f.u.err = nil
	f.u.mutate = func(e *Evidence) { e.Agent.NativeThreadID = "other" }
	_, err = f.c.Reconcile(context.Background(), f.r.Target.ExecutionID)
	if !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	f.u.mutate = nil
	v, err := f.c.Reconcile(context.Background(), f.r.Target.ExecutionID)
	if err != nil || v.State != Ready || f.u.resumes != 1 {
		t.Fatal(v, err)
	}
}

func TestLostStopAckReconcilesDeathWithoutSecondStop(t *testing.T) {
	f := setup(t)
	v := f.start(t)
	f.s.stopErr = errors.New("lost acknowledgement")
	_, err := f.c.Stop(context.Background(), f.stop(v))
	if !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	f.reopen(t)
	_, err = f.c.Stop(context.Background(), f.stop(v))
	if !errors.Is(err, ErrUncertain) || f.s.stops != 1 {
		t.Fatal(err)
	}
	v, err = f.c.Reconcile(context.Background(), f.r.Target.ExecutionID)
	if err != nil || v.State != Stopped || f.s.stops != 1 {
		t.Fatal(v, err)
	}
}

func TestUncertainLiveStopDoesNotRearmPrimary(t *testing.T) {
	f := setup(t)
	v := f.start(t)
	f.s.stopAlive = true
	_, err := f.c.Stop(context.Background(), f.stop(v))
	if !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	_, err = f.c.Reconcile(context.Background(), f.r.Target.ExecutionID)
	if !errors.Is(err, ErrUncertain) || f.s.stops != 1 {
		t.Fatal(err)
	}
	a := f.j.entries[f.r.Target.ExecutionID].Evidence.Agent
	if f.c.VerifyAgent(context.Background(), a) == nil {
		t.Fatal("uncertain stop rearmed authority")
	}
}

func TestUnprovenAbsenceAndPIDReuseFence(t *testing.T) {
	for _, kind := range []string{"missing_receipt", "pid_reuse", "boot_change", "incarnation_change"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			f.start(t)
			switch kind {
			case "missing_receipt":
				f.s.lookupErr = ErrUnavailable
			case "pid_reuse":
				f.s.ref.ProcessStart = "new-start"
			case "boot_change":
				f.s.ref.BootID = "new-boot"
			case "incarnation_change":
				f.s.ref.Session = "new-supervisor"
			}
			v, err := f.c.Reconcile(context.Background(), f.r.Target.ExecutionID)
			if !errors.Is(err, ErrUncertain) || v.State != Uncertain || f.s.stops != 0 {
				t.Fatal(v, err)
			}
			r := f.r
			r.Target.ExecutionID = "replacement"
			r.Target.Generation++
			r.OperationID = "replacement-start"
			if _, err = f.c.Start(context.Background(), r); !errors.Is(err, ErrConflict) || f.s.launches != 1 {
				t.Fatal("unknown primary replaced", err)
			}
		})
	}
}

func TestWireEvidenceEveryIdentityComponentRequired(t *testing.T) {
	cases := map[string]func(*Evidence){
		"thread": func(e *Evidence) { e.Agent.NativeThreadID = "other" }, "generation": func(e *Evidence) { e.Agent.Generation++ }, "incarnation": func(e *Evidence) { e.Agent.AppServerIncarnation = "other" }, "pid": func(e *Evidence) { e.Agent.AppServerPID++ }, "boot": func(e *Evidence) { e.Agent.AppServerBootID = "other" }, "start": func(e *Evidence) { e.Agent.AppServerProcessStart = "other" }, "host": func(e *Evidence) { e.Agent.HostID = "other" }, "session": func(e *Evidence) { e.Agent.SessionID = "other" }, "execution": func(e *Evidence) { e.Agent.ExecutionID = "other" }, "version": func(e *Evidence) { e.Agent.ProtocolVersion = "0.160.0" }, "capability": func(e *Evidence) { e.CapabilitySHA256 = strings.Repeat("0", 64) }, "socket": func(e *Evidence) { e.SocketIdentity = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			f.u.mutate = mutate
			v, err := f.c.Start(context.Background(), f.r)
			if !errors.Is(err, ErrUncertain) || v.State != Uncertain {
				t.Fatal(v, err)
			}
		})
	}
}

func TestStopRefusesUnquiescentOrUnknown(t *testing.T) {
	for _, kind := range []string{"busy", "queue", "background", "writer", "missing_wire", "wrong_inode", "wrong_target", "wrong_incarnation", "gate_refused"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			v := f.start(t)
			r := f.stop(v)
			switch kind {
			case "busy":
				f.u.mutate = func(e *Evidence) { e.Idle = false }
			case "queue":
				f.u.mutate = func(e *Evidence) { e.EmptyQueue = false }
			case "background":
				f.u.mutate = func(e *Evidence) { e.EmptyBackground = false }
			case "writer":
				f.s.writers = 1
			case "missing_wire":
				f.u.err = ErrUnavailable
			case "wrong_inode":
				f.u.mutate = func(e *Evidence) { e.SocketIdentity = "replacement-inode" }
			case "wrong_target":
				r.Target.Generation++
			case "wrong_incarnation":
				r.Incarnation = "other"
			case "gate_refused":
				f.g.deny = true
			}
			if _, err := f.c.Stop(context.Background(), r); err == nil || f.s.stops != 0 {
				t.Fatal("stop escaped refusal", err)
			}
		})
	}
}

func TestOperationBindingAndRealmCannotChange(t *testing.T) {
	f := setup(t)
	v := f.start(t)
	for _, mutate := range []func(*StartRequest){func(r *StartRequest) { r.NativeThreadID = "other" }, func(r *StartRequest) { r.Workspace = "/other" }, func(r *StartRequest) { r.Target.Generation++ }, func(r *StartRequest) { r.RealmRevision = "other" }, func(r *StartRequest) { r.Target.ExecutionID = "another" }} {
		r := f.r
		mutate(&r)
		if _, err := f.c.Start(context.Background(), r); err == nil {
			t.Fatal("operation target changed")
		}
	}
	r := f.stop(v)
	r.OperationID = f.r.OperationID
	if _, err := f.c.Stop(context.Background(), r); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if f.s.launches != 1 || f.s.stops != 0 {
		t.Fatal("conflict had effect")
	}
}

func TestGateHeldAcrossLaunchResumeAndStop(t *testing.T) {
	f := setup(t)
	f.u.wait = make(chan struct{})
	f.u.started = make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := f.c.Start(context.Background(), f.r); done <- err }()
	<-f.u.started
	contender := make(chan struct{})
	go func() { f.g.mu.Lock(); close(contender); f.g.mu.Unlock() }()
	select {
	case <-contender:
		t.Fatal("competing authority entered during start effect")
	case <-time.After(20 * time.Millisecond):
	}
	close(f.u.wait)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-contender
	v, err := f.c.Inspect(context.Background(), f.r.Target.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.Stop(context.Background(), f.stop(v)); err != nil {
		t.Fatal(err)
	}
}

func TestDurableCrashStagesNeverReplay(t *testing.T) {
	for _, state := range []State{Recorded, Attempted, ProcessOwned, ResumeAttempted, StopAttempted} {
		t.Run(string(state), func(t *testing.T) {
			f := setup(t)
			v := f.start(t)
			e := f.j.entries[f.r.Target.ExecutionID]
			e.State = state
			if state == StopAttempted {
				e.StopOperation = "stop-1"
			}
			if err := f.j.save(e); err != nil {
				t.Fatal(err)
			}
			f.reopen(t)
			if _, err := f.c.Start(context.Background(), f.r); !errors.Is(err, ErrUncertain) {
				t.Fatal(err)
			}
			if state == StopAttempted {
				if _, err := f.c.Stop(context.Background(), f.stop(v)); !errors.Is(err, ErrUncertain) {
					t.Fatal(err)
				}
			}
			if f.s.launches != 1 || f.u.resumes != 1 || f.s.stops != 0 {
				t.Fatal("crash stage replayed effect")
			}
		})
	}
}

func TestJournalOwnershipPrivacyAndPersistence(t *testing.T) {
	f := setup(t)
	v := f.start(t)
	e := f.j.entries[f.r.Target.ExecutionID]
	if _, err := OpenJournal(f.j.dir); !errors.Is(err, ErrConflict) {
		t.Fatal("second process journal owner accepted", err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{e.Capability, e.Endpoint, e.Home, e.Process.Socket} {
		if strings.Contains(string(data), private) {
			t.Fatal("private value exported")
		}
	}
	if _, err = json.Marshal(e.launch()); err == nil {
		t.Fatal("private launch serialized")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", e.launch(), e.launch()), e.Capability) {
		t.Fatal("private capability formatted")
	}
	i, err := os.Stat(filepath.Join(f.j.dir, "primaries.json"))
	if err != nil || i.Mode().Perm() != 0600 {
		t.Fatal("ledger mode", err)
	}
	f.reopen(t)
	if f.s.stops != 0 || !f.s.alive {
		t.Fatal("daemon/journal close stopped primary")
	}
	got, err := f.c.Inspect(context.Background(), f.r.Target.ExecutionID)
	if err != nil || got.Incarnation != v.Incarnation {
		t.Fatal(got, err)
	}
}

func TestJournalCorruptionSymlinkAndUnavailableFailClosed(t *testing.T) {
	f := setup(t)
	f.start(t)
	dir := f.j.dir
	f.j.Close()
	if err := os.WriteFile(filepath.Join(dir, "primaries.json"), []byte(`{"Version":1,"Entries":{"execution-1":{}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(dir); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	d := filepath.Join(filepath.Dir(dir), "link")
	if err := os.Symlink(dir, d); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(d); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
	if _, err := f.c.Start(context.Background(), f.r); !errors.Is(err, ErrStorage) {
		t.Fatal(err)
	}
}

func TestNilDependenciesAndPinnedBinaryFailClosed(t *testing.T) {
	f := setup(t)
	var nilSupervisor *testSupervisor
	if _, err := New(f.j, nilSupervisor, f.u, f.g, f.o); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := New(f.j, f.s, nil, f.g, f.o); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := New(f.j, f.s, f.u, nil, f.o); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.o.Binary, []byte("changed binary"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Start(context.Background(), f.r); !errors.Is(err, ErrConflict) || f.s.launches != 0 {
		t.Fatal("unreviewed binary launched", err)
	}
}

func TestNoGenericRPCOrPromptSurface(t *testing.T) {
	f := setup(t)
	f.start(t)
	args := strings.Join(f.s.spec.Argv, " ")
	for _, disallowed := range []string{"turn/start", "thread/start", "--config", "--dangerously", "--full-auto", "--ws-token-file"} {
		if strings.Contains(args, disallowed) {
			t.Fatal("unbounded launch override", args)
		}
	}
	e := f.j.entries[f.r.Target.ExecutionID]
	if strings.Contains(args, e.Capability) || !strings.Contains(args, e.launch().CapabilitySHA256()) {
		t.Fatal("launch capability disclosure/binding")
	}
	if f.s.spec.Session.Execution.ProviderVersion != ProtocolVersion {
		t.Fatal("protocol pin missing")
	}
}

func TestSameNativeThreadCannotGetSecondLivePrimary(t *testing.T) {
	f := setup(t)
	f.start(t)
	r := f.r
	r.OperationID = "other-start"
	r.Target.SessionID = "other-session"
	r.Target.ExecutionID = "other-execution"
	if _, err := f.c.Start(context.Background(), r); !errors.Is(err, ErrConflict) || f.s.launches != 1 {
		t.Fatal("same native thread launched twice", err)
	}
}

func TestBindingRequiresFreshExactPrimary(t *testing.T) {
	f := setup(t)
	f.start(t)
	a, err := f.c.Binding(context.Background(), f.r.Target)
	if err != nil || f.c.VerifyAgent(context.Background(), a) != nil {
		t.Fatal(a, err)
	}
	f.s.alive = false
	if _, err := f.c.Binding(context.Background(), f.r.Target); err == nil {
		t.Fatal("dead primary exported as verified")
	}
	if f.c.VerifyAgent(context.Background(), a) == nil {
		t.Fatal("dead primary granted authority")
	}
}

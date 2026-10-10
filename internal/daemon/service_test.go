package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/seanheiney/agent-backplane/internal/domain"
	"github.com/seanheiney/agent-backplane/internal/protocol"
	"github.com/seanheiney/agent-backplane/internal/runtime"
	"github.com/seanheiney/agent-backplane/internal/store"
)

type fixtureBackend struct {
	mu                            sync.Mutex
	refs                          map[domain.ID]domain.RuntimeRef
	sessions                      map[domain.ID]domain.ID
	writers                       map[domain.ID]int
	inspectErrors                 map[domain.ID]error
	launches, stops               int
	launchErr, stopErr, lookupErr error
	launchEffect, stopEffect      bool
	beforeLaunch                  func(domain.LaunchSpec)
	beforeStop                    func(domain.RuntimeRef)
}

func fixture() *fixtureBackend {
	return &fixtureBackend{refs: map[domain.ID]domain.RuntimeRef{}, sessions: map[domain.ID]domain.ID{}, writers: map[domain.ID]int{}, inspectErrors: map[domain.ID]error{}, launchEffect: true, stopEffect: true}
}
func (f *fixtureBackend) Launch(_ context.Context, s domain.LaunchSpec) (domain.RuntimeRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeLaunch != nil {
		f.beforeLaunch(s)
	}
	f.launches++
	ref := domain.RuntimeRef{Backend: "fixture", Socket: "private-fixture/socket", Session: string(s.Session.ID), Pane: "%1", PID: 1000 + f.launches, BootID: "fixture-boot", ProcessStart: fmt.Sprint(f.launches), ExecutionID: s.Session.Execution.ID, OperationID: s.OperationID}
	if f.launchEffect {
		f.refs[ref.ExecutionID] = ref
		f.sessions[ref.ExecutionID] = s.Session.ID
	}
	return ref, f.launchErr
}
func (f *fixtureBackend) Lookup(_ context.Context, e, o domain.ID) (runtime.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lookupErr != nil {
		return runtime.Observation{}, f.lookupErr
	}
	ref, ok := f.refs[e]
	return runtime.Observation{Ref: ref, Alive: ok && ref.OperationID == o, WritableClients: f.writers[e]}, nil
}
func (f *fixtureBackend) Inspect(_ context.Context, ref domain.RuntimeRef) (runtime.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.inspectErrors[ref.ExecutionID]; err != nil {
		return runtime.Observation{}, err
	}
	got, ok := f.refs[ref.ExecutionID]
	return runtime.Observation{Ref: got, Alive: ok, WritableClients: f.writers[ref.ExecutionID]}, nil
}
func (f *fixtureBackend) Stop(_ context.Context, ref domain.RuntimeRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeStop != nil {
		f.beforeStop(ref)
	}
	f.stops++
	if f.stopEffect {
		delete(f.refs, ref.ExecutionID)
		delete(f.writers, ref.ExecutionID)
	}
	return f.stopErr
}
func (f *fixtureBackend) AttachCommand(ref domain.RuntimeRef, read bool) ([]string, error) {
	args := []string{"fixture-attach", ref.Session}
	if read {
		args = append(args, "read-only")
	}
	return args, nil
}
func (f *fixtureBackend) ValidateLink(_ context.Context, session, execution, operation domain.ID, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref, ok := f.refs[execution]
	if !ok || f.sessions[execution] != session || ref.OperationID != operation || token != "private-launch-nonce" {
		return errors.New("secret backend verifier diagnostic")
	}
	return nil
}
func (f *fixtureBackend) setWriters(e domain.ID, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writers[e] = n
}
func (f *fixtureBackend) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.launches, f.stops
}

type env struct {
	service         *Service
	store           *store.Store
	backend         *fixtureBackend
	path, workspace string
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{backend: fixture(), path: filepath.Join(t.TempDir(), "state", "registry.db"), workspace: t.TempDir()}
	var err error
	e.store, err = store.Open(e.path)
	if err != nil {
		t.Fatal(err)
	}
	e.service, err = New(e.store, e.backend, "host")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.store.Close() })
	return e
}
func (e *env) launchRequest(id domain.ID) protocol.LaunchRequest {
	return protocol.LaunchRequest{OperationID: id, Name: "Work", Workspace: e.workspace, Provider: "fixture", ProviderVersion: "v1", Argv: []string{"fixture", "--arg"}}
}
func (e *env) launch(t *testing.T) protocol.LaunchResponse {
	t.Helper()
	v, err := e.service.Launch(context.Background(), e.launchRequest("launch"))
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func (e *env) restart(t *testing.T) {
	t.Helper()
	if err := e.store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	e.store, err = store.Open(e.path)
	if err != nil {
		t.Fatal(err)
	}
	e.service, err = New(e.store, e.backend, "host")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func acquisition(v domain.Session, id domain.ID, client string, readOnly bool) protocol.LeaseRequest {
	return protocol.LeaseRequest{SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation, OperationID: id, ClientID: client, ReadOnly: readOnly}
}
func release(a domain.AttachSpec) protocol.LeaseRequest {
	l := a.Lease
	return protocol.LeaseRequest{SessionID: l.SessionID, ExecutionID: l.ExecutionID, Generation: l.Generation, OperationID: l.OperationID, ClientID: l.ClientID, Token: l.Token, ReadOnly: a.ReadOnly}
}
func assertConflict(t *testing.T, err error) {
	t.Helper()
	var c domain.ConflictError
	if !errors.As(err, &c) {
		t.Fatalf("expected conflict; got %v", err)
	}
}

func TestLaunchPersistsIntentBeforeEffectAndDeduplicatesLostAck(t *testing.T) {
	e := setup(t)
	e.backend.beforeLaunch = func(spec domain.LaunchSpec) {
		op, _, err := e.store.Operation(context.Background(), spec.OperationID)
		if err != nil || op.Outcome != domain.Attempted {
			t.Fatalf("effect without persisted attempt: %#v %v", op, err)
		}
	}
	first := e.launch(t)
	if first.Operation.Outcome != domain.Completed {
		t.Fatal(first.Operation)
	}
	e.restart(t)
	again, err := e.service.Launch(context.Background(), e.launchRequest("launch"))
	if err != nil {
		t.Fatal(err)
	}
	if again.Session.ID != first.Session.ID || again.Session.Execution.ID != first.Session.Execution.ID {
		t.Fatal("retry changed stable identity")
	}
	if n, _ := e.backend.counts(); n != 1 {
		t.Fatalf("launches=%d", n)
	}
	changed := e.launchRequest("launch")
	changed.Argv = []string{"different"}
	_, err = e.service.Launch(context.Background(), changed)
	assertConflict(t, err)
	if first.Session.Execution.ActualModel.Value != "unknown" || first.Session.Execution.Account.Value != "unknown" || first.Session.Execution.NativeThread.Value != "unknown" {
		t.Fatal("inferred native identity")
	}
}

func TestConcurrentLaunchOneEffect(t *testing.T) {
	e := setup(t)
	var wg sync.WaitGroup
	ids := make(chan domain.ID, 24)
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := e.service.Launch(context.Background(), e.launchRequest("same"))
			if err != nil {
				t.Error(err)
				return
			}
			ids <- v.Session.ID
		}()
	}
	wg.Wait()
	close(ids)
	var first domain.ID
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate session")
		}
	}
	if n, _ := e.backend.counts(); n != 1 {
		t.Fatalf("launches=%d", n)
	}
}

func TestLaunchErrorMayHaveEffectAndNeverReplaysAbsentRuntime(t *testing.T) {
	for _, effect := range []bool{true, false} {
		t.Run(fmt.Sprint(effect), func(t *testing.T) {
			e := setup(t)
			e.backend.launchErr = errors.New("ack lost")
			e.backend.launchEffect = effect
			first := e.launch(t)
			want := domain.Uncertain
			if effect {
				want = domain.Completed
			}
			if first.Operation.Outcome != want {
				t.Fatalf("outcome=%s", first.Operation.Outcome)
			}
			e.restart(t)
			again, err := e.service.Launch(context.Background(), e.launchRequest("launch"))
			if err != nil {
				t.Fatal(err)
			}
			if again.Operation.Outcome != want {
				t.Fatalf("retry outcome=%s", again.Operation.Outcome)
			}
			if n, _ := e.backend.counts(); n != 1 {
				t.Fatalf("uncertain launch replayed %d times", n)
			}
		})
	}
}

func TestInterruptedAttemptReconcilesAndRecordedIntentIsNeverSilentlyStarted(t *testing.T) {
	for _, attempt := range []bool{true, false} {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			e := setup(t)
			v := domain.NewSession("host", "Crash", e.workspace, "fixture", "v1")
			op, _, err := e.store.Record(context.Background(), domain.Operation{ID: "crash", Kind: "launch", SessionID: v.ID, ExecutionID: v.Execution.ID}, payload(e.launchRequest("crash")), &v)
			if err != nil {
				t.Fatal(err)
			}
			if attempt {
				if _, _, err = e.store.Attempt(context.Background(), op.ID); err != nil {
					t.Fatal(err)
				}
				if _, err = e.backend.Launch(context.Background(), domain.LaunchSpec{Session: v, OperationID: op.ID, Argv: []string{"fixture"}}); err != nil {
					t.Fatal(err)
				}
			}
			e.restart(t)
			got, _, err := e.store.Operation(context.Background(), op.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := domain.Failed
			if attempt {
				want = domain.Completed
			}
			if got.Outcome != want {
				t.Fatalf("recovered outcome=%s", got.Outcome)
			}
			n, _ := e.backend.counts()
			expected := 0
			if attempt {
				expected = 1
			}
			if n != expected {
				t.Fatalf("reconciliation launched %d", n)
			}
		})
	}
}

func TestLinkRequiresPrivateNonceAndExactLiveProcess(t *testing.T) {
	e := setup(t)
	v := e.launch(t)
	in := protocol.LinkRequest{SessionID: v.Session.ID, ExecutionID: v.Session.Execution.ID, OperationID: v.Operation.ID, Token: "private-launch-nonce"}
	for _, token := range []string{"", "forged"} {
		bad := in
		bad.Token = token
		_, err := e.service.Link(context.Background(), bad)
		assertConflict(t, err)
		if strings.Contains(err.Error(), "secret backend") {
			t.Fatal("leaked private verifier diagnostic")
		}
	}
	linked, err := e.service.Link(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !linked.Linked || !linked.Capabilities.NativeAttach || linked.Capabilities.SemanticPrompt {
		t.Fatal("incorrect managed receipt")
	}
	if _, err = e.service.Link(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	events, err := e.store.Events(context.Background(), linked.ThreadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(event.Detail, in.Token) {
			t.Fatal("nonce in journal")
		}
	}
	e.backend.mu.Lock()
	ref := e.backend.refs[in.ExecutionID]
	ref.ProcessStart = "reused-process"
	e.backend.refs[in.ExecutionID] = ref
	e.backend.mu.Unlock()
	_, err = e.service.Link(context.Background(), in)
	assertConflict(t, err)
}

func TestDurableAttachLostAckWriterSurvivesRestartAndExactRelease(t *testing.T) {
	e := setup(t)
	v := e.launch(t).Session
	in := acquisition(v, "attach", "client", false)
	first, err := e.service.Attach(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	e.restart(t)
	again, err := e.service.Attach(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if again.Lease != first.Lease {
		t.Fatal("lost grant response produced new token")
	}
	_, err = e.service.Attach(context.Background(), acquisition(v, "competitor", "other", false))
	assertConflict(t, err)
	read, err := e.service.Attach(context.Background(), acquisition(v, "read", "reader", true))
	if err != nil {
		t.Fatal(err)
	}
	if !read.ReadOnly || read.Lease.Token == "" {
		t.Fatal("read-only grant missing identity")
	}
	if err = e.service.Release(context.Background(), release(read)); err != nil {
		t.Fatal(err)
	}
	e.backend.setWriters(v.Execution.ID, 1)
	e.restart(t)
	_, err = e.service.Attach(context.Background(), in)
	assertConflict(t, err)
	err = e.service.Release(context.Background(), release(first))
	assertConflict(t, err)
	_, err = e.service.Attach(context.Background(), acquisition(v, "after-restart", "other", false))
	assertConflict(t, err)
	e.backend.setWriters(v.Execution.ID, 0)
	bad := release(first)
	bad.Token = "forged"
	assertConflict(t, e.service.Release(context.Background(), bad))
	bad = release(first)
	bad.Generation++
	assertConflict(t, e.service.Release(context.Background(), bad))
	bad = release(first)
	bad.ClientID = "other"
	assertConflict(t, e.service.Release(context.Background(), bad))
	if err = e.service.Release(context.Background(), release(first)); err != nil {
		t.Fatal(err)
	}
	if err = e.service.Release(context.Background(), release(first)); err != nil {
		t.Fatal(err)
	}
	_, err = e.service.Attach(context.Background(), in)
	assertConflict(t, err)
	if _, err = e.service.Attach(context.Background(), acquisition(v, "new", "other", false)); err != nil {
		t.Fatal(err)
	}
}

func TestNoDatabaseLeaseDoesNotFenceSurvivingNativeWriter(t *testing.T) {
	e := setup(t)
	v := e.launch(t).Session
	e.backend.setWriters(v.Execution.ID, 1)
	e.restart(t)
	_, err := e.service.Attach(context.Background(), acquisition(v, "attach", "client", false))
	assertConflict(t, err)
	if _, err = e.service.Attach(context.Background(), acquisition(v, "read", "client", true)); err != nil {
		t.Fatal(err)
	}
}

func TestStaleStopAndLostAckNeverRepeatsEffect(t *testing.T) {
	for _, effect := range []bool{true, false} {
		t.Run(fmt.Sprint(effect), func(t *testing.T) {
			e := setup(t)
			v := e.launch(t).Session
			in := protocol.SessionRequest{SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: v.Execution.Generation, OperationID: "stop"}
			bad := in
			bad.Generation++
			assertConflict(t, e.service.Stop(context.Background(), bad))
			if _, n := e.backend.counts(); n != 0 {
				t.Fatal("stale stop reached runtime")
			}
			e.backend.stopErr = errors.New("stop ack lost")
			e.backend.stopEffect = effect
			e.backend.beforeStop = func(ref domain.RuntimeRef) {
				op, _, err := e.store.Operation(context.Background(), in.OperationID)
				if err != nil || op.Outcome != domain.Attempted {
					t.Fatal("stop effect before durable attempt")
				}
				if ref.ExecutionID != in.ExecutionID {
					t.Fatal("wrong execution")
				}
			}
			err := e.service.Stop(context.Background(), in)
			if effect && err != nil {
				t.Fatal(err)
			}
			if !effect {
				assertConflict(t, err)
			}
			e.restart(t)
			err = e.service.Stop(context.Background(), in)
			if effect && err != nil {
				t.Fatal(err)
			}
			if !effect {
				assertConflict(t, err)
			}
			if _, n := e.backend.counts(); n != 1 {
				t.Fatalf("stop repeated %d times", n)
			}
			collision := in
			collision.ExecutionID = "replacement"
			assertConflict(t, e.service.Stop(context.Background(), collision))
		})
	}
}

func TestReconcileMissingRuntimePreservesRegistryAndDoesNotRelaunch(t *testing.T) {
	e := setup(t)
	v := e.launch(t).Session
	e.backend.mu.Lock()
	delete(e.backend.refs, v.Execution.ID)
	e.backend.mu.Unlock()
	e.restart(t)
	got, err := e.store.Session(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Execution.State != "missing" || got.ThreadID != v.ThreadID || got.WorkID != v.WorkID {
		t.Fatal("missing runtime lost registry")
	}
	if n, _ := e.backend.counts(); n != 1 {
		t.Fatal("missing execution relaunched")
	}
}

func TestPrivateProtocolHeaderStrictRequestAndRoundtrip(t *testing.T) {
	e := setup(t)
	call := func(method, path, body, version string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Backplane-Protocol", version)
		w := httptest.NewRecorder()
		e.service.Handler().ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/v1/sessions", "", ""); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code)
	}
	for _, body := range []string{`{"unknown":true}`, `{} {}`, strings.Repeat("x", 65<<10)} {
		if w := call("POST", "/v1/launch", body, protocol.Version); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid request status=%d", w.Code)
		}
	}
	b, _ := json.Marshal(e.launchRequest("http"))
	w := call("POST", "/v1/launch", string(b), protocol.Version)
	if w.Code != http.StatusOK {
		t.Fatalf("launch %d %s", w.Code, w.Body.String())
	}
	var launched protocol.LaunchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &launched); err != nil {
		t.Fatal(err)
	}
	w = call("GET", "/v1/sessions", "", protocol.Version)
	var inventory protocol.Inventory
	if err := json.Unmarshal(w.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Version != protocol.Version || len(inventory.Sessions) != 1 || inventory.Sessions[0].ID != launched.Session.ID {
		t.Fatal(inventory)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("private-launch-nonce")) {
		t.Fatal("private nonce in inventory")
	}
}

func TestReconcileIsolatesRuntimeFailuresAndRecoversHealthyEntries(t *testing.T) {
	e := setup(t)
	damaged := e.launch(t).Session
	healthyResult, err := e.service.Launch(context.Background(), e.launchRequest("healthy"))
	if err != nil {
		t.Fatal(err)
	}
	healthy := healthyResult.Session
	e.backend.mu.Lock()
	original := e.backend.refs[damaged.Execution.ID]
	forged := original
	forged.ProcessStart = "forged"
	e.backend.refs[damaged.Execution.ID] = forged
	e.backend.mu.Unlock()
	if err = e.service.Reconcile(context.Background()); err != nil {
		t.Fatalf("one damaged runtime blocked authority: %v", err)
	}
	got, err := e.store.Session(context.Background(), damaged.ID)
	if err != nil || got.Execution.State != "recovery_required" {
		t.Fatalf("damaged projection=%#v err=%v", got, err)
	}
	good, err := e.store.Session(context.Background(), healthy.ID)
	if err != nil || good.Execution.State != "running" {
		t.Fatalf("healthy projection=%#v err=%v", good, err)
	}
	if _, err = e.service.Attach(context.Background(), acquisition(healthy, "healthy-attach", "client", true)); err != nil {
		t.Fatal(err)
	}
	before, err := e.store.Events(context.Background(), damaged.ThreadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := e.store.Events(context.Background(), damaged.ThreadID, 0)
	if err != nil || len(before) != len(after) {
		t.Fatal("unchanged broken runtime repeated event")
	}
	e.backend.mu.Lock()
	e.backend.refs[damaged.Execution.ID] = original
	e.backend.inspectErrors[damaged.Execution.ID] = errors.New("socket unavailable")
	e.backend.mu.Unlock()
	if err = e.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err = e.store.Session(context.Background(), damaged.ID)
	if err != nil || got.Execution.State != "runtime_unavailable" {
		t.Fatal("unavailable runtime still reported running")
	}
	e.backend.mu.Lock()
	delete(e.backend.inspectErrors, damaged.Execution.ID)
	e.backend.mu.Unlock()
	if err = e.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err = e.store.Session(context.Background(), damaged.ID)
	if err != nil || got.Execution.State != "running" {
		t.Fatal("exact runtime was not recovered")
	}
	if launches, _ := e.backend.counts(); launches != 2 {
		t.Fatal("recovery launched a replacement")
	}
}

type backendWithoutLinkVerifier struct{ runtime.Backend }

func TestLinkRequiresVerifierAndBackendSecretsStayOutOfJournal(t *testing.T) {
	e := setup(t)
	e.backend.launchEffect = false
	e.backend.launchErr = errors.New("private-launch-nonce")
	result := e.launch(t)
	b, _ := json.Marshal(result)
	if bytes.Contains(b, []byte("private-launch-nonce")) {
		t.Fatal("backend launch diagnostic leaked into response")
	}
	events, err := e.store.Events(context.Background(), result.Session.ThreadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(event.Detail, "private-launch-nonce") {
			t.Fatal("backend launch diagnostic leaked into journal")
		}
	}
	e.backend.launchEffect = true
	e.backend.launchErr = nil
	managed, err := e.service.Launch(context.Background(), e.launchRequest("managed"))
	if err != nil {
		t.Fatal(err)
	}
	without, err := New(e.store, backendWithoutLinkVerifier{Backend: e.backend}, "host")
	if err != nil {
		t.Fatal(err)
	}
	_, err = without.Link(context.Background(), protocol.LinkRequest{SessionID: managed.Session.ID, ExecutionID: managed.Session.Execution.ID, OperationID: managed.Operation.ID, Token: "private-launch-nonce"})
	assertConflict(t, err)
}

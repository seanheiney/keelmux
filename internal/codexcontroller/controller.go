package codexcontroller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"github.com/seanheiney/keelmux/internal/domain"
	"golang.org/x/sys/unix"
)

type Controller struct {
	journal                         *Journal
	supervisor                      Supervisor
	upstream                        Upstream
	gate                            Gate
	options                         Options
	homeIdentity, codexHomeIdentity directoryIdentity
}

func absent(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Func, reflect.Slice, reflect.Chan:
		return r.IsNil()
	}
	return false
}
func New(j *Journal, s Supervisor, u Upstream, g Gate, o Options) (*Controller, error) {
	if j == nil || absent(s) || absent(u) || absent(g) {
		return nil, ErrUnavailable
	}
	if !cleanPath(o.Binary) || !cleanPath(o.Home) || !cleanPath(o.CodexHome) || !validText(o.RealmRevision) || !digest(o.BinarySHA256) {
		return nil, ErrInvalid
	}
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Timeout < time.Second || o.Timeout > 30*time.Second {
		return nil, ErrInvalid
	}
	canonicalHome, homeIdentity, err := ownedDirectory(o.Home)
	if err != nil {
		return nil, err
	}
	canonicalCodexHome, codexHomeIdentity, err := ownedDirectory(o.CodexHome)
	if err != nil {
		return nil, err
	}
	o.Home, o.CodexHome = canonicalHome, canonicalCodexHome
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.available() {
		return nil, ErrStorage
	}
	return &Controller{journal: j, supervisor: s, upstream: u, gate: g, options: o, homeIdentity: homeIdentity, codexHomeIdentity: codexHomeIdentity}, nil
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,256}$`)

func validText(s string) bool { return identifier.MatchString(s) }
func cleanPath(s string) bool {
	return filepath.IsAbs(s) && filepath.Clean(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func digest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == strings.ToLower(s)
}
func validTarget(t Target) bool {
	return validText(string(t.HostID)) && validText(string(t.SessionID)) && validText(string(t.ExecutionID)) && t.Generation > 0
}
func validRequest(r StartRequest) bool {
	return validTarget(r.Target) && validText(string(r.OperationID)) && validText(r.NativeThreadID) && validText(r.RealmRevision) && cleanPath(r.Workspace)
}
func validEntry(e entry, dir string) bool {
	if !validRequest(e.Request) || !validText(string(e.Incarnation)) || !digest(e.Capability) || !digest(e.BinarySHA256) || !cleanPath(e.Binary) || !cleanPath(e.Home) || !cleanPath(e.CodexHome) || e.HomeIdentity.Inode == 0 || e.CodexHomeIdentity.Inode == 0 || e.Endpoint != filepath.Join(dir, "app-"+string(e.Incarnation)+".sock") {
		return false
	}
	if e.StopOperation != "" && !validText(string(e.StopOperation)) {
		return false
	}
	switch e.State {
	case Recorded, Attempted, ProcessOwned, ResumeAttempted, Ready, StopAttempted, Stopped, Uncertain:
	default:
		return false
	}
	if e.State == Ready && !validEvidence(e, e.Evidence) {
		return false
	}
	if (e.State == StopAttempted || e.State == Stopped) && e.StopOperation == "" {
		return false
	}
	return true
}
func nonce(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", ErrUnavailable
	}
	return hex.EncodeToString(b), nil
}

func (c *Controller) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.options.Timeout)
}
func (c *Controller) gateRun(ctx context.Context, r LifecycleRequest, f func(context.Context) error) error {
	called := false
	var effectErr error
	repeated := false
	err := c.gate.WithPrimaryLifecycle(ctx, r, func(inner context.Context) error {
		if called || inner == nil {
			repeated = true
			return ErrUnavailable
		}
		called = true
		// The trusted gate may shorten but cannot remove the controller deadline.
		d, ok := ctx.Deadline()
		if !ok {
			return ErrUnavailable
		}
		bound, cancel := context.WithDeadline(inner, d)
		defer cancel()
		if err := bound.Err(); err != nil {
			return err
		}
		effectErr = f(bound)
		return effectErr
	})
	if repeated {
		return ErrUnavailable
	}
	if effectErr != nil {
		return effectErr
	}
	if err != nil {
		return ErrUnavailable // Trusted adapters may carry private diagnostics.
	}
	if !called {
		return ErrUnavailable
	}
	return nil
}

// Start launches one foreground primary and resumes one authorized existing
// thread. Repeating an operation returns its durable view; it never respawns or
// repeats resume, even after crashes or missing acknowledgements.
func (c *Controller) Start(ctx context.Context, r StartRequest) (View, error) {
	if !validRequest(r) || r.RealmRevision != c.options.RealmRevision {
		return View{}, ErrInvalid
	}
	ctx, cancel := c.bounded(ctx)
	defer cancel()
	var out View
	err := c.gateRun(ctx, LifecycleRequest{Action: StartPrimary, OperationID: r.OperationID, Target: r.Target, NativeThreadID: r.NativeThreadID, RealmRevision: r.RealmRevision}, func(ctx context.Context) error {
		j := c.journal
		j.mu.Lock()
		defer j.mu.Unlock()
		if !j.available() {
			return ErrStorage
		}
		if !c.sameDirectories() {
			return ErrConflict
		}
		if e, ok := j.entries[r.Target.ExecutionID]; ok {
			out = e.view()
			if e.Request != r || !c.sameOptions(e) {
				return ErrConflict
			}
			if e.State != Ready && e.State != Stopped {
				return ErrUncertain
			}
			return nil
		}
		if j.operationUsed(r.OperationID) {
			return ErrConflict
		}
		// A different generation cannot replace a possibly surviving primary.
		for _, e := range j.entries {
			if e.State != Stopped && (e.Request.Target.SessionID == r.Target.SessionID || e.Request.NativeThreadID == r.NativeThreadID && (e.Request.RealmRevision == r.RealmRevision || e.CodexHome == c.options.CodexHome || e.CodexHomeIdentity == c.codexHomeIdentity)) {
				return ErrConflict
			}
		}
		if err := verifyBinary(c.options.Binary, c.options.BinarySHA256); err != nil {
			return err
		}
		inc, err := nonce(16)
		if err != nil {
			return err
		}
		cap, err := nonce(32)
		if err != nil {
			return err
		}
		e := entry{Request: r, Incarnation: domain.ID(inc), Endpoint: filepath.Join(j.dir, "app-"+inc+".sock"), Binary: c.options.Binary, BinarySHA256: c.options.BinarySHA256, Home: c.options.Home, CodexHome: c.options.CodexHome, Capability: cap, State: Recorded, Reason: "intent_recorded", UpdatedAt: time.Now().UTC()}
		e.HomeIdentity, e.CodexHomeIdentity = c.homeIdentity, c.codexHomeIdentity
		if err = j.save(e); err != nil {
			return err
		}
		out = e.view()
		if err = c.transition(&e, Attempted, "launch_attempted"); err != nil {
			return err
		}
		out = e.view()
		ref, err := c.supervisor.LaunchProcess(ctx, launchSpec(e))
		if err != nil {
			out, err = c.uncertain(&e, "launch_unconfirmed")
			return err
		}
		if !validProcess(e, ref) {
			out, err = c.uncertain(&e, "process_identity_mismatch")
			return err
		}
		e.Process = ref
		if err = c.transition(&e, ProcessOwned, "primary_process_owned"); err != nil {
			return err
		}
		out = e.view()
		// Persist the resume attempt before opening/writing native RPC. An error
		// from Resume can mean partial delivery and is never automatically retried.
		if err = c.transition(&e, ResumeAttempted, "resume_attempted"); err != nil {
			return err
		}
		out = e.view()
		ev, err := c.upstream.Resume(ctx, e.launch(), ref)
		if err != nil || !validEvidence(e, ev) {
			out, err = c.uncertain(&e, "anchor_unconfirmed")
			return err
		}
		if old, err := c.supervisor.Inspect(ctx, ref); err != nil || !old.Alive || old.Ref != ref {
			out, err = c.uncertain(&e, "process_verification_failed")
			return err
		}
		e.Evidence = ev
		if err = c.transition(&e, Ready, "exact_primary_verified"); err != nil {
			return err
		}
		out = e.view()
		return nil
	})
	return out, err
}

func verifyBinary(path, want string) error {
	// An executable replaced by a FIFO must not block the common lifecycle gate
	// before we can inspect its type. Qualified executable symlinks remain valid;
	// the upstream adapter must separately prove the image actually launched.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	i, err := f.Stat()
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0111 == 0 || i.Size() > 512<<20 {
		return ErrUnavailable
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil || hex.EncodeToString(h.Sum(nil)) != want {
		return ErrConflict
	}
	return nil
}
func launchSpec(e entry) domain.LaunchSpec {
	t := e.Request.Target
	return domain.LaunchSpec{Session: domain.Session{ID: t.SessionID, HostID: t.HostID, Workspace: e.Request.Workspace, Mode: domain.Managed, Execution: domain.Execution{ID: t.ExecutionID, Generation: t.Generation, Provider: "codex", ProviderVersion: ProtocolVersion}}, OperationID: e.Request.OperationID, Argv: []string{e.Binary, "app-server", "--listen", "unix://" + e.Endpoint, "--ws-auth", "capability-token", "--ws-token-sha256", e.launch().CapabilitySHA256()}, Env: []string{"HOME=" + e.Home, "CODEX_HOME=" + e.CodexHome}}
}
func validProcess(e entry, r domain.RuntimeRef) bool {
	return r.Backend == "native" && r.PID > 0 && r.BootID != "" && r.ProcessStart != "" && r.Session != "" && cleanPath(r.Socket) && r.ExecutionID == e.Request.Target.ExecutionID && r.OperationID == e.Request.OperationID
}
func validEvidence(e entry, v Evidence) bool {
	a := v.Agent
	t := e.Request.Target
	r := e.Process
	return validProcess(e, r) && a.HostID == t.HostID && a.SessionID == t.SessionID && a.ExecutionID == t.ExecutionID && a.Generation == t.Generation && a.AppServerIncarnation == e.Incarnation && a.AppServerPID == r.PID && a.AppServerBootID == r.BootID && a.AppServerProcessStart == r.ProcessStart && a.ProtocolVersion == ProtocolVersion && a.NativeThreadID == e.Request.NativeThreadID && v.CapabilitySHA256 == e.launch().CapabilitySHA256() && validText(v.SocketIdentity) && (e.Evidence.SocketIdentity == "" || e.Evidence.SocketIdentity == v.SocketIdentity)
}
func (c *Controller) sameOptions(e entry) bool {
	return e.Binary == c.options.Binary && e.BinarySHA256 == c.options.BinarySHA256 && e.Home == c.options.Home && e.CodexHome == c.options.CodexHome && e.HomeIdentity == c.homeIdentity && e.CodexHomeIdentity == c.codexHomeIdentity && e.Request.RealmRevision == c.options.RealmRevision && c.sameDirectories()
}
func (c *Controller) sameDirectories() bool {
	return sameDirectory(c.options.Home, c.homeIdentity) && sameDirectory(c.options.CodexHome, c.codexHomeIdentity)
}
func (c *Controller) transition(e *entry, state State, reason string) error {
	e.State = state
	e.Reason = reason
	e.UpdatedAt = time.Now().UTC()
	return c.journal.save(*e)
}
func (c *Controller) uncertain(e *entry, reason string) (View, error) {
	if err := c.transition(e, Uncertain, reason); err != nil {
		return e.view(), err
	}
	return e.view(), ErrUncertain
}

// Inspect reads only the durable ledger. It makes no live or liveness claim.
func (c *Controller) Inspect(ctx context.Context, id domain.ID) (View, error) {
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
	return e.view(), nil
}

// Reconcile obtains fresh owned process and exact-thread wire evidence without
// launch, resume, turn writes, grant acquisition or a native projection lookup.
// A stopped process resolves a prior stop only; it never authorizes a new start.
func (c *Controller) Reconcile(ctx context.Context, id domain.ID) (View, error) {
	ctx, cancel := c.bounded(ctx)
	defer cancel()
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
	if !c.sameOptions(e) {
		return e.view(), ErrConflict
	}
	if e.State == Stopped {
		return e.view(), nil
	}
	o, err := c.supervisor.Lookup(ctx, id, e.Request.OperationID)
	if err != nil || !validProcess(e, o.Ref) || (e.Process != (domain.RuntimeRef{}) && e.Process != o.Ref) {
		return c.uncertain(&e, "process_verification_failed")
	}
	e.Process = o.Ref
	if !o.Alive {
		if e.StopOperation == "" {
			return c.uncertain(&e, "primary_exited_without_stop_receipt")
		}
		if err = c.transition(&e, Stopped, "stop_observed"); err != nil {
			return e.view(), err
		}
		_ = c.upstream.Close(ctx, e.Incarnation)
		return e.view(), nil
	}
	if e.StopOperation != "" {
		return c.uncertain(&e, "stop_not_observed")
	}
	v, err := c.upstream.Inspect(ctx, e.launch(), o.Ref)
	if err != nil || !validEvidence(e, v) {
		return c.uncertain(&e, "anchor_unconfirmed")
	}
	e.Evidence = v
	if err = c.transition(&e, Ready, "exact_primary_reconciled"); err != nil {
		return e.view(), err
	}
	return e.view(), nil
}

// VerifyAgent is the primary-verification portion of cb.Verifier. Compose it
// with trusted writer/connection verification in the home authority. It does
// not register writers, create grants or accept client-supplied process proofs.
func (c *Controller) VerifyAgent(ctx context.Context, a cb.AgentBinding) error {
	ctx, cancel := c.bounded(ctx)
	defer cancel()
	j := c.journal
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.available() {
		return ErrStorage
	}
	e, ok := j.entries[a.ExecutionID]
	if !ok || e.State != Ready || e.Evidence.Agent != a || !c.sameOptions(e) {
		return ErrConflict
	}
	o, err := c.supervisor.Inspect(ctx, e.Process)
	if err != nil || !o.Alive || o.Ref != e.Process {
		return ErrUnavailable
	}
	v, err := c.upstream.Inspect(ctx, e.launch(), e.Process)
	if err != nil || !validEvidence(e, v) {
		return ErrUnavailable
	}
	return nil
}

// Binding supplies the verified host-internal broker binding. Public inventory
// should use Inspect instead. A caller must still acquire and bind through the
// existing broker authority; this method never creates writer authority.
func (c *Controller) Binding(ctx context.Context, target Target) (cb.AgentBinding, error) {
	ctx, cancel := c.bounded(ctx)
	defer cancel()
	j := c.journal
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.available() {
		return cb.AgentBinding{}, ErrStorage
	}
	e, ok := j.entries[target.ExecutionID]
	if !ok || e.State != Ready || e.Request.Target != target || !c.sameOptions(e) {
		return cb.AgentBinding{}, ErrConflict
	}
	o, err := c.supervisor.Inspect(ctx, e.Process)
	if err != nil || !o.Alive || o.Ref != e.Process {
		return cb.AgentBinding{}, ErrUnavailable
	}
	v, err := c.upstream.Inspect(ctx, e.launch(), e.Process)
	if err != nil || !validEvidence(e, v) {
		return cb.AgentBinding{}, ErrUnavailable
	}
	return v.Agent, nil
}

func (c *Controller) Stop(ctx context.Context, r StopRequest) (View, error) {
	if !validTarget(r.Target) || !validText(string(r.OperationID)) || !validText(string(r.Incarnation)) {
		return View{}, ErrInvalid
	}
	ctx, cancel := c.bounded(ctx)
	defer cancel()
	// Snapshot for gate routing only. Revalidate after entering the gate; never
	// call the common gate while holding the journal mutex (lock order).
	j := c.journal
	j.mu.Lock()
	e, ok := j.entries[r.Target.ExecutionID]
	available := j.available()
	j.mu.Unlock()
	if !available {
		return View{}, ErrStorage
	}
	if !ok {
		return View{}, ErrNotFound
	}
	var out View
	err := c.gateRun(ctx, LifecycleRequest{Action: StopPrimary, OperationID: r.OperationID, Target: r.Target, Agent: e.Evidence.Agent, NativeThreadID: e.Request.NativeThreadID, RealmRevision: e.Request.RealmRevision}, func(ctx context.Context) error {
		j.mu.Lock()
		defer j.mu.Unlock()
		if !j.available() {
			return ErrStorage
		}
		e = j.entries[r.Target.ExecutionID]
		out = e.view()
		if e.Request.Target != r.Target || e.Incarnation != r.Incarnation || !c.sameOptions(e) {
			return ErrConflict
		}
		if e.StopOperation != "" {
			if e.StopOperation != r.OperationID {
				return ErrConflict
			}
			if e.State == Stopped {
				return nil
			}
			return ErrUncertain
		}
		if j.operationUsed(r.OperationID) || e.State != Ready {
			return ErrConflict
		}
		o, err := c.supervisor.Inspect(ctx, e.Process)
		if err != nil || !o.Alive || o.Ref != e.Process || o.WritableClients != 0 {
			return ErrUnavailable
		}
		v, err := c.upstream.Inspect(ctx, e.launch(), e.Process)
		if err != nil || !validEvidence(e, v) || !v.Idle || !v.EmptyQueue || !v.EmptyBackground {
			return ErrUnavailable
		}
		e.StopOperation = r.OperationID
		if err = c.transition(&e, StopAttempted, "stop_attempted"); err != nil {
			return err
		}
		out = e.view()
		if err = c.supervisor.Stop(ctx, e.Process); err != nil {
			out, err = c.uncertain(&e, "stop_unconfirmed")
			return err
		}
		o, err = c.supervisor.Inspect(ctx, e.Process)
		if err != nil || o.Alive || o.Ref != e.Process {
			out, err = c.uncertain(&e, "stop_unconfirmed")
			return err
		}
		if err = c.transition(&e, Stopped, "stop_observed"); err != nil {
			return err
		}
		out = e.view()
		// Anchor loss is independent of process death. Its close result cannot
		// turn verified process death into a claim about broker grant retirement.
		_ = c.upstream.Close(ctx, e.Incarnation)
		return nil
	})
	return out, err
}

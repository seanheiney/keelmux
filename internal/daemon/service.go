// Package daemon serves the private bp/1 host authority. Native runtime effects
// follow durable intent; terminal attachment is not semantic prompt delivery.
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seanheiney/keelmux/internal/adapters/providers"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/runtime"
	"github.com/seanheiney/keelmux/internal/store"
)

type Service struct {
	mu      sync.Mutex
	store   *store.Store
	backend runtime.Backend
	hostID  domain.ID
	// Reader registration announces callbacks before waiting for the actor
	// gate. Admission cannot overtake a callback already awaiting persistence.
	codexCallbacks atomic.Int64
	// Registration and the final handover check/commit share this barrier.
	// A reader releases it before waiting for mu; handover never holds it over IO.
	codexRegistration sync.Mutex
}

func New(st *store.Store, backend runtime.Backend, hostID domain.ID) (*Service, error) {
	if st == nil || backend == nil || hostID == "" {
		return nil, errors.New("daemon requires store, backend and stable host identity")
	}
	sessions, err := st.Sessions(context.Background())
	if err != nil {
		return nil, err
	}
	for _, v := range sessions {
		if v.HostID != hostID {
			return nil, errors.New("registry belongs to a different host authority")
		}
	}
	if err = st.RecoverCodex(context.Background()); err != nil {
		return nil, err
	}
	return &Service{store: st, backend: backend, hostID: hostID}, nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.store.Sessions(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, protocol.Inventory{Version: protocol.Version, Sessions: v})
	})
	mux.HandleFunc("POST /v1/launch", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.LaunchRequest
		if !decode(w, r, &in) {
			return
		}
		v, err := s.Launch(r.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	mux.HandleFunc("POST /v1/link", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.LinkRequest
		if !decode(w, r, &in) {
			return
		}
		v, err := s.Link(r.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	mux.HandleFunc("POST /v1/attach", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.LeaseRequest
		if !decode(w, r, &in) {
			return
		}
		v, err := s.Attach(r.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	mux.HandleFunc("POST /v1/release", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.LeaseRequest
		if !decode(w, r, &in) {
			return
		}
		if err := s.Release(r.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/reconcile", func(w http.ResponseWriter, r *http.Request) {
		var in struct{}
		if !decode(w, r, &in) {
			return
		}
		if err := s.Reconcile(r.Context()); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/stop", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.SessionRequest
		if !decode(w, r, &in) {
			return
		}
		if err := s.Stop(r.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backplane-Protocol", protocol.Version)
		if r.Header.Get("X-Backplane-Protocol") != protocol.Version {
			writeJSON(w, http.StatusBadRequest, protocol.Error{Message: "X-Backplane-Protocol must be bp/1"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type invalidError struct{ message string }

type runtimeIssue struct{ state, detail string }

func (e runtimeIssue) Error() string { return e.detail }

func (e invalidError) Error() string { return e.message }
func invalid(message string) error   { return invalidError{message} }
func conflict(message string) error  { return domain.ConflictError{Reason: message} }
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var c domain.ConflictError
	var b invalidError
	var issue runtimeIssue
	switch {
	case errors.As(err, &c):
		status = http.StatusConflict
	case errors.As(err, &b):
		status = http.StatusBadRequest
	case errors.As(err, &issue):
		status = http.StatusServiceUnavailable
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		status = http.StatusRequestTimeout
	}
	writeJSON(w, status, protocol.Error{Message: err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, invalid("invalid request: "+err.Error()))
		return false
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, invalid("request must contain exactly one JSON value"))
		return false
	}
	return true
}
func payload(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// Post-effect writes survive an HTTP disconnect; they are bounded independently
// of the caller's cancelled request context.
func durableContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (s *Service) Launch(ctx context.Context, in protocol.LaunchRequest) (protocol.LaunchResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out protocol.LaunchResponse
	if in.OperationID == "" || in.Provider == "" || strings.TrimSpace(in.Name) == "" || !filepath.IsAbs(in.Workspace) || len(in.Argv) == 0 || in.Argv[0] == "" {
		return out, invalid("launch requires operation, name, provider, absolute workspace and executable")
	}
	if strings.ContainsRune(in.Workspace, 0) {
		return out, invalid("workspace contains NUL")
	}
	for _, arg := range in.Argv {
		if strings.ContainsRune(arg, 0) {
			return out, invalid("argv contains NUL")
		}
	}
	if in.Library != nil {
		if err := in.Library.Validate(); err != nil {
			return out, invalid(err.Error())
		}
	}
	prior, p, err := s.store.Operation(ctx, in.OperationID)
	if err == nil {
		if prior.Kind != "launch" || !bytes.Equal(p, payload(in)) {
			return out, conflict("operation ID already names a different request")
		}
		if prior.Outcome == domain.Recorded {
			return s.attemptLaunch(ctx, in, prior)
		}
		if prior.Outcome == domain.Attempted || prior.Outcome == domain.Uncertain {
			if err := s.reconcileOperation(ctx, prior); err != nil {
				return out, err
			}
		}
		return s.launchResponse(ctx, in.OperationID)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return out, err
	}
	info, err := os.Stat(in.Workspace)
	if err != nil || !info.IsDir() {
		return out, invalid("workspace must be an existing directory")
	}
	if err := validateLaunchEnvironment(in.Provider); err != nil {
		return out, invalid(err.Error())
	}
	v := domain.NewSession(s.hostID, in.Name, in.Workspace, in.Provider, in.ProviderVersion)
	if in.Library != nil {
		snapshot := *in.Library
		v.Library = &snapshot
	}
	op := domain.Operation{ID: in.OperationID, Kind: "launch", SessionID: v.ID, ExecutionID: v.Execution.ID}
	op, _, err = s.store.Record(ctx, op, payload(in), &v)
	if err != nil {
		return out, err
	}
	return s.attemptLaunch(ctx, in, op)
}
func (s *Service) launchResponse(ctx context.Context, id domain.ID) (protocol.LaunchResponse, error) {
	op, _, err := s.store.Operation(ctx, id)
	if err != nil {
		return protocol.LaunchResponse{}, err
	}
	v, err := s.store.Session(ctx, op.SessionID)
	return protocol.LaunchResponse{Session: v, Operation: op}, err
}
func (s *Service) attemptLaunch(ctx context.Context, in protocol.LaunchRequest, op domain.Operation) (protocol.LaunchResponse, error) {
	if err := validateLaunchEnvironment(in.Provider); err != nil {
		return protocol.LaunchResponse{}, invalid(err.Error())
	}
	op, claimed, err := s.store.Attempt(ctx, op.ID)
	if err != nil {
		return protocol.LaunchResponse{}, err
	}
	if !claimed {
		return s.launchResponse(ctx, op.ID)
	}
	v, err := s.store.Session(ctx, op.SessionID)
	if err != nil {
		return protocol.LaunchResponse{}, err
	}
	ref, effectErr := s.backend.Launch(ctx, domain.LaunchSpec{Session: v, OperationID: op.ID, Argv: in.Argv})
	dc, cancel := durableContext()
	defer cancel()
	if effectErr == nil {
		effectErr = validateRef(ref, v.Execution.ID, op.ID)
	}
	if effectErr != nil {
		v.Execution.State = "outcome_uncertain"
		if _, err = s.store.Resolve(dc, op.ID, domain.Uncertain, "launch acknowledgement missing or runtime evidence invalid; reconcile exact runtime, no automatic replay", &v); err != nil {
			return protocol.LaunchResponse{}, err
		}
		op, _, err = s.store.Operation(dc, op.ID)
		if err != nil {
			return protocol.LaunchResponse{}, err
		}
		if err = s.reconcileOperation(dc, op); err != nil {
			return s.launchResponse(dc, op.ID)
		}
		return s.launchResponse(dc, op.ID)
	}
	v.Execution.Runtime = ref
	v.Execution.State = "running"
	if _, err = s.store.Resolve(dc, op.ID, domain.Completed, "managed runtime launched; provider acceptance and effective account/model remain unknown", &v); err != nil {
		return protocol.LaunchResponse{}, err
	}
	return s.launchResponse(dc, op.ID)
}

// The launch home supplies the inherited environment, which can differ from
// the client. Native PTYs supply TTY input/output. Completed retries have no
// new effect and must not be invalidated by a later environment change.
func validateLaunchEnvironment(provider string) error {
	if provider == "gemini" {
		return providers.ValidateManagedEnvironment(provider, os.LookupEnv)
	}
	return nil
}
func validateRef(ref domain.RuntimeRef, execution, operation domain.ID) error {
	if ref.ExecutionID != execution || ref.OperationID != operation || ref.Backend == "" || ref.Socket == "" || ref.Session == "" || (ref.Backend != "native" && ref.Pane == "") || ref.PID <= 0 || ref.BootID == "" || ref.ProcessStart == "" {
		return conflict("runtime lacks exact execution/operation and process lifetime evidence")
	}
	return nil
}
func inspectMatches(obs runtime.Observation, ref domain.RuntimeRef) error {
	if obs.Alive && obs.Ref != ref {
		return conflict("runtime locator or process lifetime changed")
	}
	return nil
}

func (s *Service) Link(ctx context.Context, in protocol.LinkRequest) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.store.Session(ctx, in.SessionID)
	if err != nil {
		return v, err
	}
	if in.Status && in.Off {
		return v, invalid("status and off cannot be combined")
	}
	if in.ExecutionID == "" || in.OperationID == "" || v.Execution.ID != in.ExecutionID || v.Mode != domain.Managed {
		return v, conflict("link requires exact managed launch context; arbitrary terminals cannot be adopted")
	}
	verifier, ok := s.backend.(runtime.LinkVerifier)
	if !ok || in.Token == "" {
		return v, conflict("link requires private inherited launch correlation; public IDs alone are insufficient")
	}
	if err := verifier.ValidateLink(ctx, in.SessionID, in.ExecutionID, in.OperationID, in.Token); err != nil {
		// The backend error may contain private verifier context. Keep it out of
		// HTTP responses and durable diagnostic material.
		return v, conflict("private launch correlation rejected; use the helper inside the exact managed CLI")
	}
	op, _, err := s.store.Operation(ctx, in.OperationID)
	if err != nil {
		return v, err
	}
	if op.Kind != "launch" || op.SessionID != v.ID || op.ExecutionID != in.ExecutionID || op.Outcome != domain.Completed || v.Execution.Runtime.OperationID != op.ID {
		return v, conflict("link context does not name this confirmed managed launch")
	}
	obs, err := s.backend.Lookup(ctx, in.ExecutionID, in.OperationID)
	if err != nil {
		return v, runtimeIssue{"runtime_unavailable", "exact managed runtime lookup unavailable; reconcile before linking"}
	}
	if !obs.Alive {
		return v, conflict("managed runtime is unavailable; reconcile before linking")
	}
	if err = validateRef(obs.Ref, in.ExecutionID, in.OperationID); err != nil {
		return v, err
	}
	if obs.Ref != v.Execution.Runtime {
		return v, conflict("link runtime identity changed")
	}
	if in.Status {
		return v, nil
	}
	return s.store.SetLinked(ctx, v.ID, v.Execution.ID, !in.Off)
}

func leaseRequest(in protocol.LeaseRequest) domain.Lease {
	return domain.Lease{SessionID: in.SessionID, ExecutionID: in.ExecutionID, Generation: in.Generation, OperationID: in.OperationID, ClientID: in.ClientID, Token: in.Token}
}
func (s *Service) current(ctx context.Context, id, execution domain.ID, generation uint64) (domain.Session, error) {
	v, err := s.store.Session(ctx, id)
	if err != nil {
		return v, err
	}
	if execution == "" || generation == 0 || v.Execution.ID != execution || v.Execution.Generation != generation {
		return v, conflict("stale execution generation")
	}
	return v, nil
}
func (s *Service) Attach(ctx context.Context, in protocol.LeaseRequest) (domain.AttachSpec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out domain.AttachSpec
	if in.ClientID == "" || in.OperationID == "" || in.Token != "" {
		return out, invalid("acquisition requires client and operation; tokens are returned by the authority")
	}
	v, err := s.current(ctx, in.SessionID, in.ExecutionID, in.Generation)
	if err != nil {
		return out, err
	}
	if broker, e := s.store.CodexBound(ctx, v.ID, v.Execution.ID); e != nil {
		return out, e
	} else if broker {
		return out, conflict("broker projection requires a verified home controller")
	}
	if v.Mode != domain.Managed || !v.Capabilities.NativeAttach || v.Execution.State != "running" {
		return out, conflict("native attachment unavailable for this session state and mode")
	}
	if in.ReadOnly && !v.Capabilities.ReadOnlyAttach {
		return out, conflict("read-only native attachment is unsupported for this session")
	}
	// Check an idempotency collision before considering runtime state.
	op, p, err := s.store.Operation(ctx, in.OperationID)
	if err == nil && (op.Kind != "attach" || !bytes.Equal(p, payload(in))) {
		return out, conflict("operation ID already names a different request")
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return out, err
	}
	obs, err := s.backend.Inspect(ctx, v.Execution.Runtime)
	if err != nil {
		return out, runtimeIssue{"runtime_unavailable", "native runtime inspection unavailable; reconcile before attachment"}
	}
	if err = inspectMatches(obs, v.Execution.Runtime); err != nil {
		return out, err
	}
	if !obs.Alive {
		return out, conflict("runtime is missing; reconcile before attachment")
	}
	if !in.ReadOnly && obs.WritableClients != 0 {
		return out, conflict("native writable client survives; detach it before acquiring or recovering input ownership")
	}
	argv, err := s.backend.AttachCommand(v.Execution.Runtime, in.ReadOnly)
	if err != nil {
		return out, runtimeIssue{"runtime_unavailable", "native attach command unavailable; inspect the owned terminal backend"}
	}
	l := leaseRequest(in)
	l.Token = string(domain.NewID("lease"))
	l, err = s.store.Acquire(ctx, l, in.ReadOnly, payload(in))
	if err != nil {
		return out, err
	}
	return domain.AttachSpec{Argv: argv, Lease: l, ReadOnly: in.ReadOnly}, nil
}
func (s *Service) Release(ctx context.Context, in protocol.LeaseRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.Token == "" || in.ClientID == "" || in.OperationID == "" {
		return invalid("release requires the exact acquisition identity and token")
	}
	v, err := s.current(ctx, in.SessionID, in.ExecutionID, in.Generation)
	if err != nil {
		return err
	}
	g, err := s.store.Grant(ctx, in.OperationID)
	if err != nil {
		return err
	}
	if g.Lease != leaseRequest(in) || g.ReadOnly != in.ReadOnly {
		return conflict("release does not match the stored attachment grant")
	}
	if !g.Active {
		return nil
	}
	if _, _, e := s.store.CodexGrant(ctx, in.OperationID); e == nil {
		return conflict("native projection detach cannot release the broker primary writer")
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	if !g.ReadOnly {
		obs, err := s.backend.Inspect(ctx, v.Execution.Runtime)
		if err != nil {
			return runtimeIssue{"runtime_unavailable", "cannot confirm native writer detached; retain ownership until runtime inspection succeeds"}
		}
		if err = inspectMatches(obs, v.Execution.Runtime); err != nil {
			return err
		}
		if obs.WritableClients != 0 {
			return conflict("native writable client survives; detach before releasing ownership")
		}
	}
	return s.store.Release(ctx, leaseRequest(in))
}

func (s *Service) Stop(ctx context.Context, in protocol.SessionRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.OperationID == "" {
		return invalid("stop requires a durable operation ID")
	}
	if broker, e := s.store.CodexBound(ctx, in.SessionID, in.ExecutionID); e != nil {
		return e
	} else if broker {
		return conflict("broker primary stop requires trusted primary lifecycle evidence")
	}
	op, p, err := s.store.Operation(ctx, in.OperationID)
	if err == nil {
		if op.Kind != "stop" || !bytes.Equal(p, payload(in)) {
			return conflict("operation ID already names a different request")
		}
		if op.Outcome == domain.Completed {
			return nil
		}
		if op.Outcome == domain.Attempted || op.Outcome == domain.Uncertain {
			if err := s.reconcileOperation(ctx, op); err != nil {
				return err
			}
			op, _, err = s.store.Operation(ctx, op.ID)
			if err != nil {
				return err
			}
			if op.Outcome == domain.Completed {
				return nil
			}
			return conflict("stop outcome_uncertain; inspect the original runtime before taking another action")
		}
		if op.Outcome == domain.Failed {
			return conflict("previous stop failed: " + op.Detail)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	v, err := s.current(ctx, in.SessionID, in.ExecutionID, in.Generation)
	if err != nil {
		return err
	}
	if v.Mode != domain.Managed || !v.Capabilities.Stop {
		return conflict("stop is not supported for this session mode")
	}
	if v.Execution.Runtime.ExecutionID != in.ExecutionID {
		return conflict("no confirmed runtime for this execution")
	}
	if op.ID == "" {
		op, _, err = s.store.Record(ctx, domain.Operation{ID: in.OperationID, Kind: "stop", SessionID: v.ID, ExecutionID: v.Execution.ID}, payload(in), nil)
		if err != nil {
			return err
		}
	}
	op, claimed, err := s.store.Attempt(ctx, op.ID)
	if err != nil {
		return err
	}
	if !claimed {
		return conflict("stop already attempted; reconcile the outcome")
	}
	effectErr := s.backend.Stop(ctx, v.Execution.Runtime)
	dc, cancel := durableContext()
	defer cancel()
	if effectErr != nil {
		if _, err = s.store.Resolve(dc, op.ID, domain.Uncertain, "stop acknowledgement missing; inspect original runtime, no automatic replay", nil); err != nil {
			return err
		}
		op, _, err = s.store.Operation(dc, op.ID)
		if err != nil {
			return err
		}
		if err = s.reconcileOperation(dc, op); err != nil {
			return err
		}
		op, _, err = s.store.Operation(dc, op.ID)
		if err != nil {
			return err
		}
		if op.Outcome == domain.Completed {
			return nil
		}
		return conflict("stop outcome_uncertain; no automatic replay")
	}
	v.Execution.State = "stopped"
	v.Linked = false
	_, err = s.store.Resolve(dc, op.ID, domain.Completed, "managed runtime stopped", &v)
	return err
}

// Reconcile discovers surviving exact runtimes. It never Launches or Stops one.
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.RecoverCodex(ctx); err != nil {
		return err
	}
	ops, err := s.store.Operations(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, op := range ops {
		if op.Kind != "launch" && op.Kind != "stop" {
			continue
		}
		if broker, e := s.store.CodexBound(ctx, op.SessionID, op.ExecutionID); e != nil {
			failures = append(failures, e)
			continue
		} else if broker {
			continue
		}
		if op.Outcome == domain.Recorded {
			v, e := s.store.Session(ctx, op.SessionID)
			if e != nil {
				failures = append(failures, e)
				continue
			}
			var projection *domain.Session
			if op.Kind == "launch" {
				v.Execution.State = "failed"
				projection = &v
			}
			_, e = s.store.Resolve(ctx, op.ID, domain.Failed, "intent was never attempted; explicitly start a new operation", projection)
			if e != nil {
				failures = append(failures, e)
			}
		} else if op.Outcome == domain.Attempted || op.Outcome == domain.Uncertain {
			if e := s.reconcileOperation(ctx, op); e != nil {
				var issue runtimeIssue
				if errors.As(e, &issue) {
					if _, e = s.store.SetRecovery(ctx, op.SessionID, op.ExecutionID, issue.state, issue.detail); e != nil {
						failures = append(failures, e)
					}
				} else {
					failures = append(failures, e)
				}
			}
		}
	}
	vs, err := s.store.Sessions(ctx)
	if err != nil {
		return err
	}
	for _, v := range vs {
		if broker, e := s.store.CodexBound(ctx, v.ID, v.Execution.ID); e != nil {
			failures = append(failures, e)
			continue
		} else if broker {
			if _, e = s.store.SetRecovery(ctx, v.ID, v.Execution.ID, "recovery_required", "owned app-server primary verification required; native projection survival or exit does not establish primary lifecycle"); e != nil {
				failures = append(failures, e)
			}
			continue
		}
		if v.Execution.Runtime.ExecutionID == "" || v.Execution.State == "stopped" {
			continue
		}
		obs, e := s.backend.Inspect(ctx, v.Execution.Runtime)
		if e != nil {
			if _, e = s.store.SetRecovery(ctx, v.ID, v.Execution.ID, "runtime_unavailable", "runtime inspection unavailable; recover the owned terminal backend before attachment"); e != nil {
				failures = append(failures, e)
			}
			continue
		}
		if e = inspectMatches(obs, v.Execution.Runtime); e != nil {
			if _, e = s.store.SetRecovery(ctx, v.ID, v.Execution.ID, "recovery_required", "live runtime markers or process lifetime changed; verify the original execution before attachment"); e != nil {
				failures = append(failures, e)
			}
			continue
		}
		if !obs.Alive {
			if _, e = s.store.SetState(ctx, v.ID, v.Execution.ID, "missing"); e != nil {
				failures = append(failures, e)
			}
		} else if v.Execution.State != "running" {
			if _, e = s.store.SetState(ctx, v.ID, v.Execution.ID, "running"); e != nil {
				failures = append(failures, e)
			}
		}
	}
	return errors.Join(failures...)
}
func (s *Service) reconcileOperation(ctx context.Context, op domain.Operation) error {
	if broker, e := s.store.CodexBound(ctx, op.SessionID, op.ExecutionID); e != nil {
		return e
	} else if broker {
		return runtimeIssue{"recovery_required", "native runtime cannot establish broker primary lifecycle"}
	}
	v, err := s.store.Session(ctx, op.SessionID)
	if err != nil {
		return err
	}
	if v.Execution.ID != op.ExecutionID {
		return conflict("cannot reconcile an operation against a replacement execution")
	}
	if op.Outcome == domain.Attempted {
		var projection *domain.Session
		if op.Kind == "launch" {
			v.Execution.State = "outcome_uncertain"
			projection = &v
		}
		op, err = s.store.Resolve(ctx, op.ID, domain.Uncertain, "interrupted attempted operation; runtime lookup required, no automatic replay", projection)
		if err != nil {
			return err
		}
	}
	if op.Kind == "launch" {
		obs, err := s.backend.Lookup(ctx, op.ExecutionID, op.ID)
		if err != nil {
			return runtimeIssue{"runtime_unavailable", "exact launch lookup unavailable; outcome remains uncertain and will not be replayed"}
		}
		if !obs.Alive {
			return nil
		}
		if err = validateRef(obs.Ref, op.ExecutionID, op.ID); err != nil {
			return runtimeIssue{"recovery_required", "launch lookup returned conflicting identity; outcome remains uncertain and needs repair"}
		}
		v.Execution.Runtime = obs.Ref
		v.Execution.State = "running"
		_, err = s.store.Resolve(ctx, op.ID, domain.Completed, "exact managed runtime found after uncertain launch; provider acceptance remains unknown", &v)
		return err
	}
	if op.Kind == "stop" {
		obs, err := s.backend.Inspect(ctx, v.Execution.Runtime)
		if err != nil {
			return runtimeIssue{"runtime_unavailable", "stop inspection unavailable; outcome remains uncertain and will not be replayed"}
		}
		if err = inspectMatches(obs, v.Execution.Runtime); err != nil {
			return runtimeIssue{"recovery_required", "stop inspection returned conflicting identity; original runtime needs repair"}
		}
		if obs.Alive {
			return nil
		}
		v.Execution.State = "stopped"
		v.Linked = false
		_, err = s.store.Resolve(ctx, op.ID, domain.Completed, "original managed runtime absent after uncertain stop", &v)
		return err
	}
	return fmt.Errorf("unsupported operation kind %q", op.Kind)
}

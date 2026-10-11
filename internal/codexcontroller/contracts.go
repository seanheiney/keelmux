// Package codexcontroller owns a bounded, durable Codex primary lifecycle.
// It neither grants writer authority nor owns disposable native UI processes.
package codexcontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	cb "github.com/seanheiney/keelmux/internal/codexbroker"
	"github.com/seanheiney/keelmux/internal/domain"
	runtime "github.com/seanheiney/keelmux/internal/runtime"
)

const ProtocolVersion = "0.159.0"

var (
	ErrInvalid     = errors.New("invalid Codex primary request")
	ErrConflict    = errors.New("Codex primary identity conflict")
	ErrUnavailable = errors.New("verified Codex primary dependency unavailable")
	ErrUncertain   = errors.New("Codex primary effect uncertain; reconcile without replay")
	ErrNotFound    = errors.New("Codex primary not found")
	ErrStorage     = errors.New("Codex primary journal unavailable")
)

type Target struct {
	HostID, SessionID, ExecutionID domain.ID
	Generation                     uint64
}

// StartRequest comes from the trusted home controller. NativeThreadID is an
// existing, explicitly authorized conversation in RealmRevision. No discovery,
// account inspection, thread creation or arbitrary launch overrides are offered.
type StartRequest struct {
	OperationID                              domain.ID
	Target                                   Target
	NativeThreadID, Workspace, RealmRevision string
}

type Action string

const (
	StartPrimary Action = "start"
	StopPrimary  Action = "stop"
)

type LifecycleRequest struct {
	Action                                   Action
	OperationID                              domain.ID
	Target                                   Target
	Agent                                    cb.AgentBinding // Complete exact binding for stop; zero for start.
	NativeThreadID, RealmRevision, Workspace string
	// StartIncarnation is generated once before start admission. The common
	// store must durably claim it before invoking the start callback, and an
	// already claimed incarnation must never authorize a second callback.
	StartIncarnation domain.ID `json:",omitempty"`
}

// Gate is an adapter on the EXISTING Service/store authority, never another
// lease or a controller-only mutex. It validates current managed execution and
// exact thread/realm authorization, then holds the common admission gate through
// the bounded callback. Start must atomically claim StartIncarnation once in the
// common durable store before invoking the callback; an existing claim refuses
// callback execution, even when its incarnation matches. Stop must reject
// unresolved effects, callbacks, queued work or writable projections and durably
// fence admission before the callback. Errors before the callback must have no
// external lifecycle effect. Nil fails closed.
// Product wiring must implement and test this contract; this package cannot
// safely synthesize it from separate Authority.Observe and Authority.Revoke calls.
type Gate interface {
	WithPrimaryLifecycle(context.Context, LifecycleRequest, func(context.Context) error) error
}

// Supervisor owns a foreground child independently of the daemon and UI. Lookup
// must use a durable launch receipt, not adopt an inferred PID or socket. A lost
// receipt/helper is UNKNOWN, never proof of absence. Each method is bounded.
// nativepty.Backend at the pinned source implements this interface directly.
type Supervisor interface {
	LaunchProcess(context.Context, domain.LaunchSpec) (domain.RuntimeRef, error)
	Lookup(context.Context, domain.ID, domain.ID) (runtime.Observation, error)
	Inspect(context.Context, domain.RuntimeRef) (runtime.Observation, error)
	Stop(context.Context, domain.RuntimeRef) error
}

// Launch is host-internal and must never be serialized into inventory/chat.
// Its per-launch capability is distinct from the native supervisor capability.
type Launch struct {
	Request                                         StartRequest
	Incarnation                                     domain.ID
	Endpoint, Binary, BinarySHA256, Home, CodexHome string
	capability                                      string
}

func (l Launch) Capability() string { return l.capability }
func (l Launch) CapabilitySHA256() string {
	h := sha256.Sum256([]byte(l.capability))
	return hex.EncodeToString(h[:])
}

// MarshalJSON refuses accidental export, including through fmt-backed JSON logs.
func (Launch) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }
func (Launch) String() string               { return "[private Codex launch]" }
func (Launch) GoString() string             { return "[private Codex launch]" }

// Evidence is produced by an injected TRUSTED owned-wire implementation. It
// must verify private socket owner/mode/inode, the actual connected peer PID,
// boot/process start and capability authentication on that same connection.
// Resume uses only initialize/initialized and pinned exact-thread resume; Inspect
// uses only scoped read/queue/background queries. Neither calls a model. Unknown
// callbacks and missing evidence fail closed. SocketIdentity is opaque and local.
type Evidence struct {
	Agent                             cb.AgentBinding
	CapabilitySHA256, SocketIdentity  string
	Idle, EmptyQueue, EmptyBackground bool
}

// Upstream retains the primary anchor independently of every disposable UI.
// Resume is invoked at most ONCE per launch, after a durable attempted record.
// Inspect may reconnect read-only but may not resume/start a thread or turn.
// Close releases only the anchor; it must never stop the app-server process.
// An implementation is a separate real-wire integration gate, not a fixture.
type Upstream interface {
	Resume(context.Context, Launch, domain.RuntimeRef) (Evidence, error)
	Inspect(context.Context, Launch, domain.RuntimeRef) (Evidence, error)
	Close(context.Context, domain.ID) error
}

type State string

const (
	Recorded        State = "recorded"
	Attempted       State = "attempted"
	ProcessOwned    State = "process_owned"
	ResumeAttempted State = "resume_attempted"
	Ready           State = "ready"
	StopAttempted   State = "stop_attempted"
	Stopped         State = "stopped"
	Uncertain       State = "outcome_uncertain"
)

// View intentionally contains no capability, process/endpoint/home paths,
// account/model attribution, writer token or upstream diagnostic strings.
type View struct {
	Target                                         Target
	StartOperationID, StopOperationID, Incarnation domain.ID
	NativeThreadID                                 string
	State                                          State
	Reason                                         string
	UpdatedAt                                      time.Time
}

type StopRequest struct {
	OperationID domain.ID
	Target      Target
	Incarnation domain.ID
}

// Options are trusted local installation configuration, not request parameters.
// BinarySHA256 and ProtocolVersion must come from read-only installed-binary
// qualification; version text or a caller supplied digest alone is not proof.
// Home and CodexHome must be existing directories owned by the effective local
// user. New canonicalizes their paths and pins their filesystem identities.
type Options struct {
	Binary, BinarySHA256, Home, CodexHome, RealmRevision string
	Timeout                                              time.Duration
}

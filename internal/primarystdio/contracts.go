// Package primarystdio defines the private supervisor-owned Codex control
// channel. These records are host-local evidence, never inventory or chat DTOs.
// A complete import is not a writer grant or native callback approval.
package primarystdio

import (
	"context"
	"errors"

	"github.com/seanheiney/keelmux/internal/domain"
)

const (
	Version           = 1
	MaxFrameBytes     = 1 << 20
	MaxExchangeFrames = 128
	MaxExchangeBytes  = 8 << 20
	MaxEvents         = 64
	MaxEventBytes     = 8 << 20
	MaxStderrTail     = 64 << 10
	MaxStderrBytes    = 8 << 20
)

var (
	ErrInvalid     = errors.New("invalid primary stdio evidence")
	ErrConflict    = errors.New("primary stdio identity or cursor conflict")
	ErrUnavailable = errors.New("primary stdio unavailable")
	ErrUncertain   = errors.New("primary stdio uncertain; reconcile without replay")
	ErrStorage     = errors.New("primary stdio evidence storage unavailable")
)

type Profile string

const Codex0159 Profile = "codex_0.159.0"

type Binding struct {
	HostID, SessionID, ExecutionID       domain.ID
	Generation                           uint64
	StartOperationID, PrimaryIncarnation domain.ID
	NativeThreadID, RealmRevision        string
}

// CleanEnvironment has no arbitrary keys and never extends os.Environ.
// Private directories are not filesystem, network or account confinement.
type CleanEnvironment struct {
	Home, CodexHome, XDGConfigHome, XDGCacheHome, XDGDataHome, TempDir string
	Path, Locale                                                       string
}

type LaunchSpec struct {
	Binding                                       Binding
	Workspace, Binary, BinarySHA256, SchemaSHA256 string
	Profile                                       Profile
	Environment                                   CleanEnvironment
}

type ChannelRef struct {
	Runtime                                domain.RuntimeRef
	PrimaryIncarnation, ChannelIncarnation domain.ID
}

// Channel pins one launch, exact child lifetime and pipe incarnation. It is
// neither a socket-authentication proof nor permission to replace a lost child.
type Channel struct {
	Version int
	Spec    LaunchSpec
	Ref     ChannelRef
}

type Cursor struct {
	ChannelIncarnation domain.ID
	Seq                uint64
	ChainSHA256        string
}

type EventKind string

const (
	EventResult      EventKind = "result"
	EventNotice      EventKind = "notice"
	EventCallback    EventKind = "callback"
	EventUnsupported EventKind = "unsupported"
	EventMalformed   EventKind = "malformed"
	EventGap         EventKind = "gap"
)

type ReasonCode string

const (
	ReasonNone            ReasonCode = "none"
	ReasonPendingCallback ReasonCode = "pending_callback"
	ReasonUnsupported     ReasonCode = "unsupported_frame"
	ReasonMalformed       ReasonCode = "malformed_frame"
	ReasonCorrelation     ReasonCode = "correlation_unknown"
	ReasonOverflow        ReasonCode = "evidence_overflow"
	ReasonStorage         ReasonCode = "storage_unavailable"
	ReasonPartialEOF      ReasonCode = "partial_eof"
	ReasonIdentity        ReasonCode = "identity_unknown"
	ReasonTimeout         ReasonCode = "request_uncertain"
)

// NativeID is diagnostic correlation metadata only; no callback answer API
// accepts it. Its tagged value preserves native string versus int64 identity.
type NativeID struct {
	Kind string
	Text string
}

type Event struct {
	Cursor      Cursor
	Kind        EventKind
	Reason      ReasonCode
	FrameSHA256 string
	FrameBytes  uint32
	RequestID   NativeID
}

type Continuity string

const (
	ContinuityComplete Continuity = "complete"
	ContinuityUnknown  Continuity = "unknown"
	ContinuityBroken   Continuity = "broken"
)

type BootstrapState string

const (
	BootstrapUnsent    BootstrapState = "unsent"
	BootstrapAttempted BootstrapState = "attempted"
	BootstrapComplete  BootstrapState = "complete"
	BootstrapUncertain BootstrapState = "uncertain"
)

type Snapshot struct {
	Channel                                              ChannelRef
	Binding                                              Binding
	Cursor                                               Cursor
	SnapshotSHA256                                       string
	Continuity                                           Continuity
	Bootstrap                                            BootstrapState
	Pending                                              bool
	ProcessAlive, ExactImage, ExactDirectories           bool
	ExactLoadedThread, Idle, EmptyQueue, EmptyBackground bool
	StderrTruncated                                      bool
}

type Observation struct {
	Snapshot      Snapshot
	From, Through Cursor
	BatchSHA256   string
	Events        []Event
	More          bool
}

type ImportAck struct {
	From, Through Cursor
	BatchSHA256   string
}

type Quiescence struct {
	Cursor         Cursor
	SnapshotSHA256 string
}

type StopRequest struct {
	OperationID domain.ID
	Quiescence  Quiescence
}

// Imports commit a contiguous, immutable prefix in the EXISTING common store.
// A receipt acknowledges evidence only and never retires a blocking event.
type ImportBatch struct {
	ImportID    domain.ID
	Channel     Channel
	Observation Observation
}
type ImportReceipt struct {
	ImportID      domain.ID
	Channel       ChannelRef
	From, Through Cursor
	BatchSHA256   string
}
type Ledger struct {
	Bound           bool
	Channel         Channel
	Cursor          Cursor
	ImportCount     uint64
	LastImportID    domain.ID
	LastBatchSHA256 string
	Continuity      Continuity
	Pending         bool
	Snapshot        Snapshot
}

// Supervisor retains native pipes across daemon IPC loss. Bootstrap attempts
// are durable before bytes and never replayed. Observe sends only fixed reads.
// Quiescence is a bounded same-connection receive-registration barrier; its
// closure is local code, never serialized. Lost guard acknowledgement is
// uncertainty even when the local database commit may have succeeded.
type Supervisor interface {
	LaunchStdio(context.Context, LaunchSpec) (ChannelRef, error)
	BootstrapStdioOnce(context.Context, ChannelRef) (Observation, error)
	ObserveStdio(context.Context, ChannelRef, Cursor) (Observation, error)
	AckStdioImported(context.Context, ChannelRef, ImportAck) error
	WithStdioQuiescence(context.Context, ChannelRef, Quiescence, func(context.Context) error) error
	StopStdio(context.Context, ChannelRef, StopRequest) error
}

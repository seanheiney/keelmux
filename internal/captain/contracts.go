// Package captain defines the bounded local lifecycle-observation ledger.
// It does not invoke models, dispatch work, read terminal text, or grant native
// authority. Store callers must supply an authenticated identity out of band;
// these contracts are deliberately not exposed by the current Service or wire.
package captain

import (
	"context"
	"errors"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
)

const (
	Version         = 1
	PolicyVersion   = 1
	MaxPrincipals   = 64
	MaxGrants       = 256
	MaxAssignments  = 64
	MaxCommands     = 4096
	MaxSources      = 8
	MaxReaders      = 16
	MaxScanRows     = 256       // Total across all sources, not per source.
	MaxScanBytes    = 128 << 10 // Original source bytes, checked before retention.
	MaxRecordBytes  = 512 << 10
	MaxEntries      = 1024     // Finite assignment lifetime; no automatic compaction.
	MaxLedgerBytes  = 32 << 20 // Selected immutable entry bytes per assignment.
	MaxCommandBytes = 8 << 20  // All retained configuration/control receipts.
	// IDs use domain.ValidObjectID with these fixed namespace prefixes.
	PrincipalIDKind   = "captain_principal"
	GrantIDKind       = "captain_grant"
	AssignmentIDKind  = "captain_assignment"
	RequestIDKind     = "captain_request"
	ReservationIDKind = "captain_reservation"
	ReportIDKind      = "captain_report"
)

var (
	ErrDenied   = errors.New("captain scope denied")
	ErrConflict = errors.New("captain revision or identity conflict")
	ErrBudget   = errors.New("captain reservation budget exhausted")
	ErrCapacity = errors.New("captain finite ledger capacity reached")
	ErrCorrupt  = errors.New("captain ledger requires repair")
	ErrNotFound = errors.New("captain record not found")
)

type Role string

const (
	Owner  Role = "owner"
	Runner Role = "runner"
	Reader Role = "reader"
)

// Target is a current local execution fence, never attribution for old events.
type Target struct {
	HostID      domain.ID `json:"host_id"`
	SessionID   domain.ID `json:"session_id"`
	ThreadID    domain.ID `json:"thread_id"`
	ExecutionID domain.ID `json:"execution_id"`
	Generation  uint64    `json:"generation"`
}

// AuthCaller is trusted Store-only context, not a request-body credential.
// Role/revision are independently checked against the current principal row.
// RunnerTarget must exactly equal the assignment's current captain execution.
// A same-OS-user client cannot be isolated by these cooperating ledger checks.
type AuthCaller struct {
	PrincipalID  domain.ID
	Role         Role
	RoleRevision uint64
	RunnerTarget Target
}

type Ref struct {
	ID       domain.ID `json:"id"`
	Revision uint64    `json:"revision"`
}

type Principal struct {
	Version uint32 `json:"version"`
	Ref     Ref    `json:"ref"`
	Roles   []Role `json:"roles"` // Sorted unique explicit roles, at most three.
	Active  bool   `json:"active"`
}

type Source struct {
	Target        Target `json:"target"`
	FirstSequence int64  `json:"first_sequence"` // Explicit read floor, >= 1.
}

// ReadGrant is directional owner -> recipient. Its identity/source never change.
// Revocation is permanent for its ID; a newly reviewed grant needs a new ID.
type ReadGrant struct {
	Version            uint32    `json:"version"`
	Ref                Ref       `json:"ref"`
	OwnerPrincipal     domain.ID `json:"owner_principal"`
	RecipientPrincipal domain.ID `json:"recipient_principal"`
	Source             Source    `json:"source"`
	Active             bool      `json:"active"`
}

type ReaderScope struct {
	PrincipalID domain.ID `json:"principal_id"`
	Grants      []Ref     `json:"grants"` // This reader's independent exact source grants.
}

type Control string

const (
	Active  Control = "active"
	Paused  Control = "paused"
	Revoked Control = "revoked"
)

// Assignment identity/configuration/cap are immutable; control changes Ref.
// Group membership is intentionally absent: it cannot confer read authority.
type Assignment struct {
	Version         uint32        `json:"version"`
	Ref             Ref           `json:"ref"`
	OwnerPrincipal  domain.ID     `json:"owner_principal"`
	RunnerPrincipal domain.ID     `json:"runner_principal"`
	Captain         Target        `json:"captain"`
	Grants          []Ref         `json:"grants"`  // Sorted unique runner grants, <= MaxSources.
	Readers         []ReaderScope `json:"readers"` // Sorted, bounded explicit audience.
	PolicyVersion   uint32        `json:"policy_version"`
	MaxReservations uint32        `json:"max_reservations"`
	Control         Control       `json:"control"`
}

type Position struct {
	Grant        Ref    `json:"grant"`
	Through      int64  `json:"through"`
	AnchorDigest string `json:"anchor_digest"` // Genesis or exact consumed row.
}

type EventKey struct {
	HostID   domain.ID `json:"host_id"`
	ThreadID domain.ID `json:"thread_id"`
	Sequence int64     `json:"sequence"` // Only local lifecycle stream in version 1.
}

type Disposition string

const (
	Meaningful  Disposition = "meaningful"
	Quiet       Disposition = "quiet"
	Unsupported Disposition = "unsupported"
)

// Event is a finite projection: no Detail, names, paths, terminal/account text.
// RowDigest binds the original row, including discarded fields. Historical
// origin execution/generation is unknown in v1 even when Target is current.
type Event struct {
	Key         EventKey    `json:"key"`
	SessionID   domain.ID   `json:"session_id"`
	OperationID domain.ID   `json:"operation_id"`
	RowDigest   string      `json:"row_digest"`
	Kind        string      `json:"kind"` // Exact known kind, or "unsupported".
	Category    string      `json:"category"`
	Disposition Disposition `json:"disposition"`
	At          time.Time   `json:"at"`
}

type Coverage struct {
	Kind              string `json:"kind"`              // "observed_lifecycle_subset".
	CompleteHistory   bool   `json:"complete_history"`  // Always false.
	NativeOutput      string `json:"native_output"`     // "none".
	HistoricalOrigin  string `json:"historical_origin"` // "unknown".
	UnsupportedEvents uint32 `json:"unsupported_events"`
}

// Scan is untrusted until Store re-reads every scoped row and cursor preimage
// in the same transaction as a checkpoint/reservation. Events includes quiet
// and unsupported rows, so source digests cover the complete selected prefix.
type Scan struct {
	Version     uint32     `json:"version"`
	Assignment  Ref        `json:"assignment"`
	Before      []Position `json:"before"`
	Through     []Position `json:"through"`
	Events      []Event    `json:"events"`
	SourceBytes uint32     `json:"source_bytes"`
	More        bool       `json:"more"`
	Coverage    Coverage   `json:"coverage"`
	Digest      string     `json:"digest"`
}

type ReservationRef struct {
	ID     domain.ID `json:"id"`
	Digest string    `json:"digest"`
}

type Reservation struct {
	Version         uint32         `json:"version"`
	Ref             ReservationRef `json:"ref"`
	Assignment      Ref            `json:"assignment"`
	Ordinal         uint32         `json:"ordinal"`
	RunnerPrincipal domain.ID      `json:"runner_principal"`
	RoleRevision    uint64         `json:"role_revision"`
	Scan            Scan           `json:"scan"`
	CreatedAt       time.Time      `json:"created_at"`
}

type Finding struct {
	Category    string     `json:"category"`
	SessionID   domain.ID  `json:"session_id"`
	OperationID domain.ID  `json:"operation_id"`
	Sources     []EventKey `json:"sources"` // Every meaningful key exactly once.
}

type Usage struct {
	ReservationsConsumed uint32  `json:"reservations_consumed"`
	NativeInvocation     string  `json:"native_invocation"`     // "not_invoked".
	ActualProviderTurns  *uint64 `json:"actual_provider_turns"` // nil = unknown.
	ActualTokens         *uint64 `json:"actual_tokens"`         // nil = unknown; independent usage.
	ActualCharges        string  `json:"actual_charges"`        // "unknown", no monetary promise.
}

// Report is constructed by Store from the reserved typed scan, not supplied
// free-form prose or a producer. All-source cursor publication is atomic.
type Report struct {
	Version     uint32         `json:"version"`
	ID          domain.ID      `json:"id"`
	Assignment  Ref            `json:"assignment"`
	Reservation ReservationRef `json:"reservation"`
	Scan        Scan           `json:"scan"`
	Findings    []Finding      `json:"findings"`
	CommittedAt time.Time      `json:"committed_at"`
	Usage       Usage          `json:"usage"`
	Digest      string         `json:"digest"`
}

// Status retains the journal anchor. Reconstruct and validate every entry
// before trusting cursors, reservations, pending work, or a matched retry.
type Status struct {
	Assignment           Assignment      `json:"assignment"`
	Cursors              []Position      `json:"cursors"`
	Coverage             Coverage        `json:"coverage"` // Cumulative committed prefix.
	ReservationsConsumed uint32          `json:"reservations_consumed"`
	Pending              *ReservationRef `json:"pending"`
	EntryCount           uint32          `json:"entry_count"`
	EntryDigest          string          `json:"entry_digest"`
	StoredBytes          uint64          `json:"stored_bytes"`
}

type Mutation struct {
	RequestID        domain.ID
	ExpectedRevision uint64 // Zero for create; exact current revision otherwise.
}

// Repository is a Store-only contract. Bootstrap is explicit empty-ledger owner
// initialization at the trusted local boundary; migration seeds nothing.
// Every other read/publication checks current independent roles/grants/control
// and exact execution generations. A historical retry is not current authority.
// MaxReservations counts accepted lifetime slots, without refunds on cancel,
// pause or restart; none of these methods invokes a provider or authorizes one.
type Repository interface {
	BootstrapCaptainOwner(context.Context, domain.ID) (Principal, error)
	PutCaptainPrincipal(context.Context, AuthCaller, Mutation, Principal) (Principal, error)
	PutCaptainGrant(context.Context, AuthCaller, Mutation, ReadGrant) (ReadGrant, error)
	CreateCaptainAssignment(context.Context, AuthCaller, Mutation, Assignment) (Status, error)
	SetCaptainControl(context.Context, AuthCaller, Mutation, Ref, Control) (Status, error)
	ScanCaptainEvents(context.Context, AuthCaller, Ref) (Scan, error)
	CheckpointCaptainQuiet(context.Context, AuthCaller, domain.ID, Scan) (Status, error)
	ReserveCaptainWork(context.Context, AuthCaller, domain.ID, Scan) (Reservation, error)
	CommitCaptainReport(context.Context, AuthCaller, ReservationRef, domain.ID) (Report, error)
	CancelCaptainReservation(context.Context, AuthCaller, domain.ID, Ref, ReservationRef) (Status, error)
	ReadCaptainReport(context.Context, AuthCaller, Ref, domain.ID) (Report, error)
	ReadCaptainStatus(context.Context, AuthCaller, Ref) (Status, error)
}

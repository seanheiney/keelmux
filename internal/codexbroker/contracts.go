// Package codexbroker specifies an unimplemented local Codex 0.159.0 broker.
package codexbroker

import (
	"context"
	"encoding/json"
	"time"

	"github.com/seanheiney/agent-backplane/internal/domain"
)

type (
	WriterKind     string
	AdmissionState string
	Role           string
	FrameKind      string
	EffectClass    string
	ReceiptState   string
	DecisionAction string
)

const (
	NativeWriter   WriterKind = "native"
	SemanticWriter WriterKind = "semantic"
)

const (
	Admitting AdmissionState = "admitting"
	Fenced    AdmissionState = "fenced"
	Retired   AdmissionState = "retired"
)

// Grant views store.grants/one_writer; fenced pending/uncertain rows stay active.
type Grant struct {
	WriterRef
	ReadOnly, Active bool
	Admission        AdmissionState
}
type WriterRef struct {
	Lease       domain.Lease // Exact original token/client/acquisition/generation; private.
	PrincipalID string       // Trusted local host identity; never clientInfo or wire input.
	Kind        WriterKind
	Epoch       uint64 // Monotonic writer epoch, independent of execution generation.
}
type AgentBinding struct {
	HostID, SessionID, ExecutionID         domain.ID
	Generation                             uint64
	AppServerIncarnation                   domain.ID
	AppServerPID                           int
	AppServerBootID, AppServerProcessStart string
	ProtocolVersion, NativeThreadID        string
}

const (
	NativeLocal   Role = "native_local"
	TypedObserver Role = "typed_observer"
	TypedWriter   Role = "typed_writer"
)

// Immutable host provenance; old connections/endpoints NEVER rearm at any epoch.
type ConnectionBinding struct {
	ID                                  domain.ID
	Agent                               AgentBinding
	Writer                              WriterRef
	Role                                Role
	NativeLaunchID, EndpointIncarnation domain.ID
	NativePID                           int
	NativeProcessStart                  string
}

const (
	Request      FrameKind = "request"
	Notification FrameKind = "notification"
	Result       FrameKind = "result"
	RPCError     FrameKind = "error"
)

type Frame struct {
	Kind    FrameKind
	ID      json.RawMessage // Validated canonical string/integer JSON, channel-local.
	Method  string
	Payload json.RawMessage // params, result or error according to Kind.
}

const (
	Denied          EffectClass = "denied"
	NativeBootstrap EffectClass = "native_bootstrap"
	ScopedRead      EffectClass = "scoped_read"
	Mutation        EffectClass = "mutation"
	ViewerLocal     EffectClass = "viewer_local"
)

type Baseline struct {
	ProtocolVersion  string
	Workspace        string
	WorkspaceAliases []string // Home-verified aliases; never resolved at an entry.
	NativeBootstrap  map[string]json.RawMessage
}

// OutputContext comes from the broker's trusted typed-ID/channel request map.
// OriginalRequest is nil only for unsolicited native notifications/callbacks.
type OutputContext struct {
	Recipient         ConnectionBinding
	UpstreamChannelID domain.ID
	OriginalRequest   *Frame
	OriginOperationID domain.ID
}
type Policy interface {
	// Accept absent jsonrpc/"2.0"; reject malformed/batch/duplicate-key envelopes.
	Decode([]byte) (Frame, error)
	// Default-deny by schema/parameters/role; Authority checks one-shot bootstrap.
	Classify(ConnectionBinding, Frame, Baseline) (EffectClass, error)
	// Recheck live read scope; filter IDs/cursors and withhold unknown/global data.
	NativeOutput(OutputContext, Frame) (Frame, bool, error)
}

const (
	Recorded     ReceiptState = "recorded"
	Attempted    ReceiptState = "attempted"
	PendingAck   ReceiptState = "pending_ack"
	Acknowledged ReceiptState = "acknowledged"
	Completed    ReceiptState = "completed"
	Failed       ReceiptState = "failed"
	Uncertain    ReceiptState = "outcome_uncertain"
)

type Intent struct {
	OperationID         domain.ID
	Origin              ConnectionBinding
	UpstreamChannelID   domain.ID
	DownstreamRequestID json.RawMessage // Separate from the persisted upstream Frame.ID.
	Frame               Frame
	CanonicalHash       [32]byte // Authority recomputes target+payload; exclude only transport IDs.
	ClientUserMessageID string   // Persist before turn/start; not native idempotency.
}
type Receipt struct {
	Intent                             Intent
	State                              ReceiptState
	NativeTurnID, NativeItemID, Detail string
}
type CallbackKey struct {
	UpstreamChannelID                    domain.ID
	RequestID                            json.RawMessage
	ThreadID, TurnID, ItemID, ApprovalID string
}

const (
	Accept  DecisionAction = "accept"
	Decline DecisionAction = "decline"
	Answer  DecisionAction = "answer"
)

type CallbackRecord struct {
	ID, OriginOperationID                    domain.ID
	Key                                      CallbackKey
	Origin                                   ConnectionBinding // Original turn's writer/epoch, never current owner.
	Attributed                               bool              // False until unique originating intent is established.
	Payload                                  json.RawMessage
	CanonicalHash                            [32]byte
	DisplayConnectionID, DecisionOperationID domain.ID
	Allowed                                  []DecisionAction // Intersection with actual availableDecisions.
	DecisionState                            ReceiptState
	Resolved                                 bool
}
type Decision struct {
	OperationID, CallbackID domain.ID
	Key                     CallbackKey
	Actor                   ConnectionBinding
	Action                  DecisionAction
	Answers                 map[string][]string
}

// Bounded wire.Conn.Write: nil means PendingAck; any invoked error is Uncertain.
type Sender interface {
	WriteFrame(context.Context, domain.ID, []byte) error
}
type Handover struct {
	OperationID                   domain.ID
	Previous                      WriterRef
	NextPrincipalID, NextClientID string
	NextKind                      WriterKind
}
type Quiescence struct {
	Agent                                                    AgentBinding
	Idle, EmptyQueue, EmptyBackground, NativeWritersDetached bool
	Source                                                   string
	ObservedAt                                               time.Time
}
type QuiescenceProbe interface {
	Inspect(context.Context, AgentBinding) (Quiescence, error)
}
type Observation struct {
	SessionID, ExecutionID         domain.ID
	Generation                     uint64
	NativeThreadID, Status, Source string
	ObservedAt                     time.Time
}

// Authority serializes admission->Attempted->bounded send with revoke/handover.
// No DB transaction spans IO; no gate waits for a turn; unknowns NEVER replay.
type Authority interface {
	Bind(context.Context, ConnectionBinding) (ConnectionBinding, error)
	Dispatch(context.Context, Intent) (Receipt, error)
	RecordCallback(context.Context, CallbackRecord) (CallbackRecord, error)
	Decide(context.Context, Decision) (Receipt, error)
	Settle(context.Context, Receipt) (Receipt, error)
	Revoke(context.Context, WriterRef) (Grant, error)
	// Probe at the gate; unknowns block. Retire old/install next epoch atomically.
	Handover(context.Context, Handover) (Grant, error)
	Observe(context.Context, ConnectionBinding) (Observation, error)
}

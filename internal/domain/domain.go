// Package domain defines stable product identities independently of runtime locators.
package domain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

type ID string

func NewID(kind string) ID {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return ID(kind + "_" + hex.EncodeToString(b))
}

type Mode string

const (
	Observed Mode = "observed"
	Bridge   Mode = "bridge"
	Managed  Mode = "managed"
)

// Evidence values never derive account/model identity from terminal text.
type Evidence struct {
	Value      string    `json:"value"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

func Unknown() Evidence { return Evidence{Value: "unknown", Source: "unknown"} }

type Capabilities struct {
	NativeAttach   bool   `json:"native_attach"`
	ReadOnlyAttach bool   `json:"read_only_attach"`
	Stop           bool   `json:"stop"`
	SemanticPrompt bool   `json:"semantic_prompt"`
	NativeResume   bool   `json:"native_resume"`
	Explanation    string `json:"explanation"`
}

func ManagedCapabilities() Capabilities {
	return Capabilities{NativeAttach: true, ReadOnlyAttach: true, Stop: true,
		Explanation: "Owned tmux terminal: attach, detach, reconnect, or stop. Semantic prompts and remote approvals are unavailable; use the native CLI."}
}

type RuntimeRef struct {
	Backend      string `json:"backend"`
	Socket       string `json:"socket"`
	Session      string `json:"session"`
	Pane         string `json:"pane"`
	PID          int    `json:"pid"`
	BootID       string `json:"boot_id"`
	ProcessStart string `json:"process_start"`
	ExecutionID  ID     `json:"execution_id"`
	OperationID  ID     `json:"operation_id"`
}

type Execution struct {
	ID              ID         `json:"id"`
	Generation      uint64     `json:"generation"`
	Provider        string     `json:"provider"`
	ProviderVersion string     `json:"provider_version"`
	RequestedModel  Evidence   `json:"requested_model"`
	ActualModel     Evidence   `json:"actual_model"`
	Account         Evidence   `json:"account"`
	NativeThread    Evidence   `json:"native_thread"`
	Runtime         RuntimeRef `json:"runtime"`
	State           string     `json:"state"`
}

type Session struct {
	ID           ID           `json:"id"`
	HostID       ID           `json:"host_id"`
	WorkID       ID           `json:"work_id"`
	ThreadID     ID           `json:"thread_id"`
	LaneID       ID           `json:"lane_id"`
	Name         string       `json:"name"`
	Workspace    string       `json:"workspace"`
	Mode         Mode         `json:"mode"`
	Capabilities Capabilities `json:"capabilities"`
	Execution    Execution    `json:"execution"`
	Linked       bool         `json:"linked"`
	CreatedAt    time.Time    `json:"created_at"`
}

func NewSession(host ID, name, workspace, provider, version string) Session {
	return Session{ID: NewID("session"), HostID: host, WorkID: NewID("work"), ThreadID: NewID("thread"), LaneID: NewID("lane"),
		Name: name, Workspace: workspace, Mode: Managed, Capabilities: ManagedCapabilities(), CreatedAt: time.Now().UTC(),
		Execution: Execution{ID: NewID("execution"), Generation: 1, Provider: provider, ProviderVersion: version,
			RequestedModel: Unknown(), ActualModel: Unknown(), Account: Unknown(), NativeThread: Unknown(), State: "pending"}}
}

type Outcome string

const (
	Recorded  Outcome = "recorded"
	Attempted Outcome = "attempted"
	Completed Outcome = "completed"
	Failed    Outcome = "failed"
	Uncertain Outcome = "outcome_uncertain"
)

func CanTransition(from, to Outcome) bool {
	if from == to {
		return true
	}
	switch from {
	case Recorded:
		return to == Attempted || to == Failed
	case Attempted:
		return to == Completed || to == Failed || to == Uncertain
	case Uncertain:
		return to == Completed || to == Failed
	}
	return false
}

type Operation struct {
	ID          ID        `json:"id"`
	Kind        string    `json:"kind"`
	SessionID   ID        `json:"session_id"`
	ExecutionID ID        `json:"execution_id"`
	Outcome     Outcome   `json:"outcome"`
	Detail      string    `json:"detail,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type Event struct {
	Sequence    int64     `json:"sequence"`
	ThreadID    ID        `json:"thread_id"`
	SessionID   ID        `json:"session_id"`
	OperationID ID        `json:"operation_id,omitempty"`
	Kind        string    `json:"kind"`
	Detail      string    `json:"detail"`
	At          time.Time `json:"at"`
}
type LaunchSpec struct {
	Session     Session  `json:"session"`
	OperationID ID       `json:"operation_id"`
	Argv        []string `json:"argv"`
	Env         []string `json:"env,omitempty"`
}

func (s LaunchSpec) Validate() error {
	if s.Session.ID == "" || s.Session.Execution.ID == "" || s.Session.Execution.Generation == 0 || s.OperationID == "" {
		return errors.New("launch requires stable session, execution generation, and operation identity")
	}
	if !filepath.IsAbs(s.Session.Workspace) {
		return errors.New("workspace must be absolute")
	}
	if len(s.Argv) == 0 || s.Argv[0] == "" {
		return errors.New("launch requires executable")
	}
	return nil
}

type Lease struct {
	SessionID   ID     `json:"session_id"`
	ExecutionID ID     `json:"execution_id"`
	ClientID    string `json:"client_id"`
	Token       string `json:"token"`
}
type AttachSpec struct {
	Argv     []string `json:"argv"`
	Lease    Lease    `json:"lease"`
	ReadOnly bool     `json:"read_only"`
}
type ConflictError struct{ Reason string }

func (e ConflictError) Error() string { return fmt.Sprintf("conflict: %s", e.Reason) }

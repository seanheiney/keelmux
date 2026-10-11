// Package adoption compares saved declarations, applied workspace files and the
// immutable client-declared launch snapshot. It never reloads native processes
// or establishes native loading, permissions or tool invocation.
package adoption

import "github.com/seanheiney/keelmux/internal/domain"

const (
	Unknown     = "unknown"
	Match       = "match"
	Different   = "different"
	Unavailable = "unavailable"
	Blocked     = "blocked"
	Pending     = "pending"
	Aligned     = "aligned"
)

// Request is a caller-authorized local observation. LocalHost must be read with
// config.ReadHostID; a declaration is not independent runtime authority.
type Request struct {
	StateDir  string
	LocalHost domain.ID
	Session   domain.Session
}

type Selection struct {
	ProfileID        string `json:"profile_id"`
	ProfileRevision  uint64 `json:"profile_revision"`
	ResolutionDigest string `json:"resolution_digest"`
}

type Application struct {
	ID       string `json:"id"`
	Digest   string `json:"digest,omitempty"`
	Present  bool   `json:"present"`
	Verified bool   `json:"verified"`
	Coverage string `json:"coverage,omitempty"`
}

// Report contains bounded codes and declaration identities only: no generated
// content, native argv, environment values, tool requests or results.
type Report struct {
	HostID          domain.ID               `json:"host_id"`
	SessionID       domain.ID               `json:"session_id"`
	ExecutionID     domain.ID               `json:"execution_id"`
	Generation      uint64                  `json:"generation"`
	Provider        string                  `json:"provider"`
	Workspace       string                  `json:"workspace"`
	ExecutionState  string                  `json:"execution_state"`
	Launch          *domain.LibrarySnapshot `json:"launch,omitempty"`
	Saved           *Selection              `json:"saved,omitempty"`
	Applied         *Application            `json:"applied,omitempty"`
	SavedToLaunch   string                  `json:"saved_to_launch"`
	SavedToApplied  string                  `json:"saved_to_applied"`
	AppliedToLaunch string                  `json:"applied_to_launch"`
	State           string                  `json:"state"`
	Reason          string                  `json:"reason"`
	NextAction      string                  `json:"next_action"`
	NativeLoaded    string                  `json:"native_loaded"`
	Permissions     string                  `json:"permissions"`
}

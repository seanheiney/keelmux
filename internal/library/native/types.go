// Package native maps verified library declarations to explicit workspace
// files and launch arguments. Applying files never starts a native process,
// executes a tool, changes account stores, or proves native loaded state.
package native

import "errors"

var (
	ErrUnsafe      = errors.New("unsafe native application path or state")
	ErrStale       = errors.New("native application preview is stale; preview again")
	ErrCollision   = errors.New("native application collides with preserved content")
	ErrDrift       = errors.New("native application owned content changed")
	ErrPending     = errors.New("native application intent needs explicit recovery")
	ErrUnsupported = errors.New("native application has no supported mapping")
	ErrBusy        = errors.New("native application writer is busy")
)

// Target is explicitly selected by the caller. Workspace must be an existing
// owned directory. ApplicationID binds one harness and workspace ledger.
type Target struct {
	Harness       string `json:"harness"`
	Workspace     string `json:"workspace"`
	ApplicationID string `json:"application_id"`
}

type FileIdentity struct {
	Present bool   `json:"present"`
	SHA256  string `json:"sha256,omitempty"`
}

type FileOperation struct {
	Path    string       `json:"path"` // Workspace-relative native file.
	Before  FileIdentity `json:"before"`
	After   FileIdentity `json:"after"`
	Content []byte       `json:"content,omitempty"`
}

type Collision struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Unmapped struct {
	PackageID string `json:"package_id"`
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Field     string `json:"field,omitempty"`
	Reason    string `json:"reason"`
}

// Plan is reviewable desired state, including all preimages and generated
// content. Apply regenerates this plan from the registry rather than trusting
// caller-authored content, argv, digest, or resolution authority.
type Plan struct {
	Schema                int                     `json:"schema"`
	Target                Target                  `json:"target"`
	ProfileID             string                  `json:"profile_id"`
	ProfileRevision       uint64                  `json:"profile_revision"`
	ResolutionDigest      string                  `json:"resolution_digest"`
	ExpectedReceiptDigest string                  `json:"expected_receipt_digest,omitempty"`
	WorkspaceDevice       uint64                  `json:"workspace_device"`
	WorkspaceInode        uint64                  `json:"workspace_inode"`
	Files                 []FileOperation         `json:"files"`
	Guards                map[string]FileIdentity `json:"guards"`
	Collisions            []Collision             `json:"collisions"`
	Unmapped              []Unmapped              `json:"unmapped"`
	LaunchArgs            []string                `json:"launch_args"` // Structured argv, never shell.
	Prerequisites         []string                `json:"prerequisites"`
	Scope                 string                  `json:"scope"`
	Precedence            string                  `json:"precedence"`
	NativeContract        string                  `json:"native_contract"`
	Loaded                string                  `json:"loaded"`
	Reload                string                  `json:"reload"`
	Digest                string                  `json:"digest"`
}

type OwnedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// Receipt proves only this applied file set and declared launch arguments.
// Neither native permissions nor running configuration can be inferred from it.
type Receipt struct {
	Schema             int         `json:"schema"`
	Target             Target      `json:"target"`
	Revision           uint64      `json:"revision"`
	ProfileID          string      `json:"profile_id"`
	ProfileRevision    uint64      `json:"profile_revision"`
	ResolutionDigest   string      `json:"resolution_digest"`
	PlanDigest         string      `json:"plan_digest"`
	Files              []OwnedFile `json:"files"`
	WorkspaceDevice    uint64      `json:"workspace_device"`
	WorkspaceInode     uint64      `json:"workspace_inode"`
	LockDevice         uint64      `json:"lock_device"`
	LockInode          uint64      `json:"lock_inode"`
	CreatedDirectories []string    `json:"created_directories"`
	LaunchArgs         []string    `json:"launch_args"`
	Unmapped           []Unmapped  `json:"unmapped"`
	Applied            bool        `json:"applied"`
	Coverage           string      `json:"coverage"`
	Loaded             string      `json:"loaded"`
	Reload             string      `json:"reload"`
	Digest             string      `json:"digest"`
}

// ApplicationStatus is a read-only byte/ownership observation. A pending intent or drift
// never returns Verified=true. Caller authorization is supplied above this API.
type ApplicationStatus struct {
	Present  bool        `json:"present"`
	Verified bool        `json:"verified"`
	Pending  bool        `json:"pending"`
	Receipt  *Receipt    `json:"receipt,omitempty"`
	Drift    []Collision `json:"drift"`
}

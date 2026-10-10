// Package runtime is the terminal backend boundary. It does not promise semantic prompts.
package runtime

import (
	"context"
	"github.com/seanheiney/agent-backplane/internal/domain"
)

type Observation struct {
	Ref             domain.RuntimeRef `json:"ref"`
	Alive           bool              `json:"alive"`
	WritableClients int               `json:"writable_clients"`
	Detail          string            `json:"detail"`
}

// Lookup uses exact execution AND operation markers. Absence is not permission to replay.
type Backend interface {
	Launch(context.Context, domain.LaunchSpec) (domain.RuntimeRef, error)
	Lookup(context.Context, domain.ID, domain.ID) (Observation, error)
	Inspect(context.Context, domain.RuntimeRef) (Observation, error)
	Stop(context.Context, domain.RuntimeRef) error
	AttachCommand(domain.RuntimeRef, bool) ([]string, error)
}

// LinkVerifier verifies an inherited owned-launch nonce against private receipts
// and live runtime markers. Public inventory IDs alone cannot establish a link.
type LinkVerifier interface {
	ValidateLink(context.Context, domain.ID, domain.ID, domain.ID, string) error
}

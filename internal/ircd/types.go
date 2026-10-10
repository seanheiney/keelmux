// Package ircd provisions one optional pinned Ergo server in a private local
// realm. It never grants model, terminal, remote principal or public authority.
package ircd

import (
	"context"
	"errors"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/runtime"
	"time"
)

const Version = "2.19.1"
const SourceCommit = "63c743a70644f0f19109508ade8580bf37f7d23d"

var (
	ErrUnavailable    = errors.New("managed IRC runtime unavailable")
	ErrRepair         = errors.New("managed IRC state requires repair")
	ErrConflict       = errors.New("managed IRC operation conflicts with original ownership")
	ErrUncertain      = errors.New("managed IRC outcome uncertain; read-only reconciliation required")
	ErrNotInitialized = errors.New("managed IRC is not initialized")
)

type InitOptions struct{ IRCPort, APIPort int }
type RuntimeInfo struct{ Version, Source, Target, ArchiveSHA256, BinarySHA256, DefaultSHA256, LicenseSHA256, Path string }
type Credential struct{ Account, CredentialRef string }
type Config struct {
	ID                                                             domain.ID
	Endpoint, RealmID, APIEndpoint, CAFile, CredentialRef, Account string
	Runtime                                                        RuntimeInfo
}
type Status struct {
	Config                                       Config
	Instance, OperationID                        domain.ID
	State                                        string
	Alive                                        bool
	Version, StartTime, GoVersion, RuntimeCommit string
	ObservedAt                                   time.Time
}
type ChannelOperation struct {
	ID, ServerID, ThreadID              domain.ID
	Channel, State, Owner, RegisteredAt string
	UpdatedAt                           time.Time
}

// Supervisor is the owned persistent process boundary. Lookup absence never
// permits retry of a launch whose acknowledgement was lost. An observation with
// Alive=false and nil error must prove an exited owned child; unavailable helper
// or missing receipt is an error, never guessed process death.
type Supervisor interface {
	LaunchProcess(context.Context, domain.LaunchSpec) (domain.RuntimeRef, error)
	Lookup(context.Context, domain.ID, domain.ID) (runtime.Observation, error)
	Inspect(context.Context, domain.RuntimeRef) (runtime.Observation, error)
	Stop(context.Context, domain.RuntimeRef) error
}
type Manager struct {
	dir        string
	supervisor Supervisor
}

func NewWithSupervisor(dir string, supervisor Supervisor) (*Manager, error) {
	m, e := New(dir)
	if e != nil {
		return nil, e
	}
	if supervisor == nil {
		return nil, ErrUnavailable
	}
	m.supervisor = supervisor
	return m, nil
}

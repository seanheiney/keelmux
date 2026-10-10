// Package ircdservice exposes explicit private local-home IRCD operations.
// It has no native terminal/model input API and never starts an IRCD implicitly.
package ircdservice

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
	"github.com/seanheiney/keelmux/internal/ircdchannel"
	"github.com/seanheiney/keelmux/internal/protocol"
)

type Manager interface {
	Init(context.Context, ircd.InitOptions) (ircd.Config, error)
	Config() (ircd.Config, error)
	Status(context.Context) (ircd.Status, error)
	Start(context.Context, domain.ID) (ircd.Status, error)
	Stop(context.Context, domain.ID) error
	Restart(context.Context, domain.ID, domain.ID) (ircd.Status, error)
	EnsureAccount(context.Context, string) (ircd.Credential, error)
}
type Registrar interface {
	EnsureProject(context.Context, ircdchannel.Project, domain.ID, domain.ID) (ircdchannel.Route, error)
}
type Binder interface {
	Bind(context.Context, protocol.ChannelBindRequest) (protocol.ChannelStatus, error)
}
type Sessions interface {
	Session(context.Context, domain.ID) (domain.Session, error)
}

// ProjectRegistry is required only for explicit managed project bindings. A
// sessions-only deployment can still initialize, inspect and control its IRCD.
type ProjectRegistry interface {
	Project(context.Context, domain.ID) (domain.Project, error)
	BindProjectThread(context.Context, domain.ID, domain.ID) (domain.Project, error)
}
type Service struct {
	manager   Manager
	registrar Registrar
	binder    Binder
	sessions  Sessions
}

func New(manager Manager, registrar Registrar, binder Binder, sessions Sessions) (*Service, error) {
	for _, dependency := range []any{manager, registrar, binder, sessions} {
		if dependency == nil {
			return nil, channels.ErrInvalid
		}
		value := reflect.ValueOf(dependency)
		if value.Kind() == reflect.Pointer && value.IsNil() {
			return nil, channels.ErrInvalid
		}
	}
	return &Service{manager: manager, registrar: registrar, binder: binder, sessions: sessions}, nil
}
func validID(id domain.ID, kind string) bool {
	s := string(id)
	if len(s) != len(kind)+33 || !strings.HasPrefix(s, kind+"_") {
		return false
	}
	for _, r := range s[len(kind)+1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func validOperation(id domain.ID) bool { return validID(id, "operation") || validID(id, "op") }
func validAllowlist(actors []string) bool {
	if len(actors) == 0 || len(actors) > 256 {
		return false
	}
	seen := map[string]bool{}
	for _, actor := range actors {
		if !validAccount(actor) || seen[actor] {
			return false
		}
		seen[actor] = true
	}
	return true
}

func validAccount(s string) bool {
	if len(s) < 1 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (s *Service) Bind(ctx context.Context, in protocol.IRCDBindRequest) (protocol.ChannelStatus, error) {
	var zero protocol.ChannelStatus
	if !validID(in.ProjectID, "project") || !validID(in.ID, "binding") || !validID(in.SessionID, "session") || !validOperation(in.OperationID) || !validAllowlist(in.Allowlist) {
		return zero, channels.ErrInvalid
	}
	session, err := s.sessions.Session(ctx, in.SessionID)
	if err != nil {
		return zero, err
	}
	if session.ID != in.SessionID || !validID(session.ThreadID, "thread") {
		return zero, domain.ConflictError{Reason: "session stable thread identity could not be verified"}
	}
	registry, ok := s.sessions.(ProjectRegistry)
	if !ok {
		return zero, domain.ConflictError{Reason: "explicit project registry is unavailable"}
	}
	project, err := registry.Project(ctx, in.ProjectID)
	if err != nil {
		return zero, err
	}
	if project.Validate() != nil || project.ID != in.ProjectID || project.HostID != session.HostID || project.Workspace != session.Workspace {
		return zero, domain.ConflictError{Reason: "project host or workspace does not match the session"}
	}
	membership, err := registry.BindProjectThread(ctx, project.ID, session.ID)
	if err != nil {
		return zero, err
	}
	if membership != project {
		return zero, ircd.ErrConflict
	}
	// Membership is explicit and durably pinned before any external effect.
	// Re-read the stable scope to reject a mismatched registry/session resolver.
	current, err := s.sessions.Session(ctx, session.ID)
	if err != nil {
		return zero, err
	}
	if current.ID != session.ID || current.ThreadID != session.ThreadID || current.HostID != project.HostID || current.Workspace != project.Workspace {
		return zero, ircd.ErrConflict
	}
	projection := ircdchannel.Project{ID: project.ID, HostID: project.HostID, Name: project.Name, Workspace: project.Workspace}
	route, err := s.registrar.EnsureProject(ctx, projection, session.ThreadID, in.OperationID)
	if err != nil {
		return zero, err
	}
	cfg, err := s.manager.Config()
	if err != nil {
		return zero, err
	}
	channel, err := ircdchannel.ProjectChannelName(projection)
	account, accountErr := ircdchannel.ThreadAccount(session.ThreadID)
	ref := filepath.Join(filepath.Dir(cfg.CredentialRef), "accounts", account, "credential.json")
	registered, registrationErr := time.Parse(time.RFC3339Nano, route.RegisteredAt)
	if err != nil || accountErr != nil || route.ProjectID != project.ID || route.Nick != account || route.ServerID != cfg.ID || route.ThreadID != session.ThreadID || route.Endpoint != cfg.Endpoint || route.RealmID != cfg.RealmID || route.Account != account || route.CredentialRef != ref || route.CAFile != cfg.CAFile || route.Channel != channel || registrationErr != nil || registered.IsZero() || route.ConfirmedAt.IsZero() || !validOperation(route.OperationID) {
		return zero, ircd.ErrConflict
	}
	allow := append([]string{}, in.Allowlist...)
	sort.Strings(allow)
	out, err := s.binder.Bind(ctx, protocol.ChannelBindRequest{ID: in.ID, SessionID: session.ID, ProjectID: project.ID, IRCAddressed: true, Provider: channels.IRC, Endpoint: route.Endpoint, ChannelID: route.Channel, CredentialRef: route.CredentialRef, Allowlist: allow})
	if err != nil {
		return zero, err
	}
	if out.ProjectID != project.ID || !out.IRCAddressed || out.ID != in.ID || out.SessionID != session.ID || out.ThreadID != session.ThreadID || out.Provider != channels.IRC || out.Endpoint != route.Endpoint || out.RealmID != route.RealmID || out.SelfID != route.Account || out.ChannelID != route.Channel || out.RootID != "" || !reflect.DeepEqual(out.Allowlist, allow) || out.SemanticDispatch {
		return zero, errors.New("managed IRC binding proof could not be verified")
	}
	return out, nil
}

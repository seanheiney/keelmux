package ircdchannel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/ircd"
)

// Project is an explicit registry projection; names and workspaces never create
// inferred identity or membership. The caller proves host/session membership.
type Project struct {
	ID, HostID      domain.ID
	Name, Workspace string
}

type ProjectRuntime interface {
	EnsureThreadAccount(context.Context, domain.ID) (ircd.Credential, error)
	BeginProjectChannel(context.Context, string, domain.ID, domain.ID) (ircd.ProjectChannelOperation, bool, error)
	ConfirmProjectChannel(context.Context, string, domain.ID) (ircd.ProjectChannelOperation, error)
}

// ProjectChannelName includes all 128 project identity bits. The bounded slug
// is descriptive only; the manager pins one immutable channel per full ID.
func ProjectChannelName(p Project) (string, error) {
	if (domain.Project{ID: p.ID, HostID: p.HostID, Name: p.Name, Workspace: p.Workspace, CreatedAt: time.Unix(1, 0)}).Validate() != nil {
		return "", ErrScope
	}
	var slug strings.Builder
	separator := false
	for _, c := range strings.ToLower(p.Name) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			if separator && slug.Len() > 0 && slug.Len() < 24 {
				slug.WriteByte('-')
			}
			separator = false
			if slug.Len() < 24 {
				slug.WriteRune(c)
			}
		} else {
			separator = true
		}
	}
	name := strings.Trim(slug.String(), "-")
	if name == "" {
		name = "project"
	}
	return "#km-" + name + "-" + strings.TrimPrefix(string(p.ID), "project_"), nil
}

func ThreadAccount(threadID domain.ID) (string, error) { return ircd.ThreadAccount(threadID) }

func (s *Service) EnsureProject(parent context.Context, project Project, threadID, operationID domain.ID) (Route, error) {
	channel, e := ProjectChannelName(project)
	account, accountErr := ThreadAccount(threadID)
	runtime, ok := s.runtime.(ProjectRuntime)
	if e != nil || accountErr != nil || !validOperationID(operationID) || !ok {
		return Route{}, ErrScope
	}
	ctx, leave, e := s.enter(parent)
	if e != nil {
		return Route{}, e
	}
	defer leave()
	cfg, e := s.runtime.Config()
	if e != nil || validateConfig(cfg) != nil {
		return Route{}, ErrScope
	}
	management, e := s.runtime.EnsureAccount(ctx, cfg.Account)
	if e != nil {
		return Route{}, e
	}
	if management.Account != cfg.Account || management.CredentialRef != cfg.CredentialRef {
		return Route{}, ErrScope
	}
	secret, e := loadSecret(cfg)
	if e != nil {
		return Route{}, e
	}
	op, fresh, e := runtime.BeginProjectChannel(ctx, channel, project.ID, operationID)
	if e != nil {
		return Route{}, e
	}
	if !matchesProjectOperation(op, cfg, channel, project.ID) || fresh && (op.ID != operationID || op.State != "attempted" || op.RegisteredAt != "") {
		return Route{}, ErrScope
	}
	var mutationErr error
	if fresh {
		attempt, cancel := context.WithTimeout(ctx, s.timeout)
		mutationErr = s.register(attempt, cfg, secret, channel)
		cancel()
	}
	bounded, cancel := context.WithTimeout(ctx, s.timeout)
	confirmed, e := runtime.ConfirmProjectChannel(bounded, channel, project.ID)
	cancel()
	if e != nil {
		if errors.Is(mutationErr, ErrAuthentication) || errors.Is(mutationErr, ErrScope) {
			return Route{}, mutationErr
		}
		return Route{}, ErrUncertain
	}
	registered, e := time.Parse(time.RFC3339Nano, confirmed.RegisteredAt)
	if e != nil || registered.IsZero() || !matchesProjectOperation(confirmed, cfg, channel, project.ID) || confirmed.ID != op.ID || confirmed.State != "confirmed" {
		return Route{}, ErrScope
	}
	credential, e := runtime.EnsureThreadAccount(ctx, threadID)
	if e != nil {
		return Route{}, e
	}
	ref := filepath.Join(filepath.Dir(cfg.CredentialRef), "accounts", account, "credential.json")
	if credential.Account != account || credential.CredentialRef != ref || validateThreadSecret(ref, account) != nil {
		return Route{}, ErrScope
	}
	return Route{ServerID: cfg.ID, ProjectID: project.ID, ThreadID: threadID, OperationID: confirmed.ID, Endpoint: cfg.Endpoint, RealmID: cfg.RealmID, Account: account, Nick: account, CredentialRef: ref, CAFile: cfg.CAFile, Channel: channel, RegisteredAt: confirmed.RegisteredAt, ConfirmedAt: confirmed.UpdatedAt}, nil
}

func matchesProjectOperation(o ircd.ProjectChannelOperation, c ircd.Config, channel string, project domain.ID) bool {
	return validOperationID(o.ID) && o.ServerID == c.ID && o.ProjectID == project && o.Channel == channel && o.Owner == c.Account && !o.UpdatedAt.IsZero() && (o.State == "attempted" || o.State == "confirmed")
}

func validateThreadSecret(ref, account string) error {
	raw, e := config.ReadPrivateSecret(ref)
	if e != nil {
		return ErrAuthentication
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, e := d.Token(); e != nil || token != json.Delim('{') {
		return ErrAuthentication
	}
	values := map[string]string{}
	for d.More() {
		key, e := d.Token()
		name, ok := key.(string)
		_, exists := values[name]
		if e != nil || !ok || exists || name != "account" && name != "password" && name != "nick" {
			return ErrAuthentication
		}
		var value string
		if d.Decode(&value) != nil {
			return ErrAuthentication
		}
		values[name] = value
	}
	if token, e := d.Token(); e != nil || token != json.Delim('}') || len(values) != 3 || values["account"] != account || values["nick"] != account || len(values["password"]) != 64 {
		return ErrAuthentication
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrAuthentication
	}
	for _, c := range values["password"] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ErrAuthentication
		}
	}
	return nil
}

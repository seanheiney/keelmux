package ircd

import (
	"context"
	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ProjectChannelOperation struct {
	ID, ServerID, ProjectID             domain.ID
	Channel, State, Owner, RegisteredAt string
	UpdatedAt                           time.Time
}
type projectChannelRecord struct {
	Format    int
	Operation ProjectChannelOperation
}

func (m *Manager) projectChannelPath(projectID domain.ID) string {
	return filepath.Join(m.dir, "project-channels", string(projectID)+".json")
}
func validProjectChannel(channel string, projectID domain.ID) bool {
	if !validLocalID(projectID, "project") || len(channel) < 38 || len(channel) > 61 || !strings.HasPrefix(channel, "#km-") || !strings.HasSuffix(channel, "-"+strings.TrimPrefix(string(projectID), "project_")) {
		return false
	}
	slug := channel[4 : len(channel)-33]
	if len(slug) < 1 || len(slug) > 24 || strings.Trim(slug, "-") != slug {
		return false
	}
	for _, c := range slug {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
func (m *Manager) projectChannelRecord(c Config, channel string, project domain.ID) (projectChannelRecord, error) {
	var r projectChannelRecord
	e := readJSON(m.projectChannelPath(project), &r)
	if e != nil {
		return r, e
	}
	o := r.Operation
	if r.Format != 2 || o.ServerID != c.ID || o.ProjectID != project || o.Channel != channel || o.Owner != c.Account || !validOperationID(o.ID) || (o.State != "attempted" && o.State != "confirmed") || o.UpdatedAt.IsZero() || (o.State == "confirmed" && !validTimestamp(o.RegisteredAt)) {
		return r, ErrConflict
	}
	return r, nil
}

// BeginProjectChannel admits one external registration after committing the exact
// server/channel/project route. Existing intents only authorize read-only proof.
func (m *Manager) BeginProjectChannel(ctx context.Context, channel string, projectID, operationID domain.ID) (ProjectChannelOperation, bool, error) {
	canonical, e := channels.CanonicalChannel(channels.IRC, channel)
	if e != nil || canonical != channel || !validProjectChannel(channel, projectID) || !validLocalID(projectID, "project") || !validOperationID(operationID) {
		return ProjectChannelOperation{}, false, ErrConflict
	}
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return ProjectChannelOperation{}, false, e
	}
	defer unlock()
	r, e := m.readConfig(ctx)
	if e != nil || r.Stage != "ready" {
		return ProjectChannelOperation{}, false, ErrRepair
	}
	c := r.Config
	if old, e := m.projectChannelRecord(c, channel, projectID); e == nil {
		return old.Operation, false, nil
	} else if !os.IsNotExist(e) {
		return ProjectChannelOperation{}, false, e
	}
	if _, e := os.Lstat(m.channelPath(channel)); !os.IsNotExist(e) {
		// A historical format1 attempt is never reset, rewritten or adopted,
		// including when its external mutation outcome is still unknown.
		return ProjectChannelOperation{}, false, ErrConflict
	}
	if e = m.requireLive(ctx, r); e != nil {
		return ProjectChannelOperation{}, false, e
	}
	if e = m.confirmedAccount(ctx, c); e != nil {
		return ProjectChannelOperation{}, false, e
	}
	var list listResponse
	if m.api(ctx, c, "/v1/list", struct{}{}, &list) != nil || !list.Success || len(list.Channels) > 256 {
		return ProjectChannelOperation{}, false, ErrUnavailable
	}
	for _, ch := range list.Channels {
		v, e := channels.CanonicalChannel(channels.IRC, ch.Name)
		if e == nil && v == channel {
			return ProjectChannelOperation{}, false, ErrConflict
		}
	}
	dir := filepath.Join(m.dir, "project-channels")
	if config.EnsurePrivateDirectory(dir) != nil {
		return ProjectChannelOperation{}, false, ErrRepair
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) >= 128 {
		return ProjectChannelOperation{}, false, ErrConflict
	}
	op := ProjectChannelOperation{ID: operationID, ServerID: c.ID, ProjectID: projectID, Channel: channel, State: "attempted", Owner: c.Account, UpdatedAt: time.Now().UTC()}
	if writeJSON(m.projectChannelPath(projectID), projectChannelRecord{Format: 2, Operation: op}, false) != nil {
		return ProjectChannelOperation{}, false, ErrRepair
	}
	return op, true, nil
}

// ConfirmProjectChannel never creates or edits a remote channel. Owner and registration
// timestamp prove the original journaled route; an unknown outcome is not replayed.
func (m *Manager) ConfirmProjectChannel(ctx context.Context, channel string, projectID domain.ID) (ProjectChannelOperation, error) {
	canonical, e := channels.CanonicalChannel(channels.IRC, channel)
	if e != nil || canonical != channel || !validProjectChannel(channel, projectID) || !validLocalID(projectID, "project") {
		return ProjectChannelOperation{}, ErrConflict
	}
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return ProjectChannelOperation{}, e
	}
	defer unlock()
	r, e := m.readConfig(ctx)
	if e != nil || r.Stage != "ready" {
		return ProjectChannelOperation{}, ErrRepair
	}
	c := r.Config
	saved, e := m.projectChannelRecord(c, channel, projectID)
	if e != nil {
		return ProjectChannelOperation{}, e
	}
	if e = m.requireLive(ctx, r); e != nil {
		return ProjectChannelOperation{}, e
	}
	if e = m.confirmedAccount(ctx, c); e != nil {
		return ProjectChannelOperation{}, e
	}
	var list listResponse
	if m.api(ctx, c, "/v1/list", struct{}{}, &list) != nil || !list.Success || len(list.Channels) > 256 {
		return ProjectChannelOperation{}, ErrUnavailable
	}
	matches := 0
	var proof apiChannel
	for _, ch := range list.Channels {
		v, e := channels.CanonicalChannel(channels.IRC, ch.Name)
		if e == nil && v == channel {
			matches++
			proof = ch
		}
	}
	if matches != 1 || !proof.Registered || proof.Owner != c.Account || !validTimestamp(proof.RegisteredAt) || !validTimestamp(proof.CreatedAt) || (saved.Operation.RegisteredAt != "" && saved.Operation.RegisteredAt != proof.RegisteredAt) {
		return saved.Operation, ErrUncertain
	}
	saved.Operation.State = "confirmed"
	saved.Operation.RegisteredAt = proof.RegisteredAt
	saved.Operation.UpdatedAt = time.Now().UTC()
	if writeJSON(m.projectChannelPath(projectID), saved, true) != nil {
		return ProjectChannelOperation{}, ErrRepair
	}
	return saved.Operation, nil
}

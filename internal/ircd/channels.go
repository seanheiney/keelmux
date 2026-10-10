package ircd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
)

type channelRecord struct {
	Format    int
	Operation ChannelOperation
}

func validOperationID(id domain.ID) bool {
	return validLocalID(id, "operation") || validLocalID(id, "op")
}
func (m *Manager) channelPath(channel string) string {
	return filepath.Join(m.dir, "channels", digest([]byte(channel))+".json")
}
func (m *Manager) confirmedAccount(ctx context.Context, c Config) error {
	secret, sha, e := m.accountSecret(c)
	if e != nil {
		return e
	}
	var saved accountRecord
	if readJSON(filepath.Join(m.dir, "account.json"), &saved) != nil || saved.Format != 1 || saved.State != "confirmed" || saved.Account != c.Account || saved.ServerID != string(c.ID) || saved.CredentialSHA256 != sha {
		return ErrRepair
	}
	proof, e := m.accountProof(ctx, c, secret)
	if e != nil {
		return e
	}
	if saved.RegisteredAt != proof.RegisteredAt {
		return ErrConflict
	}
	return nil
}
func (m *Manager) channelRecord(c Config, channel string, thread domain.ID) (channelRecord, error) {
	var r channelRecord
	e := readJSON(m.channelPath(channel), &r)
	if e != nil {
		return r, e
	}
	o := r.Operation
	if r.Format != 1 || o.ServerID != c.ID || o.ThreadID != thread || o.Channel != channel || o.Owner != c.Account || !validOperationID(o.ID) || (o.State != "attempted" && o.State != "confirmed") || o.UpdatedAt.IsZero() || (o.State == "confirmed" && !validTimestamp(o.RegisteredAt)) {
		return r, ErrConflict
	}
	return r, nil
}

// BeginChannel admits one external registration after committing the exact
// server/channel/thread route. Existing intents only authorize read-only proof.
func (m *Manager) BeginChannel(ctx context.Context, channel string, threadID, operationID domain.ID) (ChannelOperation, bool, error) {
	canonical, e := channels.CanonicalChannel(channels.IRC, channel)
	if e != nil || canonical != channel || !strings.HasPrefix(channel, "#") || !validLocalID(threadID, "thread") || !validOperationID(operationID) {
		return ChannelOperation{}, false, ErrConflict
	}
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return ChannelOperation{}, false, e
	}
	defer unlock()
	r, e := m.readConfig(ctx)
	if e != nil || r.Stage != "ready" {
		return ChannelOperation{}, false, ErrRepair
	}
	c := r.Config
	if old, e := m.channelRecord(c, channel, threadID); e == nil {
		return old.Operation, false, nil
	} else if !os.IsNotExist(e) {
		return ChannelOperation{}, false, e
	}
	if _, e = m.probe(ctx, c); e != nil {
		return ChannelOperation{}, false, e
	}
	if e = m.confirmedAccount(ctx, c); e != nil {
		return ChannelOperation{}, false, e
	}
	var list listResponse
	if m.api(ctx, c, "/v1/list", struct{}{}, &list) != nil || !list.Success || len(list.Channels) > 256 {
		return ChannelOperation{}, false, ErrUnavailable
	}
	for _, ch := range list.Channels {
		v, e := channels.CanonicalChannel(channels.IRC, ch.Name)
		if e == nil && v == channel {
			return ChannelOperation{}, false, ErrConflict
		}
	}
	dir := filepath.Join(m.dir, "channels")
	if config.EnsurePrivateDirectory(dir) != nil {
		return ChannelOperation{}, false, ErrRepair
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) >= 128 {
		return ChannelOperation{}, false, ErrConflict
	}
	op := ChannelOperation{ID: operationID, ServerID: c.ID, ThreadID: threadID, Channel: channel, State: "attempted", Owner: c.Account, UpdatedAt: time.Now().UTC()}
	if writeJSON(m.channelPath(channel), channelRecord{Format: 1, Operation: op}, false) != nil {
		return ChannelOperation{}, false, ErrRepair
	}
	return op, true, nil
}

// ConfirmChannel never creates or edits a remote channel. Owner and registration
// timestamp prove the original journaled route; an unknown outcome is not replayed.
func (m *Manager) ConfirmChannel(ctx context.Context, channel string, threadID domain.ID) (ChannelOperation, error) {
	canonical, e := channels.CanonicalChannel(channels.IRC, channel)
	if e != nil || canonical != channel || !validLocalID(threadID, "thread") {
		return ChannelOperation{}, ErrConflict
	}
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return ChannelOperation{}, e
	}
	defer unlock()
	r, e := m.readConfig(ctx)
	if e != nil || r.Stage != "ready" {
		return ChannelOperation{}, ErrRepair
	}
	c := r.Config
	saved, e := m.channelRecord(c, channel, threadID)
	if e != nil {
		return ChannelOperation{}, e
	}
	if _, e = m.probe(ctx, c); e != nil {
		return ChannelOperation{}, e
	}
	if e = m.confirmedAccount(ctx, c); e != nil {
		return ChannelOperation{}, e
	}
	var list listResponse
	if m.api(ctx, c, "/v1/list", struct{}{}, &list) != nil || !list.Success || len(list.Channels) > 256 {
		return ChannelOperation{}, ErrUnavailable
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
	if writeJSON(m.channelPath(channel), saved, true) != nil {
		return ChannelOperation{}, ErrRepair
	}
	return saved.Operation, nil
}

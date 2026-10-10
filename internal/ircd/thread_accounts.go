package ircd

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
)

// ThreadAccount encodes all 128 immutable thread identity bits. Its canonical
// lowercase ASCII spelling is also the exact persistent IRC nickname (29 bytes).
func ThreadAccount(threadID domain.ID) (string, error) {
	if !validLocalID(threadID, "thread") {
		return "", ErrConflict
	}
	b, e := hex.DecodeString(strings.TrimPrefix(string(threadID), "thread_"))
	if e != nil || len(b) != 16 {
		return "", ErrConflict
	}
	return "kt-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}

type threadAccountRecord struct {
	Format             int
	ServerID, ThreadID domain.ID
	Account, State     string
	UpdatedAt          time.Time
}

// EnsureThreadAccount makes a durable full-thread claim before account IO.
// Existing accounts without this claim are never adopted. Account mutations
// still use their independent prepared/attempted/confirmed journal, so neither
// a lost ACK nor a failed confirmation permits registration replay or reset.
func (m *Manager) EnsureThreadAccount(ctx context.Context, threadID domain.ID) (Credential, error) {
	account, e := ThreadAccount(threadID)
	if e != nil {
		return Credential{}, e
	}
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Credential{}, e
	}
	defer unlock()
	r, e := m.readConfig(ctx)
	if e != nil || r.Stage != "ready" {
		return Credential{}, ErrRepair
	}
	dir := filepath.Join(m.dir, "thread-accounts")
	p := filepath.Join(dir, string(threadID)+".json")
	var saved threadAccountRecord
	e = readJSON(p, &saved)
	if e == nil {
		if saved.Format != 2 || saved.ServerID != r.Config.ID || saved.ThreadID != threadID || saved.Account != account || saved.UpdatedAt.IsZero() || saved.State != "claimed" && saved.State != "confirmed" {
			return Credential{}, ErrConflict
		}
	} else if !os.IsNotExist(e) {
		return Credential{}, ErrRepair
	} else {
		// Even a locally generated account needs an original thread claim; its
		// name and credentials alone do not establish the thread's ownership.
		if _, e := os.Lstat(filepath.Join(m.dir, "accounts", account)); !os.IsNotExist(e) {
			return Credential{}, ErrConflict
		}
		if config.EnsurePrivateDirectory(dir) != nil {
			return Credential{}, ErrRepair
		}
		entries, e := os.ReadDir(dir)
		if e != nil || len(entries) >= maxManagedAccounts-1 {
			return Credential{}, ErrConflict
		}
		saved = threadAccountRecord{Format: 2, ServerID: r.Config.ID, ThreadID: threadID, Account: account, State: "claimed", UpdatedAt: time.Now().UTC()}
		if writeJSON(p, saved, false) != nil {
			return Credential{}, ErrRepair
		}
	}
	credential, e := m.ensureAccountLocked(ctx, r, account)
	if e != nil {
		return Credential{}, e
	}
	saved.State, saved.UpdatedAt = "confirmed", time.Now().UTC()
	if writeJSON(p, saved, true) != nil {
		return Credential{}, ErrRepair
	}
	return credential, nil
}

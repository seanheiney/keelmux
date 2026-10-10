package ircd

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	CredentialSHA256   string `json:",omitempty"`
	RegisteredAt       string `json:",omitempty"`
	UpdatedAt          time.Time
}

// EnsureThreadAccount makes a durable full-thread claim before account IO.
// Existing accounts without this claim are never adopted. Only the fresh claim
// turn may consume a prepared account journal. Existing claims require the
// original private authority and reconcile attempted/confirmed accounts only;
// lost artifacts, ACKs or confirmation never grant registration replay/reset.
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
	fresh := false
	if e == nil {
		if saved.Format != 2 || saved.ServerID != r.Config.ID || saved.ThreadID != threadID || saved.Account != account || saved.UpdatedAt.IsZero() || saved.State != "claimed" && saved.State != "attempted" && saved.State != "confirmed" {
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
		fresh = true
		saved = threadAccountRecord{Format: 2, ServerID: r.Config.ID, ThreadID: threadID, Account: account, State: "claimed", UpdatedAt: time.Now().UTC()}
		if writeJSON(p, saved, false) != nil {
			return Credential{}, ErrRepair
		}
	}
	if fresh {
		// Publish the original private pair, then consume the thread's one-shot
		// permission before any account API call. A crash before account IO may
		// need repair; it cannot be interpreted as permission to register again.
		credential, _, e := m.prepareAccount(r.Config, account)
		if e != nil {
			return Credential{}, e
		}
		_, sha, e := readAccountSecret(account, credential.CredentialRef)
		if e != nil {
			return Credential{}, e
		}
		saved.State, saved.CredentialSHA256, saved.UpdatedAt = "attempted", sha, time.Now().UTC()
		if writeJSON(p, saved, true) != nil {
			return Credential{}, ErrRepair
		}
	}
	if _, e = m.threadAccountAuthority(r.Config, saved, fresh); e != nil {
		return Credential{}, e
	}
	credential, e := m.ensureAccountLocked(ctx, r, account)
	if e != nil {
		return Credential{}, e
	}
	proof, e := m.threadAccountAuthority(r.Config, saved, false)
	if e != nil || proof.State != "confirmed" || !validTimestamp(proof.RegisteredAt) {
		return Credential{}, ErrRepair
	}
	saved.State, saved.CredentialSHA256, saved.RegisteredAt, saved.UpdatedAt = "confirmed", proof.CredentialSHA256, proof.RegisteredAt, time.Now().UTC()
	if writeJSON(p, saved, true) != nil {
		return Credential{}, ErrRepair
	}
	return credential, nil
}

// This check is read-only and runs before ensureAccountLocked can prepare any
// credentials or send account IO. It also rejects replacement prepared pairs
// inside an existing directory and credential/incarnation drift. Historical
// format-2 claims acquire pins only from intact, already attempted authority.
func (m *Manager) threadAccountAuthority(c Config, claim threadAccountRecord, fresh bool) (accountRecord, error) {
	var saved accountRecord
	switch claim.State {
	case "claimed":
		if claim.CredentialSHA256 != "" || claim.RegisteredAt != "" {
			return saved, ErrRepair
		}
	case "attempted":
		if claim.CredentialSHA256 == "" || claim.RegisteredAt != "" {
			return saved, ErrRepair
		}
	case "confirmed":
		if (claim.CredentialSHA256 == "") != (claim.RegisteredAt == "") || claim.RegisteredAt != "" && !validTimestamp(claim.RegisteredAt) {
			return saved, ErrRepair
		}
	default:
		return saved, ErrRepair
	}
	dir := filepath.Join(m.dir, "accounts", claim.Account)
	info, e := os.Lstat(dir)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return saved, ErrRepair
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return saved, ErrRepair
	}
	_, sha, e := readAccountSecret(claim.Account, filepath.Join(dir, "credential.json"))
	if e != nil || readJSON(filepath.Join(dir, "account.json"), &saved) != nil || saved.Format != 1 || saved.ServerID != string(c.ID) || saved.Account != claim.Account || saved.CredentialSHA256 != sha || saved.UpdatedAt.IsZero() {
		return saved, ErrRepair
	}
	if claim.CredentialSHA256 != "" && claim.CredentialSHA256 != sha || claim.RegisteredAt != "" && (!validTimestamp(claim.RegisteredAt) || saved.RegisteredAt != "" && saved.RegisteredAt != claim.RegisteredAt) {
		return saved, ErrRepair
	}
	switch saved.State {
	case "prepared":
		if !fresh || claim.State != "attempted" || claim.CredentialSHA256 != sha || saved.RegisteredAt != "" || claim.RegisteredAt != "" {
			return saved, ErrRepair
		}
	case "attempted":
		if saved.RegisteredAt != "" && !validTimestamp(saved.RegisteredAt) || claim.State == "confirmed" && claim.RegisteredAt == "" {
			return saved, ErrRepair
		}
	case "confirmed":
		if !validTimestamp(saved.RegisteredAt) {
			return saved, ErrRepair
		}
	default:
		return saved, ErrRepair
	}
	return saved, nil
}

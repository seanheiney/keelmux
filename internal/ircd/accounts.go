package ircd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/seanheiney/keelmux/internal/config"
)

type accountRecord struct {
	Format                                         int
	ServerID                                       string
	Account, CredentialSHA256, State, RegisteredAt string
	UpdatedAt                                      time.Time
}

const maxManagedAccounts = 128

// ValidAccount accepts one canonical services account spelling. This bounded
// ASCII subset has no IRC case aliases or filesystem path components.
func ValidAccount(account string) bool {
	if len(account) < 1 || len(account) > 32 || account[0] < 'a' || account[0] > 'z' {
		return false
	}
	for _, ch := range account {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

func (m *Manager) accountSecret(c Config) (saslSecret, string, error) {
	return readAccountSecret(c.Account, c.CredentialRef)
}
func readAccountSecret(account, ref string) (s saslSecret, sha string, err error) {
	b, e := readFile(ref, 8192, 0600)
	if e != nil || strictJSON(b, &s) != nil || s.Account != account || len(s.Password) != 64 {
		return s, "", ErrRepair
	}
	for _, r := range s.Password {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return s, "", ErrRepair
		}
	}
	return s, digest(b), nil
}

// prepareAccount atomically publishes a private secret and prepared journal for
// each optional human services identity. A partial stage is never adopted; no
// remote request has been sent until the journal changes to attempted.
func (m *Manager) prepareAccount(c Config, account string) (Credential, string, error) {
	if !ValidAccount(account) {
		return Credential{}, "", ErrConflict
	}
	if account == c.Account {
		return Credential{Account: account, CredentialRef: c.CredentialRef}, filepath.Join(m.dir, "account.json"), nil
	}
	dir := filepath.Join(m.dir, "accounts")
	if config.EnsurePrivateDirectory(dir) != nil {
		return Credential{}, "", ErrRepair
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) > maxManagedAccounts-1 {
		return Credential{}, "", ErrRepair
	}
	found := false
	for _, entry := range entries {
		if !ValidAccount(entry.Name()) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || config.EnsurePrivateDirectory(filepath.Join(dir, entry.Name())) != nil {
			return Credential{}, "", ErrRepair
		}
		found = found || entry.Name() == account
	}
	destination := filepath.Join(dir, account)
	cred := Credential{Account: account, CredentialRef: filepath.Join(destination, "credential.json")}
	journal := filepath.Join(destination, "account.json")
	if found {
		return cred, journal, nil
	}
	if len(entries) >= maxManagedAccounts-1 {
		return Credential{}, "", ErrConflict
	}
	stage, e := os.MkdirTemp(dir, ".account-")
	if e != nil {
		return Credential{}, "", ErrRepair
	}
	defer os.RemoveAll(stage)
	if os.Chmod(stage, 0700) != nil {
		return Credential{}, "", ErrRepair
	}
	password, e := randomHex(32)
	if e != nil {
		return Credential{}, "", e
	}
	b, e := json.Marshal(saslSecret{Account: account, Password: password})
	if e != nil || publish(filepath.Join(stage, "credential.json"), b, 0600, false) != nil {
		return Credential{}, "", ErrRepair
	}
	record := accountRecord{Format: 1, ServerID: string(c.ID), Account: account, CredentialSHA256: digest(b), State: "prepared", UpdatedAt: time.Now().UTC()}
	if writeJSON(filepath.Join(stage, "account.json"), record, false) != nil || syncDir(stage) != nil || renameDirectoryExclusive(stage, destination) != nil || syncDir(dir) != nil {
		return Credential{}, "", ErrRepair
	}
	return cred, journal, nil
}
func (m *Manager) accountProof(ctx context.Context, c Config, s saslSecret) (accountResponse, error) {
	var a authResponse
	if m.api(ctx, c, "/v1/check_auth", authInput{Account: s.Account, Password: s.Password}, &a) != nil {
		return accountResponse{}, ErrUnavailable
	}
	if !a.Success || a.Account != s.Account {
		return accountResponse{}, ErrConflict
	}
	var info accountResponse
	if m.api(ctx, c, "/v1/ns/info", struct {
		Account string `json:"accountName"`
	}{s.Account}, &info) != nil {
		return info, ErrUnavailable
	}
	if !info.Success || info.Account != s.Account || !validTimestamp(info.RegisteredAt) || len(info.Channels) > 128 {
		return accountResponse{}, ErrConflict
	}
	return info, nil
}

// EnsureAccount registers one private managed services identity once. Each
// identity has a separate credential and durable intent. An admitted attempt is
// never sent again; matching account/password proof reconciles a lost ACK.
func (m *Manager) EnsureAccount(ctx context.Context, account string) (Credential, error) {
	unlock, e := m.lock(ctx, false)
	if e != nil {
		return Credential{}, e
	}
	defer unlock()
	r, e := m.readConfig(ctx)
	if e != nil || r.Stage != "ready" {
		return Credential{}, ErrRepair
	}
	c := r.Config
	if e = m.requireLive(ctx, r); e != nil {
		return Credential{}, e
	}
	if !ValidAccount(account) {
		return Credential{}, ErrConflict
	}
	if _, e = m.probe(ctx, c); e != nil {
		return Credential{}, e
	}
	cred, p, e := m.prepareAccount(c, account)
	if e != nil {
		return Credential{}, e
	}
	secret, sha, e := readAccountSecret(account, cred.CredentialRef)
	if e != nil {
		return Credential{}, e
	}
	var saved accountRecord
	e = readJSON(p, &saved)
	hasRecord := e == nil
	if e == nil {
		if saved.Format != 1 || saved.ServerID != string(c.ID) || saved.Account != account || saved.CredentialSHA256 != sha || saved.UpdatedAt.IsZero() || (saved.State != "prepared" && saved.State != "attempted" && saved.State != "confirmed") || (saved.State == "prepared" && saved.RegisteredAt != "") || (saved.State == "confirmed" && !validTimestamp(saved.RegisteredAt)) {
			return Credential{}, ErrRepair
		}
		if saved.State != "prepared" {
			proof, e := m.accountProof(ctx, c, secret)
			if e != nil {
				return Credential{}, ErrUncertain
			}
			if saved.RegisteredAt != "" && saved.RegisteredAt != proof.RegisteredAt {
				return Credential{}, ErrConflict
			}
			saved.State = "confirmed"
			saved.RegisteredAt = proof.RegisteredAt
			saved.UpdatedAt = time.Now().UTC()
			if writeJSON(p, saved, true) != nil {
				return Credential{}, ErrRepair
			}
			return cred, nil
		}
	} else if !os.IsNotExist(e) || account != c.Account {
		return Credential{}, ErrRepair
	}
	var exists accountResponse
	if m.api(ctx, c, "/v1/ns/info", struct {
		Account string `json:"accountName"`
	}{account}, &exists) != nil {
		return Credential{}, ErrUnavailable
	}
	if exists.Success || exists.Error != "" || exists.ErrorCode != "" || exists.Account != "" || exists.RegisteredAt != "" || len(exists.Channels) != 0 {
		return Credential{}, ErrConflict
	}
	saved = accountRecord{Format: 1, ServerID: string(c.ID), Account: account, CredentialSHA256: sha, State: "attempted", UpdatedAt: time.Now().UTC()}
	if writeJSON(p, saved, hasRecord) != nil {
		return Credential{}, ErrRepair
	}
	var ack apiGeneric
	if m.api(ctx, c, "/v1/ns/saregister", authInput{Account: account, Password: secret.Password}, &ack) != nil {
		return Credential{}, ErrUncertain
	}
	if !ack.Success {
		return Credential{}, ErrUncertain
	}
	proof, e := m.accountProof(ctx, c, secret)
	if e != nil {
		return Credential{}, ErrUncertain
	}
	saved.State = "confirmed"
	saved.RegisteredAt = proof.RegisteredAt
	saved.UpdatedAt = time.Now().UTC()
	if writeJSON(p, saved, true) != nil {
		return Credential{}, ErrRepair
	}
	return cred, nil
}

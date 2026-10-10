package ircd

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

type accountRecord struct {
	Format                                         int
	ServerID                                       string
	Account, CredentialSHA256, State, RegisteredAt string
	UpdatedAt                                      time.Time
}

func (m *Manager) accountSecret(c Config) (saslSecret, string, error) {
	var s saslSecret
	b, e := readFile(c.CredentialRef, 8192, 0600)
	if e != nil || strictJSON(b, &s) != nil || s.Account != c.Account || len(s.Password) != 64 {
		return s, "", ErrRepair
	}
	for _, r := range s.Password {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return s, "", ErrRepair
		}
	}
	return s, digest(b), nil
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

// EnsureAccount registers the private bot once. An admitted attempt is never
// sent again; matching password/account proof reconciles a lost acknowledgement.
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
	if account != c.Account {
		return Credential{}, ErrConflict
	}
	if _, e = m.probe(ctx, c); e != nil {
		return Credential{}, e
	}
	secret, sha, e := m.accountSecret(c)
	if e != nil {
		return Credential{}, e
	}
	p := filepath.Join(m.dir, "account.json")
	var saved accountRecord
	e = readJSON(p, &saved)
	if e == nil {
		if saved.Format != 1 || saved.ServerID != string(c.ID) || saved.Account != account || saved.CredentialSHA256 != sha || (saved.State != "attempted" && saved.State != "confirmed") {
			return Credential{}, ErrRepair
		}
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
		return Credential{Account: account, CredentialRef: c.CredentialRef}, nil
	} else if !os.IsNotExist(e) {
		return Credential{}, ErrRepair
	}
	var exists accountResponse
	if m.api(ctx, c, "/v1/ns/info", struct {
		Account string `json:"accountName"`
	}{account}, &exists) != nil {
		return Credential{}, ErrUnavailable
	}
	if exists.Success || exists.Error != "" || exists.ErrorCode != "" {
		return Credential{}, ErrConflict
	}
	saved = accountRecord{Format: 1, ServerID: string(c.ID), Account: account, CredentialSHA256: sha, State: "attempted", UpdatedAt: time.Now().UTC()}
	if writeJSON(p, saved, false) != nil {
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
	return Credential{Account: account, CredentialRef: c.CredentialRef}, nil
}

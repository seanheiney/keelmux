package ircd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
)

func TestThreadClaimRequiresOriginalAccountAuthority(t *testing.T) {
	for _, state := range []string{"attempted", "confirmed", "legacy-claimed", "legacy-confirmed"} {
		for _, loss := range []string{"directory", "credential", "journal", "replacement-prepared", "replacement-credential", "registered-incarnation"} {
			t.Run(state+"/"+loss, func(t *testing.T) {
				m, cfg, claim, credential, journal, account := threadAuthorityFixture(t)
				switch state {
				case "attempted":
					claim.State, claim.RegisteredAt = "attempted", ""
					account.State, account.RegisteredAt = "attempted", ""
				case "legacy-claimed":
					claim.State, claim.CredentialSHA256, claim.RegisteredAt = "claimed", "", ""
					account.State, account.RegisteredAt = "attempted", ""
				case "legacy-confirmed":
					claim.CredentialSHA256, claim.RegisteredAt = "", ""
				}
				if writeJSON(journal, account, true) != nil {
					t.Fatal("fixture journal")
				}
				switch loss {
				case "directory":
					if os.RemoveAll(filepath.Dir(credential.CredentialRef)) != nil {
						t.Fatal("fixture directory loss")
					}
				case "credential":
					if os.Remove(credential.CredentialRef) != nil {
						t.Fatal("fixture credential loss")
					}
				case "journal":
					if os.Remove(journal) != nil {
						t.Fatal("fixture journal loss")
					}
				case "replacement-prepared":
					account.State, account.RegisteredAt = "prepared", ""
					if writeJSON(journal, account, true) != nil {
						t.Fatal("fixture replacement journal")
					}
				case "replacement-credential":
					if state == "legacy-claimed" || state == "legacy-confirmed" {
						t.Skip("historical claims have no digest; complete original pair is the read-only authority")
					}
					secret, _, e := readAccountSecret(claim.Account, credential.CredentialRef)
					if e != nil {
						t.Fatal(e)
					}
					secret.Password, e = randomHex(32)
					if e != nil || writeJSON(credential.CredentialRef, secret, true) != nil {
						t.Fatal("fixture replacement credential")
					}
					_, account.CredentialSHA256, e = readAccountSecret(claim.Account, credential.CredentialRef)
					if e != nil || writeJSON(journal, account, true) != nil {
						t.Fatal("fixture matching replacement journal")
					}
				case "registered-incarnation":
					if state != "confirmed" {
						t.Skip("only confirmed claims have an original registered incarnation")
					}
					account.RegisteredAt = time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)
					if writeJSON(journal, account, true) != nil {
						t.Fatal("fixture replacement incarnation")
					}
				}
				before, _ := os.ReadDir(filepath.Join(m.dir, "accounts"))
				if _, e := m.threadAccountAuthority(cfg, claim, false); e != ErrRepair {
					t.Fatal("lost or replaced authority granted account permission", e)
				}
				after, _ := os.ReadDir(filepath.Join(m.dir, "accounts"))
				if len(before) != len(after) {
					t.Fatal("authority check recreated account state")
				}
			})
		}
	}
}

func TestThreadFreshPermissionAndReadOnlyLostACK(t *testing.T) {
	m, cfg, claim, _, journal, account := threadAuthorityFixture(t)
	claim.State, claim.RegisteredAt = "attempted", ""
	account.State, account.RegisteredAt = "prepared", ""
	if writeJSON(journal, account, true) != nil {
		t.Fatal("fixture prepared journal")
	}
	if _, e := m.threadAccountAuthority(cfg, claim, true); e != nil {
		t.Fatal("fresh one-shot permission refused", e)
	}
	if _, e := m.threadAccountAuthority(cfg, claim, false); e != ErrRepair {
		t.Fatal("existing attempted claim replayed prepared journal", e)
	}
	account.State = "attempted"
	if writeJSON(journal, account, true) != nil {
		t.Fatal("fixture attempted journal")
	}
	if _, e := m.threadAccountAuthority(cfg, claim, false); e != nil {
		t.Fatal("lost ACK original authority refused", e)
	}
	claim.CredentialSHA256, claim.State = "", "claimed"
	if _, e := m.threadAccountAuthority(cfg, claim, false); e != nil {
		t.Fatal("intact historical attempted authority refused", e)
	}
	account.State = "prepared"
	if writeJSON(journal, account, true) != nil {
		t.Fatal("fixture legacy prepared journal")
	}
	if _, e := m.threadAccountAuthority(cfg, claim, false); e != ErrRepair {
		t.Fatal("historical unknown claim granted mutation replay", e)
	}
}

func threadAuthorityFixture(t *testing.T) (*Manager, Config, threadAccountRecord, Credential, string, accountRecord) {
	t.Helper()
	dir := t.TempDir()
	if os.Chmod(dir, 0700) != nil {
		t.Fatal("private fixture")
	}
	m, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	cfg := Config{ID: domain.NewID("ircd"), Account: "keelmux-bot", CredentialRef: filepath.Join(dir, "bot.json")}
	thread := domain.NewID("thread")
	name, e := ThreadAccount(thread)
	if e != nil {
		t.Fatal(e)
	}
	credential, journal, e := m.prepareAccount(cfg, name)
	if e != nil {
		t.Fatal(e)
	}
	var account accountRecord
	if readJSON(journal, &account) != nil {
		t.Fatal("private prepared fixture")
	}
	account.State, account.RegisteredAt = "confirmed", time.Now().UTC().Format(time.RFC3339Nano)
	if writeJSON(journal, account, true) != nil {
		t.Fatal("private confirmed fixture")
	}
	claim := threadAccountRecord{Format: 2, ServerID: cfg.ID, ThreadID: thread, Account: name, State: "confirmed", CredentialSHA256: account.CredentialSHA256, RegisteredAt: account.RegisteredAt, UpdatedAt: time.Now().UTC()}
	return m, cfg, claim, credential, journal, account
}

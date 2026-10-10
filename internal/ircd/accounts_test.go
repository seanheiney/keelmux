package ircd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func TestCanonicalManagedAccounts(t *testing.T) {
	for _, name := range []string{"keelmux-bot", "operator-one", "a", "operator_2", strings.Repeat("a", 32)} {
		if !ValidAccount(name) {
			t.Fatal("canonical account refused")
		}
	}
	for _, name := range []string{"", "Operator", "operator.one", "1operator", "operator[one]", "operator\\one", "../operator", "operator\n", "oñerator", strings.Repeat("a", 33)} {
		if ValidAccount(name) {
			t.Fatal("noncanonical account accepted")
		}
	}
}

func TestPrivateAccountPreparation(t *testing.T) {
	dir := t.TempDir()
	if os.Chmod(dir, 0700) != nil {
		t.Fatal("private fixture")
	}
	m, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	cfg := Config{ID: domain.NewID("ircd"), Account: "keelmux-bot", CredentialRef: filepath.Join(dir, "bot.json")}
	bot, p, e := m.prepareAccount(cfg, cfg.Account)
	if e != nil || bot.CredentialRef != cfg.CredentialRef || p != filepath.Join(dir, "account.json") {
		t.Fatal("legacy bot path changed", e)
	}
	var original Credential
	for i := 0; i < maxManagedAccounts-1; i++ {
		cred, journal, e := m.prepareAccount(cfg, fmt.Sprintf("operator-%03d", i))
		if e != nil {
			t.Fatal("bounded preparation", e)
		}
		if i == 0 {
			original = cred
		}
		secret, sha, e := readAccountSecret(cred.Account, cred.CredentialRef)
		if e != nil || secret.Account != cred.Account || len(secret.Password) != 64 {
			t.Fatal("private credential", e)
		}
		var saved accountRecord
		if readJSON(journal, &saved) != nil || saved.State != "prepared" || saved.ServerID != string(cfg.ID) || saved.CredentialSHA256 != sha {
			t.Fatal("atomic credential/journal pair")
		}
	}
	before, _ := readFile(original.CredentialRef, 8192, 0600)
	again, _, e := m.prepareAccount(cfg, original.Account)
	after, _ := readFile(original.CredentialRef, 8192, 0600)
	if e != nil || again != original || string(before) != string(after) {
		t.Fatal("prepared credential replaced", e)
	}
	if _, _, e = m.prepareAccount(cfg, "overflow"); e != ErrConflict {
		t.Fatal("account capacity exceeded", e)
	}
	if _, _, e = m.prepareAccount(cfg, "../escape"); e != ErrConflict {
		t.Fatal("account path escaped", e)
	}
}

func TestPrivateAccountDirectoryRefusal(t *testing.T) {
	for _, fixture := range []string{"symlink", "public", "partial-stage"} {
		t.Run(fixture, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.Chmod(dir, 0700)
			m, e := New(dir)
			if e != nil {
				t.Fatal(e)
			}
			cfg := Config{ID: domain.NewID("ircd"), Account: "keelmux-bot", CredentialRef: filepath.Join(dir, "bot.json")}
			accounts := filepath.Join(dir, "accounts")
			if os.Mkdir(accounts, 0700) != nil {
				t.Fatal("fixture")
			}
			switch fixture {
			case "symlink":
				if os.Symlink(t.TempDir(), filepath.Join(accounts, "operator")) != nil {
					t.Fatal("fixture")
				}
			case "public":
				p := filepath.Join(accounts, "operator")
				if os.Mkdir(p, 0700) != nil || os.Chmod(p, 0755) != nil {
					t.Fatal("fixture")
				}
			case "partial-stage":
				if os.Mkdir(filepath.Join(accounts, ".account-incomplete"), 0700) != nil {
					t.Fatal("fixture")
				}
			}
			if _, _, e = m.prepareAccount(cfg, "operator-two"); e != ErrRepair {
				t.Fatal("adopted invalid account directory", e)
			}
		})
	}
}

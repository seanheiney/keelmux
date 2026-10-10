package ircd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
)

// Real account/journal proof only: this owned foreground fixture is not a
// package or native daemon persistence claim. The product gate covers those
// separately through the integrated executables and native supervisor.
func TestRealPinnedThreadAccountsAndProjectIntent(t *testing.T) {
	archive := os.Getenv("KEELMUX_ERGO_ARCHIVE")
	if archive == "" {
		t.Skip("explicit pinned owned Ergo archive required")
	}
	raw, e := os.ReadFile(archive)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "server")
	supervisor := newTestSupervisor(dir)
	t.Cleanup(supervisor.cleanup)
	m, e := NewWithSupervisor(dir, supervisor)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if _, e = m.InstallArchive(ctx, bytes.NewReader(raw)); e != nil {
		t.Fatal(e)
	}
	cfg, e := m.Init(ctx, InitOptions{})
	if e != nil {
		t.Fatal(e)
	}
	started, e := m.Start(ctx, domain.NewID("operation"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.EnsureAccount(ctx, cfg.Account); e != nil {
		t.Fatal(e)
	}
	thread := domain.NewID("thread")
	cred, e := m.EnsureThreadAccount(ctx, thread)
	if e != nil {
		t.Fatal("real thread registration", e)
	}
	secret, sha, e := readAccountSecret(cred.Account, cred.CredentialRef)
	if e != nil || secret.Nick != cred.Account {
		t.Fatal("stable private thread nick", e)
	}
	journal := filepath.Join(filepath.Dir(cred.CredentialRef), "account.json")
	var account accountRecord
	if readJSON(journal, &account) != nil {
		t.Fatal("thread account proof journal")
	}
	originalTime := account.RegisteredAt
	account.State, account.RegisteredAt = "attempted", ""
	if writeJSON(journal, account, true) != nil {
		t.Fatal("owned lostACK fixture")
	}
	if again, e := m.EnsureThreadAccount(ctx, thread); e != nil || again != cred {
		t.Fatal("thread lostACK read-only recovery", e)
	}
	if readJSON(journal, &account) != nil || account.RegisteredAt != originalTime || account.CredentialSHA256 != sha {
		t.Fatal("thread account reset or credential replacement")
	}
	claimPath := filepath.Join(dir, "thread-accounts", string(thread)+".json")
	var claim threadAccountRecord
	if readJSON(claimPath, &claim) != nil || claim.ThreadID != thread || claim.State != "confirmed" {
		t.Fatal("full thread claim missing")
	}
	claim.ServerID = domain.NewID("ircd")
	if writeJSON(claimPath, claim, true) != nil {
		t.Fatal("owned forged claim fixture")
	}
	if _, e = m.EnsureThreadAccount(ctx, thread); e != ErrConflict {
		t.Fatal("mismatched full ownership adopted", e)
	}
	claim.ServerID = cfg.ID
	writeJSON(claimPath, claim, true)
	secretBytes, _ := readFile(cred.CredentialRef, 8192, 0600)
	secret.Nick = "foreign"
	changed, _ := json.Marshal(secret)
	os.WriteFile(cred.CredentialRef, changed, 0600)
	if _, e = m.EnsureThreadAccount(ctx, thread); e != ErrRepair {
		t.Fatal("mutated stable nick was adopted", e)
	}
	os.WriteFile(cred.CredentialRef, secretBytes, 0600)
	// A surviving thread claim never regenerates authority after the private
	// account pair disappears, even if a server snapshot later loses that
	// account. These failures occur before account IO or credential generation.
	originalClaim := claim
	originalAccount := account
	accountDirectory := filepath.Dir(cred.CredentialRef)
	for _, state := range []string{"confirmed", "attempted", "legacy-claimed", "legacy-confirmed"} {
		claim = originalClaim
		switch state {
		case "attempted":
			claim.State, claim.RegisteredAt = "attempted", ""
		case "legacy-claimed":
			claim.State, claim.CredentialSHA256, claim.RegisteredAt = "claimed", "", ""
		case "legacy-confirmed":
			claim.CredentialSHA256, claim.RegisteredAt = "", ""
		}
		if writeJSON(claimPath, claim, true) != nil {
			t.Fatal("owned recovery claim fixture")
		}
		backup := filepath.Join(dir, "original-thread-authority")
		if os.Rename(accountDirectory, backup) != nil {
			t.Fatal("owned missing-authority fixture")
		}
		if _, e = m.EnsureThreadAccount(ctx, thread); e != ErrRepair {
			t.Fatal("lost original thread authority replayed", state, e)
		}
		if _, e = os.Lstat(accountDirectory); !os.IsNotExist(e) {
			t.Fatal("lost thread credentials were regenerated", state)
		}
		if os.Rename(backup, accountDirectory) != nil {
			t.Fatal("restore owned original authority")
		}
		account.State, account.RegisteredAt = "prepared", ""
		if writeJSON(journal, account, true) != nil {
			t.Fatal("owned replacement prepared-journal fixture")
		}
		if _, e = m.EnsureThreadAccount(ctx, thread); e != ErrRepair {
			t.Fatal("replacement prepared journal replayed", state, e)
		}
		account = originalAccount
		if writeJSON(journal, account, true) != nil {
			t.Fatal("restore owned original account journal")
		}
	}
	claim = originalClaim
	if writeJSON(claimPath, claim, true) != nil {
		t.Fatal("restore owned original thread claim")
	}
	// A private account directory without an original thread claim is foreign,
	// even if locally generated with a matching deterministic account spelling.
	unclaimed := domain.NewID("thread")
	name, _ := ThreadAccount(unclaimed)
	if _, _, e = m.prepareAccount(cfg, name); e != nil {
		t.Fatal(e)
	}
	if _, e = m.EnsureThreadAccount(ctx, unclaimed); e != ErrConflict {
		t.Fatal("unclaimed local account was adopted", e)
	}
	project := domain.NewID("project")
	channel := "#km-owned-" + string(project)[8:]
	op := domain.NewID("operation")
	intent, fresh, e := m.BeginProjectChannel(ctx, channel, project, op)
	if e != nil || !fresh || intent.ProjectID != project {
		t.Fatal("fresh durable project permission", e)
	}
	repeated, fresh, e := m.BeginProjectChannel(ctx, channel, project, domain.NewID("operation"))
	if e != nil || fresh || repeated.ID != op {
		t.Fatal("unknown project mutation permission replayed", e)
	}
	if _, e = m.ConfirmProjectChannel(ctx, channel, project); e != ErrUncertain {
		t.Fatal("absent registration reported completed", e)
	}
	if _, _, e = m.BeginProjectChannel(ctx, "#km-renamed-"+string(project)[8:], project, domain.NewID("operation")); e != ErrConflict {
		t.Fatal("unknown project channel renamed", e)
	}
	if replacement, e := m.Restart(ctx, started.Instance, domain.NewID("operation")); e != nil || !replacement.Alive {
		t.Fatal("owned restart", e)
	}
	reopened, e := NewWithSupervisor(dir, supervisor)
	if e != nil {
		t.Fatal(e)
	}
	if again, e := reopened.EnsureThreadAccount(ctx, thread); e != nil || again != cred {
		t.Fatal("thread account or nick changed across server restart", e)
	}
	if readJSON(journal, &account) != nil || account.RegisteredAt != originalTime {
		t.Fatal("registered thread incarnation changed across restart")
	}
}

package ircd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
)

// This opt-in test runs the actual pinned Ergo artifact as an owned foreground
// child. It proves API/TLS/journals, not native supervisor restart persistence.
func TestRealPinnedServer(t *testing.T) {
	archive := os.Getenv("KEELMUX_ERGO_ARCHIVE")
	if archive == "" {
		t.Skip("explicit pinned real Ergo archive required")
	}
	b, e := os.ReadFile(archive)
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
	if _, e = m.InstallArchive(context.Background(), bytes.NewReader(b)); e != nil {
		t.Fatal(e)
	}
	c, e := m.Init(context.Background(), InitOptions{})
	if e != nil {
		t.Fatal(e)
	}
	operation := domain.NewID("operation")
	supervisor.loseLaunch = true
	if _, e = m.Start(context.Background(), operation); e != ErrUncertain {
		t.Fatal("lost launch ACK", e)
	}
	supervisor.loseLaunch = false
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, e := m.Status(context.Background())
		if e == nil && status.Alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real status probe", e)
		}
		time.Sleep(20 * time.Millisecond)
	}
	reopened, e := NewWithSupervisor(dir, supervisor)
	if e != nil {
		t.Fatal(e)
	}
	m = reopened
	status, e := m.Start(context.Background(), operation)
	if e != nil || !status.Alive || supervisor.launches != 1 {
		t.Fatal("readonly launch reconciliation", e)
	}
	if _, e = m.Start(context.Background(), domain.NewID("operation")); e != ErrConflict || supervisor.launches != 1 {
		t.Fatal("duplicate live start", e)
	}
	if e = m.Stop(context.Background(), domain.NewID("execution")); e != ErrConflict || supervisor.stops != 0 {
		t.Fatal("foreign instance stop", e)
	}
	supervisor.unavailable = true
	if _, e = m.Start(context.Background(), operation); e != ErrUncertain || supervisor.launches != 1 {
		t.Fatal("unknown helper replay", e)
	}
	if _, e = m.Start(context.Background(), domain.NewID("operation")); e != ErrUncertain || supervisor.launches != 1 {
		t.Fatal("new operation bypassed unknown helper", e)
	}
	if e = m.Stop(context.Background(), status.Instance); e != ErrUncertain || supervisor.stops != 0 {
		t.Fatal("unknown helper stale stop", e)
	}
	supervisor.unavailable = false
	cred, e := m.EnsureAccount(context.Background(), c.Account)
	if e != nil || cred.Account != c.Account || cred.CredentialRef != c.CredentialRef {
		t.Fatal("real registration", e)
	}
	var saved accountRecord
	if e = readJSON(filepath.Join(dir, "account.json"), &saved); e != nil {
		t.Fatal(e)
	}
	originalTime := saved.RegisteredAt
	saved.State = "attempted"
	saved.RegisteredAt = ""
	if e = writeJSON(filepath.Join(dir, "account.json"), saved, true); e != nil {
		t.Fatal(e)
	}
	if _, e = m.EnsureAccount(context.Background(), c.Account); e != nil {
		t.Fatal("read-only account reconciliation", e)
	}
	if e = readJSON(filepath.Join(dir, "account.json"), &saved); e != nil || saved.RegisteredAt != originalTime || saved.State != "confirmed" {
		t.Fatal("account incarnation changed", e)
	}
	if _, e = m.EnsureAccount(context.Background(), "foreign-account"); e != ErrConflict {
		t.Fatal("foreign account")
	}
	thread := domain.NewID("thread")
	op, eFresh, e := m.BeginChannel(context.Background(), "#keelmux-owned", thread, domain.NewID("operation"))
	if e != nil || !eFresh || op.ThreadID != thread {
		t.Fatal("channel admission", e)
	}
	again, eFresh, e := m.BeginChannel(context.Background(), "#keelmux-owned", thread, domain.NewID("operation"))
	if e != nil || eFresh || again.ID != op.ID {
		t.Fatal("channel attempt replay", e)
	}
	if _, _, e = m.BeginChannel(context.Background(), "#keelmux-owned", domain.NewID("thread"), domain.NewID("operation")); e != ErrConflict {
		t.Fatal("retarget channel")
	}
	if _, e = m.ConfirmChannel(context.Background(), "#keelmux-owned", thread); e != ErrUncertain {
		t.Fatal("invented registration proof", e)
	}
	supervisor.loseStop = true
	if e = m.Stop(context.Background(), status.Instance); e != ErrUncertain {
		t.Fatal("lost stop ACK", e)
	}
	supervisor.loseStop = false
	if e = m.Stop(context.Background(), status.Instance); e != nil || supervisor.stops != 1 {
		t.Fatal("read-only stop reconciliation", e)
	}
	replacement, e := m.Restart(context.Background(), status.Instance, domain.NewID("operation"))
	if e != nil || !replacement.Alive || replacement.Instance == status.Instance || supervisor.launches != 2 {
		t.Fatal("explicit replacement", e)
	}
	if e = m.Stop(context.Background(), status.Instance); e != ErrConflict {
		t.Fatal("stale instance accepted")
	}
	if _, e = m.EnsureAccount(context.Background(), c.Account); e != nil {
		t.Fatal("durable account survived real server replacement", e)
	}
	var afterRestart accountRecord
	if e = readJSON(filepath.Join(dir, "account.json"), &afterRestart); e != nil || afterRestart.RegisteredAt != originalTime {
		t.Fatal("restart replaced account identity", e)
	}
	token, _ := readFile(filepath.Join(dir, "admin.token"), 8192, 0600)
	token[0] = 'x'
	publish(filepath.Join(dir, "admin.token"), token, 0600, true)
	if _, e = m.EnsureAccount(context.Background(), c.Account); e == nil {
		t.Fatal("bad API token accepted")
	}
}

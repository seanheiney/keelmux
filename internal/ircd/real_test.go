package ircd

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
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
	m, e := New(dir)
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
	cmd := exec.Command("/bin/sh", "-c", `umask 077; exec "$@"`, "private-ergo", c.Runtime.Path, "run", "--conf", filepath.Join(dir, "ergo.yaml"), "--quiet")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, e = m.probe(context.Background(), c)
		if e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real status probe", e)
		}
		time.Sleep(20 * time.Millisecond)
	}
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
	token, _ := readFile(filepath.Join(dir, "admin.token"), 8192, 0600)
	token[0] = 'x'
	publish(filepath.Join(dir, "admin.token"), token, 0600, true)
	if _, e = m.EnsureAccount(context.Background(), c.Account); e == nil {
		t.Fatal("bad API token accepted")
	}
}

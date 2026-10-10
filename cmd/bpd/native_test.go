package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/adapters/nativepty"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

// Exercise the actual control executable with a PATH containing only our owned
// binaries. No tmux, native credentials, vendor CLI, or model activity is used.
func TestNativeDefaultSurvivesActualDaemonLossWithoutTmux(t *testing.T) {
	dir, e := os.MkdirTemp("/tmp", "km-daemon-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "bin")
	if e = os.Mkdir(bin, 0700); e != nil {
		t.Fatal(e)
	}
	for _, pair := range [][2]string{{"keelmuxd", "bpd"}, {"keelmux-supervisor", "keelmux-supervisor"}} {
		build := exec.Command("go", "build", "-o", filepath.Join(bin, pair[0]), "./cmd/"+pair[1])
		build.Dir = "../.."
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", pair[0], err, out)
		}
	}
	state := filepath.Join(dir, "state")
	client := protocol.NewClient(filepath.Join(state, "control.sock"))
	defer client.HTTP.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var control *exec.Cmd
	start := func() {
		control = exec.Command(filepath.Join(bin, "keelmuxd"), "--state", state)
		control.Env = []string{"PATH=" + bin, "HOME=" + dir, "TERM=xterm-256color", "LANG=C.UTF-8"}
		if err := control.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := client.List(ctx); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("native daemon did not become ready without tmux")
	}
	defer func() {
		if control != nil && control.Process != nil {
			_ = control.Process.Kill()
			_ = control.Wait()
		}
	}()
	start()
	operation := domain.NewID("op")
	request := protocol.LaunchRequest{OperationID: operation, Name: "native owned fixture", Workspace: dir, Provider: "fixture", ProviderVersion: "test", Argv: []string{"/bin/sh", "-c", "printf 'owned native ready'; trap 'exit 0' TERM; while :; do /bin/sleep 1; done"}}
	launched, err := client.Launch(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	ref := launched.Session.Execution.Runtime
	native, err := nativepty.NewWithOptions(filepath.Join(state, "native"), nativepty.Options{HelperPath: filepath.Join(bin, "keelmux-supervisor")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if ref.ExecutionID == "" {
			if o, e := native.Lookup(context.Background(), launched.Session.Execution.ID, operation); e == nil {
				ref = o.Ref
			}
		}
		if e := native.Stop(context.Background(), ref); e != nil {
			t.Error("owned native cleanup", e)
		}
	}()
	if launched.Session.Execution.State != "running" || ref.Backend != "native" || ref.Pane != "" || ref.ExecutionID == "" || ref.PID <= 0 || ref.ProcessStart == "" {
		t.Fatalf("native launch not established: state=%s ref=%+v", launched.Session.Execution.State, ref)
	}
	for _, path := range []string{"tmux.sock", "tmux.sock.bp", "irc"} {
		if _, e := os.Lstat(filepath.Join(state, path)); !os.IsNotExist(e) {
			t.Fatalf("optional subsystem created implicitly: %s %v", path, e)
		}
	}
	before, err := client.Daemon(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if e := control.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = control.Wait()
	control = nil
	observed, e := native.Inspect(ctx, ref)
	if e != nil || !observed.Alive || observed.Ref != ref {
		t.Fatalf("daemon loss lost native authority: %+v %v", observed, e)
	}
	start()
	after, err := client.Daemon(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Instance == before.Instance || after.PID == before.PID {
		t.Fatal("control process was not replaced")
	}
	sessions, err := client.List(ctx)
	if err != nil || len(sessions) != 1 || sessions[0].Execution.Runtime != ref || sessions[0].Execution.State != "running" {
		t.Fatalf("restart changed owned execution: %+v %v", sessions, err)
	}
	same, err := client.Launch(ctx, request)
	if err != nil || same.Session.Execution.Runtime != ref {
		t.Fatalf("same operation replayed launch: %+v %v", same, err)
	}
	if err = client.Stop(ctx, protocol.SessionRequest{SessionID: launched.Session.ID, ExecutionID: ref.ExecutionID, Generation: launched.Session.Execution.Generation, OperationID: domain.NewID("op")}); err != nil {
		t.Fatal(err)
	}
	observed, e = native.Inspect(ctx, ref)
	if e != nil || observed.Alive {
		t.Fatalf("exact stop unconfirmed: %+v %v", observed, e)
	}
}

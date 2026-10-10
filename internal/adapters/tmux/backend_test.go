package tmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
)

type helperResult struct {
	Args      []string
	Env       map[string]string
	Workspace string
}

func TestPaneHelper(t *testing.T) {
	if os.Getenv("AB_TEST_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(2)
	}
	result := helperResult{Args: args[2:], Env: map[string]string{}}
	for _, key := range []string{"BP_SESSION_ID", "BP_EXECUTION_ID", "BP_OPERATION_ID", "BP_HOST_ID", "BP_LINK_TOKEN", "BP_STATE_DIR", "AB_LITERAL_TEST", "TERM"} {
		result.Env[key] = os.Getenv(key)
	}
	result.Workspace, _ = os.Getwd()
	data, _ := json.Marshal(result)
	if err := os.WriteFile(args[1], data, 0600); err != nil {
		os.Exit(3)
	}
	if os.Getenv("AB_RAW_KEYS") == "1" {
		cmd := exec.Command("stty", "raw", "-echo")
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			os.Exit(4)
		}
		keyFile, err := os.OpenFile(args[1]+".keys", os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(5)
		}
		_, _ = io.Copy(keyFile, os.Stdin)
		_ = keyFile.Close()
		os.Exit(0)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func backendForTest(t *testing.T) *Backend {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed; native backend integration not run")
	}
	dir, err := os.MkdirTemp("/tmp", "bp-tmux-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "socket")
	b, err := New(socket)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(b.binary, "-S", socket, "kill-server").Run(); _ = os.RemoveAll(dir) })
	return b
}

func fixtureSpec(t *testing.T, b *Backend) (domain.LaunchSpec, string) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(filepath.Dir(b.socket), "workspace with ' quotes; $(touch should-not-exist)")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(workspace, string(domain.NewID("fixture"))+".json")
	session := domain.NewSession(domain.NewID("host"), "native test", workspace, "fixture", "test")
	return domain.LaunchSpec{Session: session, OperationID: domain.NewID("operation"), Argv: []string{binary, "-test.run=^TestPaneHelper$", "--", output}, Env: []string{"AB_TEST_HELPER=1"}}, output
}

func readHelper(t *testing.T, path string) helperResult {
	t.Helper()
	var result helperResult
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, &result) == nil {
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native helper did not write launch result")
	return result
}

func TestLifecycleExactIdentityAndLostAcknowledgement(t *testing.T) {
	b := backendForTest(t)
	spec, output := fixtureSpec(t, b)
	ctx := context.Background()
	ref, err := b.Launch(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	result := readHelper(t, output)
	want := map[string]string{"BP_SESSION_ID": string(spec.Session.ID), "BP_EXECUTION_ID": string(spec.Session.Execution.ID), "BP_OPERATION_ID": string(spec.OperationID), "BP_HOST_ID": string(spec.Session.HostID), "BP_STATE_DIR": filepath.Dir(b.socket)}
	for key, expected := range want {
		if result.Env[key] != expected {
			t.Errorf("%s = %q, want %q", key, result.Env[key], expected)
		}
	}
	if result.Env["BP_LINK_TOKEN"] == "" || result.Env["TERM"] != "tmux-256color" {
		t.Fatalf("missing native context: %+v", result.Env)
	}
	if result.Workspace != spec.Session.Workspace {
		t.Errorf("workspace = %q", result.Workspace)
	}
	if ref.PID == 0 || ref.Pane == "" || ref.ProcessStart == "" || ref.BootID == "" {
		t.Fatalf("missing process evidence: %+v", ref)
	}
	if err := b.ValidateLink(ctx, spec.Session.ID, ref.ExecutionID, ref.OperationID, result.Env["BP_LINK_TOKEN"]); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "forged_token"} {
		if err := b.ValidateLink(ctx, spec.Session.ID, ref.ExecutionID, ref.OperationID, token); err == nil {
			t.Fatal("forged link accepted")
		}
	}
	// Reconstruct the backend as if the daemon died after a successful launch,
	// before the client received its acknowledgement.
	restarted, err := New(b.socket)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := restarted.Lookup(ctx, ref.ExecutionID, ref.OperationID)
	if err != nil || !observation.Alive || observation.Ref != ref {
		t.Fatalf("survivor lookup: %+v %v", observation, err)
	}
	repeated, err := restarted.Launch(ctx, spec)
	if err != nil || repeated != ref {
		t.Fatalf("lost-ack launch changed process: %+v %v", repeated, err)
	}
	if _, err := restarted.Lookup(ctx, ref.ExecutionID, domain.NewID("wrong_operation")); err == nil {
		t.Fatal("wrong operation accepted")
	}
	if err := restarted.Stop(ctx, ref); err != nil {
		t.Fatal(err)
	}
	observation, err = restarted.Inspect(ctx, ref)
	if err != nil || observation.Alive {
		t.Fatalf("stop inspection: %+v %v", observation, err)
	}
	if err := restarted.Stop(ctx, ref); err != nil {
		t.Fatal("idempotent stop:", err)
	}
	if _, err := restarted.Launch(ctx, spec); err == nil {
		t.Fatal("stopped execution replayed")
	}
	if err := restarted.ValidateLink(ctx, spec.Session.ID, ref.ExecutionID, ref.OperationID, result.Env["BP_LINK_TOKEN"]); err == nil {
		t.Fatal("dead link accepted")
	}
}

func TestArgvAndEnvironmentAreLiteral(t *testing.T) {
	b := backendForTest(t)
	spec, output := fixtureSpec(t, b)
	literal := "a 'quote' \"double\"; $(touch " + filepath.Join(filepath.Dir(b.socket), "must-not-exist") + ") `false`\nUnicode 🛠"
	spec.Argv = append(spec.Argv, literal, "", ";", "-argument", "#{session_id}")
	spec.Env = append(spec.Env, "AB_LITERAL_TEST="+literal, "BP_STATE_DIR="+filepath.Join(filepath.Dir(b.socket), "explicit state"))
	if _, err := b.Launch(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	result := readHelper(t, output)
	if !reflect.DeepEqual(result.Args, spec.Argv[4:]) || result.Env["AB_LITERAL_TEST"] != literal {
		t.Fatalf("argv/env changed: %+v", result)
	}
	if result.Env["BP_STATE_DIR"] != strings.TrimPrefix(spec.Env[2], "BP_STATE_DIR=") {
		t.Fatal("explicit state ignored")
	}
}

func TestIdentityForgeryAndMarkerChangeFailClosed(t *testing.T) {
	b := backendForTest(t)
	spec, output := fixtureSpec(t, b)
	ctx := context.Background()
	ref, err := b.Launch(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	result := readHelper(t, output)
	mutations := []func(*domain.RuntimeRef){
		func(r *domain.RuntimeRef) { r.PID++ }, func(r *domain.RuntimeRef) { r.Pane = "%999" },
		func(r *domain.RuntimeRef) { r.Session = "$999" }, func(r *domain.RuntimeRef) { r.BootID = "forged" },
		func(r *domain.RuntimeRef) { r.ProcessStart = "forged" }, func(r *domain.RuntimeRef) { r.Socket += "wrong" },
		func(r *domain.RuntimeRef) { r.OperationID = "wrong" }, func(r *domain.RuntimeRef) { r.ExecutionID = "wrong" },
	}
	for i, mutate := range mutations {
		forged := ref
		mutate(&forged)
		if _, err := b.Inspect(ctx, forged); err == nil {
			t.Fatalf("forgery %d accepted", i)
		}
		if err := b.Stop(ctx, forged); err == nil {
			t.Fatalf("forgery %d could stop", i)
		}
	}
	if _, err := b.runCommand(ctx, "set-option", "-p", "-t", ref.Pane, "@bp_execution", "forged"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Inspect(ctx, ref); err == nil {
		t.Fatal("forged pane marker accepted")
	}
	if err := b.ValidateLink(ctx, spec.Session.ID, ref.ExecutionID, ref.OperationID, result.Env["BP_LINK_TOKEN"]); err == nil {
		t.Fatal("link accepted altered marker")
	}
	if err := b.Stop(ctx, ref); err == nil {
		t.Fatal("stop acted on altered marker")
	}
	if _, err := b.runCommand(ctx, "set-option", "-p", "-t", ref.Pane, "@bp_execution", string(ref.ExecutionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.runCommand(ctx, "set-option", "-t", ref.Session, "@bp_start", "forged"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Inspect(ctx, ref); err == nil {
		t.Fatal("forged session process marker accepted")
	}
}

func TestConcurrentLaunchesCreateOnePane(t *testing.T) {
	b := backendForTest(t)
	spec, _ := fixtureSpec(t, b)
	other, err := New(b.socket)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	refs := make([]domain.RuntimeRef, 8)
	errs := make([]error, len(refs))
	for i := range refs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			backend := b
			if i%2 == 1 {
				backend = other
			}
			refs[i], errs[i] = backend.Launch(context.Background(), spec)
		}(i)
	}
	wg.Wait()
	for i := range refs {
		if errs[i] != nil || refs[i] != refs[0] {
			t.Fatalf("launch %d: %+v %v", i, refs[i], errs[i])
		}
	}
	out, err := b.runCommand(context.Background(), "list-panes", "-a", "-F", "#{pane_id}")
	if err != nil || len(strings.Fields(string(out))) != 1 {
		t.Fatalf("duplicate panes: %q %v", out, err)
	}
	changed := spec
	changed.Argv = append(append([]string{}, spec.Argv...), "changed")
	if _, err := b.Launch(context.Background(), changed); err == nil {
		t.Fatal("changed launch replay accepted")
	}
}

func TestPartialCreateTombstoneNeverReleasesOrDuplicates(t *testing.T) {
	b := backendForTest(t)
	spec, output := fixtureSpec(t, b)
	ctx := context.Background()
	b.runCommand = func(ctx context.Context, args ...string) ([]byte, error) {
		out, err := b.command(ctx, args...)
		if args[0] == "new-session" && err == nil {
			return nil, errors.New("injected lost creation acknowledgement")
		}
		return out, err
	}
	ref, err := b.Launch(ctx, spec)
	if err == nil || !strings.Contains(err.Error(), "tombstone") {
		t.Fatalf("partial launch: %+v %v", ref, err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("program ran before gate release")
	}
	restarted, err := New(b.socket)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := restarted.Lookup(ctx, ref.ExecutionID, ref.OperationID)
	if err != nil || observation.Alive || !strings.Contains(observation.Detail, "Startup incomplete") {
		t.Fatalf("partial lookup: %+v %v", observation, err)
	}
	if _, err := restarted.Launch(ctx, spec); err == nil {
		t.Fatal("partial launch duplicated")
	}
	out, err := b.command(ctx, "list-panes", "-a", "-F", "#{pane_id}")
	if err != nil || len(strings.Fields(string(out))) != 1 {
		t.Fatalf("duplicate partial panes: %q %v", out, err)
	}
	if err := restarted.Stop(ctx, ref); err != nil {
		t.Fatal("explicit partial stop:", err)
	}
}

func TestRuntimeLossIsMissingAndNeverRelaunches(t *testing.T) {
	b := backendForTest(t)
	spec, _ := fixtureSpec(t, b)
	ctx := context.Background()
	ref, err := b.Launch(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.runCommand(ctx, "kill-server"); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(b.socket)
	if err != nil {
		t.Fatal(err)
	}
	o, err := restarted.Inspect(ctx, ref)
	if err != nil || o.Alive || !strings.Contains(o.Detail, "missing") {
		t.Fatalf("runtime loss: %+v %v", o, err)
	}
	if _, err := restarted.Launch(ctx, spec); err == nil {
		t.Fatal("runtime loss replayed native execution")
	}
	if _, err := restarted.AttachCommand(ref, false); err == nil {
		t.Fatal("missing runtime attach returned command")
	}
	if _, err := b.runCommand(ctx, "list-sessions"); err == nil {
		t.Fatal("observation recreated tmux server")
	}
}

func attachControlClient(t *testing.T, b *Backend, ref domain.RuntimeRef, readOnly bool) io.WriteCloser {
	t.Helper()
	argv, err := b.AttachCommand(ref, readOnly)
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"-C"}, argv[1:]...)
	cmd := exec.Command(argv[0], args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return stdin
}

func awaitWriters(t *testing.T, b *Backend, ref domain.RuntimeRef, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last int
	for time.Now().Before(deadline) {
		o, err := b.Inspect(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		last = o.WritableClients
		if last == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("writable clients = %d, want %d", last, want)
}

func TestAttachArgumentsAndAuthoritativeWritableClients(t *testing.T) {
	b := backendForTest(t)
	spec, _ := fixtureSpec(t, b)
	ref, err := b.Launch(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, readOnly := range []bool{false, true} {
		argv, err := b.AttachCommand(ref, readOnly)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{b.binary, "-N", "-S", b.socket, "attach-session", "-E"}
		if readOnly {
			want = append(want, "-r")
		}
		want = append(want, "-t", ref.Session)
		if !reflect.DeepEqual(argv, want) {
			t.Fatalf("attach argv: %q", argv)
		}
	}
	writable := attachControlClient(t, b, ref, false)
	awaitWriters(t, b, ref, 1)
	readOnly := attachControlClient(t, b, ref, true)
	awaitWriters(t, b, ref, 1)
	out, err := b.runCommand(context.Background(), "list-clients", "-t", ref.Session, "-F", "#{client_flags}")
	if err != nil || !strings.Contains(string(out), "read-only") || !strings.Contains(string(out), "ignore-size") {
		t.Fatalf("read-only sizing flags: %q %v", out, err)
	}
	second := attachControlClient(t, b, ref, false)
	awaitWriters(t, b, ref, 2)
	restarted, err := New(b.socket)
	if err != nil {
		t.Fatal(err)
	}
	awaitWriters(t, restarted, ref, 2)
	_, _ = io.WriteString(writable, "detach-client\n")
	awaitWriters(t, restarted, ref, 1)
	_, _ = io.WriteString(second, "detach-client\n")
	awaitWriters(t, restarted, ref, 0)
	_, _ = io.WriteString(readOnly, "detach-client\n")
	out, err = b.runCommand(context.Background(), "show-options", "-g", "-v", "prefix")
	if err != nil || strings.TrimSpace(string(out)) != "C-g" {
		t.Fatalf("native prefix: %q %v", out, err)
	}
	out, err = b.runCommand(context.Background(), "list-keys", "-T", "prefix")
	if err != nil || !strings.Contains(string(out), "d   detach-client") || !strings.Contains(string(out), "C-g send-prefix") || len(strings.Split(strings.TrimSpace(string(out)), "\n")) != 2 {
		t.Fatalf("owned keys: %q %v", out, err)
	}
}

func TestNativeTerminalDetachAndLiteralKeys(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native script client unsupported platform")
	}
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script unavailable; terminal key test not run")
	}
	b := backendForTest(t)
	spec, output := fixtureSpec(t, b)
	spec.Env = append(spec.Env, "AB_RAW_KEYS=1")
	ref, err := b.Launch(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	readHelper(t, output)
	argv, err := b.AttachCommand(ref, false)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if runtime.GOOS == "darwin" {
		args = append([]string{"-q", "/dev/null"}, argv...)
	} else {
		quoted := make([]string, len(argv))
		for i, value := range argv {
			quoted[i] = shellQuote(value)
		}
		args = []string{"-q", "-c", strings.Join(quoted, " "), "/dev/null"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := exec.CommandContext(ctx, "script", args...)
	client.Env = append(cleanTmuxEnv(os.Environ()), "TERM=xterm-256color")
	input, err := client.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	client.Stdout = io.Discard
	client.Stderr = io.Discard
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = client.Process.Kill(); _ = client.Wait() })
	awaitWriters(t, b, ref, 1)
	// Native terminal bytes, not tmux send-keys. The doubled reserved prefix
	// yields one literal Ctrl-G. Ctrl-C and Escape remain native pane input.
	wire := []byte("Unicode 🛠\x03\x1b")
	if _, err := input.Write(wire); err != nil {
		t.Fatal(err)
	}
	awaitBytes := func(want []byte) {
		deadline := time.Now().Add(3 * time.Second)
		var got []byte
		for time.Now().Before(deadline) {
			got, _ = os.ReadFile(output + ".keys")
			if bytes.Equal(got, want) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("native bytes: %q, want %q", got, want)
	}
	// Wait for Escape's native ambiguity timeout before typing the prefix;
	// adjacent Escape+Ctrl-G is an Alt chord rather than a prefix sequence.
	awaitBytes(wire)
	if _, err := input.Write([]byte{7, 7}); err != nil {
		t.Fatal(err)
	}
	awaitBytes(append(wire, 7))
	if _, err := input.Write([]byte{7, 'd'}); err != nil {
		t.Fatal(err)
	}
	awaitWriters(t, b, ref, 0)
	// BSD script continues waiting for its piped input even after its terminal
	// child exits. Stop that test-only wrapper after tmux confirms native detach;
	// full TUI return is a separate integrated acceptance gate.
	_ = input.Close()
	cancel()
	_ = client.Wait()
	o, err := b.Inspect(context.Background(), ref)
	if err != nil || !o.Alive {
		t.Fatalf("detach killed native process: %+v %v", o, err)
	}
	argv, err = b.AttachCommand(ref, false)
	if err != nil || len(argv) == 0 {
		t.Fatal("reconnect unavailable", err)
	}
}

func TestPrivateSocketAndReservedEnvironment(t *testing.T) {
	for _, path := range []string{"", "default", "/tmp/../tmp/socket", "/tmp/socket\n"} {
		if _, err := New(path); err == nil {
			t.Fatalf("unsafe socket %q accepted", path)
		}
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(filepath.Join(dir, "socket")); err == nil {
		t.Fatal("public socket directory accepted")
	}
	b := backendForTest(t)
	spec, _ := fixtureSpec(t, b)
	for _, env := range []string{"BP_EXECUTION_ID=forged", "BP_LINK_TOKEN=forged", "TERM=dumb", "TMUX=forged", "bad-key=value", "missing-equals"} {
		changed := spec
		changed.Env = append(append([]string{}, spec.Env...), env)
		if _, err := b.Launch(context.Background(), changed); err == nil {
			t.Fatalf("reserved env %q accepted", env)
		}
	}
}

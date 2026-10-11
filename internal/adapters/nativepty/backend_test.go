package nativepty

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"golang.org/x/sys/unix"
)

var helperPath string

func TestMain(m *testing.M) {
	if runStdioTestMode() {
		return
	}
	if os.Getenv("KEELMUX_TEST_PTY_LIFECYCLE_CHILD") == "1" {
		os.Exit(runPTYLifecycleFixture())
	}
	if os.Getenv("KEELMUX_TEST_CONTROLLER") == "1" {
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "km-helper-")
	if err != nil {
		panic(err)
	}
	helperPath = filepath.Join(dir, "keelmux-supervisor")
	args := []string{"build", "-o", helperPath}
	if helperRace {
		args = append(args, "-race")
	}
	args = append(args, "../../../cmd/keelmux-supervisor")
	cmd := exec.Command("go", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "helper build: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
func testBackend(t *testing.T, history int) *Backend {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "km-n-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := NewWithOptions(dir, Options{HelperPath: helperPath, HistoryBytes: history})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func testSpec(script string) domain.LaunchSpec {
	s := domain.NewSession(domain.NewID("host"), "test", "/tmp", "shell", "test")
	return domain.LaunchSpec{Session: s, OperationID: domain.NewID("launch"), Argv: []string{"/bin/sh", "-c", script}}
}
func launchTest(t *testing.T, b *Backend, spec domain.LaunchSpec, process bool) domain.RuntimeRef {
	t.Helper()
	var ref domain.RuntimeRef
	var err error
	if process {
		ref, err = b.LaunchProcess(context.Background(), spec)
	} else {
		ref, err = b.Launch(context.Background(), spec)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = b.Stop(ctx, ref)
	})
	return ref
}
func readUntil(t *testing.T, s *Stream, want string) string {
	t.Helper()
	_ = s.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer s.conn.SetReadDeadline(time.Time{})
	var buf strings.Builder
	for !strings.Contains(buf.String(), want) {
		o, err := s.ReadOutput()
		if err != nil {
			t.Fatalf("read %q: %v; output %q", want, err, buf.String())
		}
		if o.Gap || o.Exited {
			t.Fatalf("unexpected output status: %+v", o)
		}
		buf.Write(o.Data)
	}
	return buf.String()
}

func TestHeadlessPTYTerminalEnvironment(t *testing.T) {
	for _, inherited := range []string{"", "dumb", "screen-256color"} {
		t.Run(inherited, func(t *testing.T) {
			t.Setenv("TERM", inherited)
			b := testBackend(t, 0)
			spec := testSpec(`printf 'TERMINAL=%s\n' "$TERM"; read value`)
			ref := launchTest(t, b, spec, false)
			stream, err := b.Open(context.Background(), ref, false, 24, 80)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			want := inherited
			if want == "" || want == "dumb" {
				want = "xterm-256color"
			}
			if !strings.Contains(string(stream.Snapshot), "TERMINAL="+want) {
				readUntil(t, stream, "TERMINAL="+want)
			}
		})
	}
	t.Setenv("TERM", "dumb")
	b := testBackend(t, 0)
	spec := testSpec("true")
	if !slicesContains(b.managedEnv(spec, "token", "process"), "TERM=dumb") {
		t.Fatal("foreground service terminal environment was changed")
	}
	spec.Env = []string{"TERM=dumb"}
	if !slicesContains(b.managedEnv(spec, "token", "pty"), "TERM=dumb") {
		t.Fatal("explicit terminal environment was overridden")
	}
}

func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func openWriter(t *testing.T, b *Backend, ref domain.RuntimeRef) *Stream {
	t.Helper()
	s, err := b.Open(context.Background(), ref, false, 24, 80)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func waitNoWriter(t *testing.T, b *Backend, ref domain.RuntimeRef) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		obs, err := b.Inspect(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		if obs.WritableClients == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("writer was not released after disconnect")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPTYLifecycleUnicodeResizeInterruptReconnect(t *testing.T) {
	testPTYLifecycle(t, true)
}
func TestWriterReadOnlyAndExactInstance(t *testing.T) {
	b := testBackend(t, 0)
	spec := testSpec(`stty -echo; while IFS= read -r line; do printf 'ECHO:%s\n' "$line"; done`)
	ref := launchTest(t, b, spec, false)
	s := openWriter(t, b, ref)
	if _, err := b.Open(context.Background(), ref, false, 24, 80); err == nil {
		t.Fatal("second writer admitted")
	}
	ro, err := b.Open(context.Background(), ref, true, 200, 200)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if err = ro.WriteInput([]byte("bad\n")); err == nil {
		t.Fatal("read only wrote")
	}
	if err = ro.Resize(99, 99); err == nil {
		t.Fatal("read only resized")
	}
	if attached, err := ClientAttached(context.Background(), ref, os.Getpid(), false); err != nil || !attached {
		t.Fatalf("peer PID readiness %v %v", attached, err)
	}
	bad := ref
	bad.Session = "foreign"
	if _, err = b.Inspect(context.Background(), bad); err == nil {
		t.Fatal("changed instance inspected")
	}
	if err = b.Stop(context.Background(), bad); err == nil {
		t.Fatal("changed instance stopped")
	}
	s.Close()
	waitNoWriter(t, b, ref)
	s2 := openWriter(t, b, ref)
	if err = s2.WriteInput([]byte("live\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, s2, "ECHO:live")
	readUntil(t, ro, "ECHO:live")
}
func TestNoReplayChangedContentAndExit(t *testing.T) {
	b := testBackend(t, 0)
	marker := filepath.Join(b.state, "effects")
	spec := testSpec(`printf x >> "$1"; while :; do sleep 1; done`)
	spec.Argv = append(spec.Argv, "test", marker)
	ref := launchTest(t, b, spec, true)
	again, err := b.LaunchProcess(context.Background(), spec)
	if err != nil || again != ref {
		t.Fatalf("lost ack reconciliation %v %v", again, err)
	}
	changed := spec
	changed.Argv = append([]string{}, spec.Argv...)
	changed.Argv[2] = "exit 0"
	if _, err = b.LaunchProcess(context.Background(), changed); err == nil {
		t.Fatal("changed content replayed")
	}
	if err = b.Stop(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err = b.LaunchProcess(context.Background(), spec); err == nil {
		t.Fatal("stopped execution replayed")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "x" {
		t.Fatalf("external effects %q %v", data, err)
	}
	if _, err = b.AttachCommand(ref, false); err == nil {
		t.Fatal("service attached as terminal")
	}
}
func TestUnknownIntentNeverReplayed(t *testing.T) {
	b := testBackend(t, 0)
	spec := testSpec("sleep 30")
	boot, err := bootID()
	if err != nil {
		t.Fatal(err)
	}
	r := receipt{Version: 1, SessionID: spec.Session.ID, HostID: spec.Session.HostID, Digest: launchDigest(spec, "pty"), Auth: nonce(), LinkToken: nonce(), Mode: "pty", Stage: "intent", Ref: domain.RuntimeRef{Backend: "native", Socket: b.socket(spec.Session.Execution.ID), Session: nonce(), BootID: boot, ExecutionID: spec.Session.Execution.ID, OperationID: spec.OperationID}}
	if err = b.writeReceipt(r); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Launch(context.Background(), spec); err == nil {
		t.Fatal("unknown intent replayed")
	}
	o, err := b.Lookup(context.Background(), r.Ref.ExecutionID, r.Ref.OperationID)
	if !errors.Is(err, ErrUnavailable) || o.Alive || !strings.Contains(o.Detail, "uncertain") {
		t.Fatalf("unknown evidence %+v %v", o, err)
	}
}
func TestLinkAndPrivateReceipts(t *testing.T) {
	b := testBackend(t, 0)
	spec := testSpec("sleep 30")
	ref := launchTest(t, b, spec, true)
	r, err := b.readReceipt(ref.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.ValidateLink(context.Background(), spec.Session.ID, ref.ExecutionID, ref.OperationID, r.LinkToken); err != nil {
		t.Fatal(err)
	}
	if err = b.ValidateLink(context.Background(), spec.Session.ID, ref.ExecutionID, ref.OperationID, nonce()); err == nil {
		t.Fatal("foreign token linked")
	}
	if err = os.Chmod(b.receiptPath(ref.ExecutionID), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Inspect(context.Background(), ref); err == nil {
		t.Fatal("public receipt trusted")
	}
	_ = os.Chmod(b.receiptPath(ref.ExecutionID), 0600)
}
func TestForeignSocketAndSupervisorIdentityRejected(t *testing.T) {
	b := testBackend(t, 0)
	spec := testSpec("sleep 30")
	ref := launchTest(t, b, spec, true)
	r, err := b.readReceipt(ref.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	bad := r
	bad.SupervisorStart = "wrong-start"
	if _, err = b.connect(context.Background(), bad); err == nil {
		t.Fatal("reused PID accepted")
	}
	foreign := filepath.Join(b.state, "foreign.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: foreign, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_ = os.Chmod(foreign, 0600)
	bad = r
	bad.Ref.Socket = foreign
	if _, err = b.connect(context.Background(), bad); err == nil {
		t.Fatal("foreign socket peer accepted")
	}
}
func TestMalformedFramesAndRingBound(t *testing.T) {
	var value request
	if err := readMessage(strings.NewReader("\xff\xff\xff\xff"), &value); err == nil {
		t.Fatal("unbounded frame accepted")
	}
	r := outputRing{limit: 4096}
	for range 100 {
		r.append([]byte(strings.Repeat("x", 1024)))
	}
	data, start, end, gap := r.read(0)
	if !gap || len(data) != 4096 || start != 98304 || end != 102400 || len(r.data) > 4096 {
		t.Fatalf("ring gap %d %d %d %v", len(data), start, end, gap)
	}
}
func TestConcurrentLaunchSingleEffect(t *testing.T) {
	b := testBackend(t, 0)
	spec := testSpec("sleep 30")
	var wg sync.WaitGroup
	refs := make(chan domain.RuntimeRef, 4)
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref, err := b.LaunchProcess(context.Background(), spec)
			refs <- ref
			errs <- err
		}()
	}
	wg.Wait()
	close(refs)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first domain.RuntimeRef
	for ref := range refs {
		if first.ExecutionID == "" {
			first = ref
		}
		if first != ref {
			t.Fatal("duplicate runtime")
		}
	}
	if err := b.Stop(context.Background(), first); err != nil {
		t.Fatal(err)
	}
}

// A separate process represents the control daemon and is killed by the parent
// after its launch acknowledgement. The owned helper must survive its death.
func TestSubprocessController(t *testing.T) {
	if os.Getenv("KEELMUX_TEST_CONTROLLER") != "1" {
		return
	}
	b, err := NewWithOptions(os.Getenv("KEELMUX_TEST_STATE"), Options{HelperPath: os.Getenv("KEELMUX_TEST_HELPER")})
	if err != nil {
		panic(err)
	}
	var spec domain.LaunchSpec
	if err = json.NewDecoder(os.Stdin).Decode(&spec); err != nil {
		panic(err)
	}
	ref, err := b.Launch(context.Background(), spec)
	if err != nil {
		panic(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(ref)
	select {}
}
func TestControlProcessDeathRecovery(t *testing.T) {
	b := testBackend(t, 0)
	spec := testSpec(`stty -echo; while IFS= read -r line; do printf 'SURVIVED:%s\n' "$line"; done`)
	data, _ := json.Marshal(spec)
	cmd := exec.Command(os.Args[0], "-test.run=^TestSubprocessController$")
	cmd.Env = append(os.Environ(), "KEELMUX_TEST_CONTROLLER=1", "KEELMUX_TEST_STATE="+b.state, "KEELMUX_TEST_HELPER="+helperPath)
	cmd.Stdin = strings.NewReader(string(data))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	line, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var ref domain.RuntimeRef
	if err = json.Unmarshal(line, &ref); err != nil {
		t.Fatalf("controller result %q %v", line, err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background(), ref) })
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	fresh, err := NewWithOptions(b.state, Options{HelperPath: helperPath})
	if err != nil {
		t.Fatal(err)
	}
	o, err := fresh.Lookup(context.Background(), ref.ExecutionID, ref.OperationID)
	if err != nil || !o.Alive || o.Ref != ref {
		t.Fatalf("control restart %+v %v", o, err)
	}
	stream := openWriter(t, fresh, ref)
	if err = stream.WriteInput([]byte("after-controller-kill\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, stream, "SURVIVED:after-controller-kill")
}

func TestProcessPrivateUmask(t *testing.T) {
	b := testBackend(t, 0)
	path := filepath.Join(b.state, "child-file")
	spec := testSpec(`: > "$1"; exec sleep 30`)
	spec.Argv = append(spec.Argv, "test", path)
	ref := launchTest(t, b, spec, true)
	_ = ref
	deadline := time.Now().Add(time.Second)
	for {
		info, err := os.Stat(path)
		if err == nil {
			if info.Mode().Perm() != 0600 {
				t.Fatalf("child mode %o", info.Mode().Perm())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSupervisorResourceObservation(t *testing.T) {
	b := testBackend(t, 0)
	for _, mode := range []string{"pty", "process"} {
		spec := testSpec("exec sleep 30")
		ref := launchTest(t, b, spec, mode == "process")
		r, err := b.readReceipt(ref.ExecutionID)
		if err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("/bin/ps", "-p", fmt.Sprint(r.SupervisorPID), "-o", "rss=").Output()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s supervisor RSS=%s KiB, history cap=%d bytes, screen cap=65536 cells", mode, strings.TrimSpace(string(out)), b.history)
	}
}

func TestMissingSupervisorIsUncertainAndNeverReplayed(t *testing.T) {
	b := testBackend(t, 0)
	spec := testSpec("exec sleep 30")
	ref := launchTest(t, b, spec, false)
	r, err := b.readReceipt(ref.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.connect(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	p, err := os.FindProcess(r.SupervisorPID)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err = b.Inspect(context.Background(), ref)
		if errors.Is(err, ErrUnavailable) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing helper did not become uncertain: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = b.Launch(context.Background(), spec); err == nil {
		t.Fatal("lost supervisor replayed")
	}
	if err = b.Stop(context.Background(), ref); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unverified stop did not fail closed: %v", err)
	}
}
func TestPrivateStateRejectsSymlinkAndPublicDirectory(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(link); err == nil {
		t.Fatal("symlink state accepted")
	}
	if err := os.Chmod(private, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(private); err == nil {
		t.Fatal("public state accepted")
	}
}

func TestPrivateStateSpecialFilesFailWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, dir, link} {
		done := make(chan error, 1)
		go func() { _, err := readPrivate(path); done <- err }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("special file accepted: %s", path)
			}
		case <-time.After(time.Second):
			t.Fatalf("special file blocked: %s", path)
		}
	}
}

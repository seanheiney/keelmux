package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"github.com/seanheiney/keelmux/internal/adapters/nativepty"
	"github.com/seanheiney/keelmux/internal/adapters/tmux"
	"github.com/seanheiney/keelmux/internal/daemon"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	bpruntime "github.com/seanheiney/keelmux/internal/runtime"
	"github.com/seanheiney/keelmux/internal/store"
	"golang.org/x/sys/unix"
)

// This owned child stands in for a native raw terminal program. It performs no
// provider login or model activity, and records actual pane bytes and size.
func TestTransportNativeHelper(t *testing.T) {
	args := os.Args
	for len(args) > 0 && args[0] != "--bp-transport-helper" {
		args = args[1:]
	}
	if len(args) != 2 {
		return
	}
	path := args[1]
	fd, _, err := descriptor(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	if _, err := term.MakeRaw(uintptr(fd)); err != nil {
		os.Exit(3)
	}
	resize := func() {
		ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
		if err == nil {
			data, _ := json.Marshal([]uint16{ws.Row, ws.Col})
			_ = os.WriteFile(path+".size", data, 0600)
		}
	}
	resize()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)
	go func() {
		for range signals {
			resize()
		}
	}()
	file, err := os.OpenFile(path+".keys", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(4)
	}
	_, _ = os.Stdout.Write([]byte("NATIVE READY 🛠\r\n"))
	buffer := make([]byte, 8192)
	for {
		n, err := os.Stdin.Read(buffer)
		if n > 0 {
			_, _ = file.Write(buffer[:n])
			_, _ = os.Stdout.Write(buffer[:n])
		}
		if err != nil {
			os.Exit(0)
		}
	}
}

func awaitNative(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned native fixture did not reach expected state")
}

func TestDriveNativeDetachReturnsCallerAndReleasesHost(t *testing.T) {
	for _, kind := range []string{"native", "tmux"} {
		t.Run(kind, func(t *testing.T) { driveNativeDetach(t, kind) })
	}
}
func driveNativeDetach(t *testing.T, kind string) {
	if _, err := exec.LookPath("tmux"); kind == "tmux" && err != nil {
		t.Skip("tmux unavailable; actual native detach proof not run")
	}
	dir, err := os.MkdirTemp("/tmp", "bp-drive-native-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "tmux.sock")
	defer os.RemoveAll(dir)
	if kind == "tmux" {
		defer exec.Command("tmux", "-S", socket, "kill-server").Run()
	}
	state, err := store.Open(filepath.Join(dir, "registry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	var backend bpruntime.Backend
	if kind == "tmux" {
		backend, err = tmux.New(socket)
	} else {
		helper := filepath.Join(dir, "keelmux-supervisor")
		build := exec.Command("go", "build", "-o", helper, "./cmd/keelmux-supervisor")
		build.Dir = "../.."
		if out, e := build.CombinedOutput(); e != nil {
			t.Fatalf("native helper build: %v %s", e, out)
		}
		backend, err = nativepty.NewWithOptions(filepath.Join(dir, "native"), nativepty.Options{HelperPath: helper})
	}
	if err != nil {
		t.Fatal(err)
	}
	hostID := domain.NewID("host")
	authority, err := daemon.New(state, backend, hostID)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := NewHost(authority, state, hostID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		awaitNative(t, func() bool { relay.mu.Lock(); defer relay.mu.Unlock(); return len(relay.active) == 0 })
	}()
	listener, err := net.Listen("unix", filepath.Join(dir, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/terminal" {
			relay.Handler().ServeHTTP(w, r)
		} else {
			authority.Handler().ServeHTTP(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	client := NewClient(filepath.Join(dir, "control.sock"))
	defer client.HTTP.CloseIdleConnections()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "capture")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	launch, err := client.Launch(ctx, protocol.LaunchRequest{OperationID: domain.NewID("launch"), Name: "native I/O fixture", Workspace: dir, Provider: "fixture", ProviderVersion: "test", Argv: []string{binary, "-test.run=^TestTransportNativeHelper$", "--", "--bp-transport-helper", capture}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ref := launch.Session.Execution.Runtime
		if ref.ExecutionID == "" {
			if o, e := backend.Lookup(context.Background(), launch.Session.Execution.ID, launch.Operation.ID); e == nil {
				ref = o.Ref
			}
		}
		if err := backend.Stop(context.Background(), ref); err != nil {
			t.Error("owned child cleanup", err)
		}
	}()
	awaitNative(t, func() bool { _, err := os.Stat(capture + ".size"); return err == nil })
	var expected []byte
	for i := 0; i < 3; i++ {
		master, input, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer master.Close()
		defer input.Close()
		fd, flags, err := descriptor(input)
		if err != nil {
			t.Fatal(err)
		}
		original, err := term.GetState(uintptr(fd))
		if err != nil {
			t.Fatal(err)
		}
		output, err := os.CreateTemp(dir, "display")
		if err != nil {
			t.Fatal(err)
		}
		defer output.Close()
		request := Request{HostID: hostID, Lease: protocol.LeaseRequest{SessionID: launch.Session.ID, ExecutionID: launch.Session.Execution.ID, Generation: launch.Session.Execution.Generation, OperationID: domain.NewID("attach"), ClientID: "owned-caller"}, Rows: 30, Cols: 100, Term: "xterm-256color"}
		stream, err := client.Open(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		ended := make(chan error, 1)
		go func() { ended <- Drive(ctx, stream, input, output) }()
		awaitNative(t, func() bool {
			now, err := term.GetState(uintptr(fd))
			return err == nil && !reflect.DeepEqual(now, original)
		})
		if err := stream.Resize(43, 123); err != nil {
			t.Fatal(err)
		}
		awaitNative(t, func() bool { b, _ := os.ReadFile(capture + ".size"); return string(b) == "[43,123]" })
		keys := []byte("Unicode 🛠\x03")
		if _, err := master.Write(keys); err != nil {
			t.Fatal(err)
		}
		expected = append(expected, keys...)
		awaitNative(t, func() bool { b, _ := os.ReadFile(capture + ".keys"); return bytes.Equal(b, expected) })
		// Allow tmux's native escape-time to resolve Escape before sending a
		// prefix, otherwise Escape+Ctrl-G is a native Alt-modified key.
		if _, err := master.Write([]byte{27}); err != nil {
			t.Fatal(err)
		}
		expected = append(expected, 27)
		awaitNative(t, func() bool { b, _ := os.ReadFile(capture + ".keys"); return bytes.Equal(b, expected) })
		if _, err := master.Write([]byte{7, 7}); err != nil {
			t.Fatal(err)
		}
		// tmux consumes the first prefix, and emits the second literally.
		expected = append(expected, 7)
		awaitNative(t, func() bool { b, _ := os.ReadFile(capture + ".keys"); return bytes.Equal(b, expected) })
		if _, err := master.Write([]byte{7, 'd'}); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-ended:
			if err != nil {
				t.Fatal("native detach did not return cleanly", err)
			}
		case <-time.After(time.Second):
			data, _ := os.ReadFile(capture + ".keys")
			display, _ := os.ReadFile(output.Name())
			clients, _ := exec.Command("tmux", "-N", "-S", socket, "list-clients", "-F", "#{client_pid}|#{client_session}").Output()
			t.Logf("owned pane keys=%q displayTail=%q clients=%q", data, display[max(0, len(display)-300):], clients)
			cancel()
			<-ended
			t.Fatal("native Ctrl-G d did not return the caller")
		}
		restored, err := term.GetState(uintptr(fd))
		if err != nil || !reflect.DeepEqual(restored, original) {
			t.Fatal("caller terminal was not restored after native detach", err)
		}
		_, restoredFlags, err := descriptor(input)
		if err != nil || restoredFlags != flags {
			t.Fatal("caller descriptor flags were not restored", err)
		}
		awaitNative(t, func() bool {
			grant, err := state.Grant(ctx, request.Lease.OperationID)
			return err == nil && !grant.Active
		})
		observation, err := backend.Inspect(ctx, launch.Session.Execution.Runtime)
		if err != nil || !observation.Alive || observation.WritableClients != 0 {
			t.Fatalf("detach lost agent or left a writer: %+v %v", observation, err)
		}
		select {
		case err := <-relay.Failures:
			t.Fatal("native cleanup failed", err)
		default:
		}
		// A caller TUI may resume only after Drive's reader has stopped.
		if err := unix.SetNonblock(fd, true); err != nil {
			t.Fatal(err)
		}
		if _, err := master.Write([]byte("caller resumes\n")); err != nil {
			t.Fatal(err)
		}
		probe, stop := context.WithTimeout(ctx, time.Second)
		buffer := make([]byte, 128)
		n, err := readTerminal(probe, fd, buffer)
		stop()
		if err != nil || string(buffer[:n]) != "caller resumes\n" {
			t.Fatal("Drive stole caller input after detach", err)
		}
		data, _ := os.ReadFile(capture + ".keys")
		if !bytes.Equal(data, expected) {
			t.Fatalf("native detach keys reached pane: %q", data)
		}
	}
}

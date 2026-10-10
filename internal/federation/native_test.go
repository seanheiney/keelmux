package federation

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
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/seanheiney/agent-backplane/internal/adapters/tmux"
	"github.com/seanheiney/agent-backplane/internal/daemon"
	"github.com/seanheiney/agent-backplane/internal/domain"
	"github.com/seanheiney/agent-backplane/internal/protocol"
	"github.com/seanheiney/agent-backplane/internal/store"
	"github.com/seanheiney/agent-backplane/internal/transport"
)

func TestNativePeerHelper(t *testing.T) {
	args := os.Args
	for len(args) > 0 && args[0] != "--bp-peer-helper" {
		args = args[1:]
	}
	if len(args) != 2 {
		return
	}
	path := args[1]
	cmd := exec.Command("stty", "raw", "-echo")
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		os.Exit(2)
	}
	resize := func() {
		rows, cols, err := pty.Getsize(os.Stdin)
		if err == nil {
			data, _ := json.Marshal([]int{rows, cols})
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
		os.Exit(3)
	}
	defer file.Close()
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

type nativePeer struct {
	dir       string
	host      domain.ID
	state     *store.Store
	backend   *tmux.Backend
	authority *daemon.Service
	relay     *transport.Host
	client    *transport.Client
	server    *http.Server
	listener  net.Listener
	session   domain.Session
	capture   string
}

func servePrivate(t *testing.T, path string, handler http.Handler) (*http.Server, net.Listener) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close() })
	return server, listener
}

func newNativePeer(t *testing.T) *nativePeer {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable; native peer fixture not run")
	}
	dir, err := os.MkdirTemp("/tmp", "bp-peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", filepath.Join(dir, "tmux.sock"), "kill-server").Run()
		_ = os.RemoveAll(dir)
	})
	st, err := store.Open(filepath.Join(dir, "registry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	backend, err := tmux.New(filepath.Join(dir, "tmux.sock"))
	if err != nil {
		t.Fatal(err)
	}
	host := domain.NewID("host")
	authority, err := daemon.New(st, backend, host)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := transport.NewHost(authority, st, host)
	if err != nil {
		t.Fatal(err)
	}
	p := &nativePeer{dir: dir, host: host, state: st, backend: backend, authority: authority, relay: relay, capture: filepath.Join(dir, "capture")}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/terminal" {
			relay.Handler().ServeHTTP(w, r)
		} else {
			authority.Handler().ServeHTTP(w, r)
		}
	})
	p.server, p.listener = servePrivate(t, filepath.Join(dir, "control.sock"), handler)
	p.client = transport.NewClient(filepath.Join(dir, "control.sock"))
	t.Cleanup(p.client.HTTP.CloseIdleConnections)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := p.client.Launch(ctx, protocol.LaunchRequest{OperationID: domain.NewID("launch"), Name: "native fixture", Workspace: dir, Provider: "fixture", ProviderVersion: "test", Argv: []string{binary, "-test.run=^TestNativePeerHelper$", "--", "--bp-peer-helper", p.capture}})
	if err != nil {
		t.Fatal(err)
	}
	p.session = result.Session
	awaitFile(t, p.capture+".size", func(data []byte) bool { return len(data) > 0 })
	return p
}

func awaitFile(t *testing.T, path string, predicate func([]byte) bool) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []byte
	for time.Now().Before(deadline) {
		last, _ = os.ReadFile(path)
		if predicate(last) {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture %s did not reach expected state: %q", filepath.Base(path), last)
	return nil
}

func requestFor(p *nativePeer, readOnly bool) transport.Request {
	s := p.session
	return transport.Request{HostID: p.host, Lease: protocol.LeaseRequest{SessionID: s.ID, ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, OperationID: domain.NewID("attach"), ClientID: string(domain.NewID("client")), ReadOnly: readOnly}, Rows: 30, Cols: 100, Term: "xterm-256color"}
}

func awaitReleased(t *testing.T, p *nativePeer, request transport.Request) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		g, err := p.state.Grant(context.Background(), request.Lease.OperationID)
		if err == nil && !g.Active {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("home attachment grant remains active after its native client should detach")
}

func assertAlive(t *testing.T, p *nativePeer) {
	t.Helper()
	observation, err := p.backend.Inspect(context.Background(), p.session.Execution.Runtime)
	if err != nil || !observation.Alive {
		t.Fatalf("healthy native agent was lost: %+v %v", observation, err)
	}
	select {
	case err := <-p.relay.Failures:
		t.Fatal("host cleanup:", err)
	default:
	}
}

func TestOneEntryTwoNativeHomesAndRevoke(t *testing.T) {
	a, b := newNativePeer(t), newNativePeer(t)
	g, err := New([]Peer{{a.host, a.client}, {b.host, b.client}}, []Grant{{"human", a.host, []domain.ID{a.session.ID}}, {"human", b.host, []domain.ID{b.session.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	entryDir, err := os.MkdirTemp("/tmp", "bp-entry-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(entryDir) })
	_, _ = servePrivate(t, filepath.Join(entryDir, "entry.sock"), g.Handler("human"))
	entry := transport.NewClient(filepath.Join(entryDir, "entry.sock"))
	t.Cleanup(entry.HTTP.CloseIdleConnections)
	sessions, err := entry.List(context.Background())
	if err != nil || len(sessions) != 2 {
		t.Fatalf("one entry directory: %d %v", len(sessions), err)
	}
	for _, session := range sessions {
		if session.Execution.Runtime.Socket != "" || session.Execution.Runtime.Pane != "" {
			t.Fatal("home locator leaked")
		}
	}
	request := requestFor(b, false)
	stream, err := entry.Open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if stream.Receipt.HostID != b.host || stream.Receipt.SessionID != b.session.ID {
		t.Fatal("wrong home receipt")
	}
	var output bytes.Buffer
	var outputMu sync.Mutex
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		for {
			f, err := stream.ReadFrame()
			if err != nil {
				return
			}
			if f.Kind == transport.Output {
				outputMu.Lock()
				_, _ = output.Write(f.Data)
				outputMu.Unlock()
			}
			if f.Kind == transport.Close || f.Kind == transport.Failure {
				return
			}
		}
	}()
	if err := stream.WriteInput([]byte("Unicode 🛠\x03\x1b")); err != nil {
		t.Fatal(err)
	}
	awaitFile(t, b.capture+".keys", func(data []byte) bool { return bytes.Equal(data, []byte("Unicode 🛠\x03\x1b")) })
	// Escape and the following prefix are distinct physical key events.
	time.Sleep(600 * time.Millisecond)
	if err := stream.WriteInput([]byte{7, 7}); err != nil {
		t.Fatal(err)
	}
	awaitFile(t, b.capture+".keys", func(data []byte) bool { return bytes.Equal(data, []byte("Unicode 🛠\x03\x1b\x07")) })
	if err := stream.Resize(42, 124); err != nil {
		t.Fatal(err)
	}
	awaitFile(t, b.capture+".size", func(data []byte) bool { return string(data) == "[42,124]" })
	if _, err := os.Stat(a.capture + ".keys"); err == nil {
		data, _ := os.ReadFile(a.capture + ".keys")
		if len(data) != 0 {
			t.Fatal("input reached the other home")
		}
	}
	if err := g.SetGrant(Grant{"human", b.host, nil}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("revoke left native stream open")
	}
	awaitReleased(t, b, request)
	outputMu.Lock()
	gotOutput := output.String()
	outputMu.Unlock()
	if !strings.Contains(gotOutput, "NATIVE READY 🛠") || !strings.Contains(gotOutput, "Unicode 🛠") {
		t.Fatalf("native Unicode output did not traverse entry: %q", gotOutput)
	}
	assertAlive(t, b)
	assertAlive(t, a)
	if _, err := entry.Open(context.Background(), requestFor(b, false)); err == nil {
		t.Fatal("revoked session reopened")
	}
	if err := g.SetGrant(Grant{"human", b.host, []domain.ID{b.session.ID}}); err != nil {
		t.Fatal(err)
	}
	reconnect := requestFor(b, false)
	reconnected, err := entry.Open(context.Background(), reconnect)
	if err != nil {
		t.Fatal(err)
	}
	_ = reconnected.Close()
	awaitReleased(t, b, reconnect)
	assertAlive(t, b)
	// This is two local host authorities and actual tmux relays on one laptop,
	// not two independent execution servers or an SSH/TLS enrollment proof.
}

func TestNativeDetachReadonlyAndGeneration(t *testing.T) {
	p := newNativePeer(t)
	request := requestFor(p, false)
	stream, err := p.client.Open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	go func() {
		for {
			f, err := stream.ReadFrame()
			if err != nil || f.Kind == transport.Close || f.Kind == transport.Failure {
				return
			}
		}
	}()
	if _, err := p.client.Open(context.Background(), requestFor(p, false)); err == nil {
		t.Fatal("competing writer admitted")
	}
	if _, err := p.client.Open(context.Background(), request); err == nil {
		t.Fatal("duplicate stream operation admitted")
	}
	if err := stream.Resize(38, 120); err != nil {
		t.Fatal(err)
	}
	awaitFile(t, p.capture+".size", func(data []byte) bool { return string(data) == "[38,120]" })
	if err := stream.WriteInput([]byte{7, 'd'}); err != nil {
		t.Fatal(err)
	}
	awaitReleased(t, p, request)
	assertAlive(t, p)
	stale := requestFor(p, false)
	stale.Lease.Generation++
	if _, err := p.client.Open(context.Background(), stale); err == nil {
		t.Fatal("stale generation admitted")
	}
	wrong := requestFor(p, false)
	wrong.HostID = domain.NewID("wrong_host")
	if _, err := p.client.Open(context.Background(), wrong); err == nil {
		t.Fatal("wrong host authority admitted")
	}
	forged := requestFor(p, false)
	forged.Lease.Token = "supplied_token"
	if _, err := p.client.Open(context.Background(), forged); err == nil {
		t.Fatal("public supplied token used to acquire stream")
	}
	writer := requestFor(p, false)
	writer.Rows, writer.Cols = 38, 120
	writable, err := p.client.Open(context.Background(), writer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writable.Close() })
	go func() {
		for {
			f, err := writable.ReadFrame()
			if err != nil || f.Kind == transport.Close || f.Kind == transport.Failure {
				return
			}
		}
	}()
	awaitFile(t, p.capture+".size", func(data []byte) bool { return string(data) == "[38,120]" })
	for _, kind := range []transport.Kind{transport.Input, transport.Resize} {
		observer := requestFor(p, true)
		observer.Rows, observer.Cols = 50, 150
		observed, err := p.client.Open(context.Background(), observer)
		if err != nil {
			t.Fatal(err)
		}
		if err := observed.WriteInput([]byte("rejected")); err == nil {
			t.Fatal("read-only client API accepted input")
		}
		if err := observed.Resize(50, 150); err == nil {
			t.Fatal("read-only client API accepted resize")
		}
		data := []byte("must not reach pane")
		if kind == transport.Resize {
			data = []byte{0, 50, 0, 150}
		}
		if err := observed.WriteFrame(transport.Frame{Kind: kind, Data: data}); err != nil {
			t.Fatal(err)
		}
		for {
			f, err := observed.ReadFrame()
			if err != nil || f.Kind == transport.Close || f.Kind == transport.Failure {
				break
			}
		}
		_ = observed.Close()
		awaitReleased(t, p, observer)
	}
	data, _ := os.ReadFile(p.capture + ".keys")
	if len(data) != 0 {
		t.Fatalf("read-only or detach bytes reached pane: %q", data)
	}
	size, _ := os.ReadFile(p.capture + ".size")
	if string(size) != "[38,120]" {
		t.Fatalf("read-only client resized pane: %s", size)
	}
	_ = writable.Close()
	awaitReleased(t, p, writer)
	assertAlive(t, p)
}

func TestRoutedTUIGrantAndHomePartition(t *testing.T) {
	p := newNativePeer(t)
	g, err := New([]Peer{{p.host, p.client}}, []Grant{{"human", p.host, []domain.ID{p.session.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	g.SetAttachCommand(func(request transport.Request) []string {
		return []string{"bp", "fixture-drive", string(request.HostID), string(request.Lease.SessionID)}
	})
	request := requestFor(p, false)
	grant, err := g.Attach(context.Background(), "human", request.Lease)
	if err != nil {
		t.Fatal(err)
	}
	if len(grant.Argv) != 4 || grant.Argv[0] != "bp" || strings.Contains(strings.Join(grant.Argv, " "), p.dir) {
		t.Fatal("home native argv leaked")
	}
	stream, err := g.Open(context.Background(), "human", request)
	if err != nil {
		t.Fatal("pre-acquired grant was not reused", err)
	}
	_ = stream.Close()
	awaitReleased(t, p, request)
	release := request.Lease
	release.Token = grant.Lease.Token
	if err := g.Release(context.Background(), "human", release); err != nil {
		t.Fatal("idempotent TUI release", err)
	}
	if err := g.Release(context.Background(), "stranger", release); err == nil {
		t.Fatal("another principal released grant")
	}
	if _, err := g.Open(context.Background(), "stranger", requestFor(p, false)); err == nil {
		t.Fatal("unlinked principal acquired stream")
	}
	_ = p.server.Close()
	_ = p.listener.Close()
	p.client.HTTP.CloseIdleConnections()
	directory, err := g.Directory(context.Background(), "human")
	if err != nil || len(directory.Places) != 1 || directory.Places[0].State != "unreachable" {
		t.Fatal("partition not represented", directory, err)
	}
	if _, err := g.Open(context.Background(), "human", requestFor(p, false)); err == nil || strings.Contains(err.Error(), p.dir) {
		t.Fatal("partition opened or leaked home socket", err)
	}
	assertAlive(t, p)
}

type delayedStreamSource struct {
	*transport.Client
	opened chan struct{}
	resume chan struct{}
}

func (s *delayedStreamSource) Open(ctx context.Context, request transport.Request) (*transport.Stream, error) {
	stream, err := s.Client.Open(ctx, request)
	close(s.opened)
	<-s.resume // Simulate a home acknowledgement already in flight on revoke.
	return stream, err
}

func TestOriginalPrincipalPendingGrantAndOpenRevocation(t *testing.T) {
	p := newNativePeer(t)
	source := &delayedStreamSource{p.client, make(chan struct{}), make(chan struct{})}
	g, err := New([]Peer{{p.host, source}}, []Grant{{"human", p.host, []domain.ID{p.session.ID}}, {"other", p.host, []domain.ID{p.session.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	g.SetAttachCommand(func(transport.Request) []string { return []string{"bp", "fixture-drive"} })
	pending := requestFor(p, false)
	if _, err := g.Attach(context.Background(), "human", pending.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Attach(context.Background(), "other", pending.Lease); err == nil {
		t.Fatal("public acquisition IDs transferred the original principal")
	}
	if _, err := g.Open(context.Background(), "other", pending); err == nil {
		t.Fatal("another granted principal opened the pending acquisition")
	}
	if err := g.SetGrant(Grant{"human", p.host, nil}); err != nil {
		t.Fatal(err)
	}
	awaitReleased(t, p, pending)
	assertAlive(t, p)
	if err := g.SetGrant(Grant{"human", p.host, []domain.ID{p.session.ID}}); err != nil {
		t.Fatal(err)
	}
	request := requestFor(p, false)
	result := make(chan error, 1)
	go func() {
		stream, err := g.Open(context.Background(), "human", request)
		if stream != nil {
			_ = stream.Close()
		}
		result <- err
	}()
	select {
	case <-source.opened:
	case <-time.After(5 * time.Second):
		t.Fatal("home open acknowledgement did not arrive")
	}
	if err := g.SetGrant(Grant{"human", p.host, nil}); err != nil {
		t.Fatal(err)
	}
	close(source.resume)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("revoked in-flight stream returned as usable")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoked in-flight stream did not end")
	}
	awaitReleased(t, p, request)
	assertAlive(t, p)
	select {
	case err := <-g.Failures:
		t.Fatal("gateway revoke cleanup:", err)
	default:
	}
}

func TestEntryReceiptRestartExactReleaseAfterRevocation(t *testing.T) {
	p := newNativePeer(t)
	peers := []Peer{{p.host, p.client}}
	grants := []Grant{{"human", p.host, []domain.ID{p.session.ID}}, {"other", p.host, []domain.ID{p.session.ID}}}
	g, err := New(peers, grants)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "entry-state")
	if err := g.SetStateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	g.SetAttachCommand(func(transport.Request) []string { return []string{"bp", "fixture-drive"} })
	request := requestFor(p, false)
	ack, err := g.Attach(context.Background(), "human", request.Lease)
	if err != nil {
		t.Fatal(err)
	}
	request.Lease.Token = ack.Lease.Token
	contender, _ := New(peers, grants)
	if err := contender.SetStateDirectory(dir); err == nil {
		_ = contender.Close()
		t.Fatal("two entry authorities shared private receipt state")
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	// Reconstruct a fresh entry with navigation permission revoked. Its private
	// prior principal receipt still authorizes only the exact saved release.
	restarted, err := New(peers, []Grant{{"other", p.host, []domain.ID{p.session.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.SetStateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Release(context.Background(), "other", request.Lease); err == nil {
		t.Fatal("another granted principal recovered the original principal's receipt")
	}
	forged := request.Lease
	forged.Token += "forged"
	if err := restarted.Release(context.Background(), "human", forged); err == nil {
		t.Fatal("forged saved release accepted")
	}
	for i := 0; i < 2; i++ {
		if err := restarted.Release(context.Background(), "human", request.Lease); err != nil {
			t.Fatal("saved exact release was not recoverable/idempotent", err)
		}
	}
	awaitReleased(t, p, request)
	if _, err := restarted.Open(context.Background(), "human", requestFor(p, false)); err == nil {
		t.Fatal("release receipt granted fresh navigation/stream permission")
	}
	assertAlive(t, p)
}

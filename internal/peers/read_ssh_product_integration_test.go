package peers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/peerbridge"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/transport"
)

const productReadOutput = "\x1b[36mKEELMUX_READ_CHILD café 世界 🦭\x1b[0m\r\n"

// This explicit opt-in proves the actual product entry, home daemon, supervisor,
// native PTY child and scoped exporter over one owned loopback SSH server. It
// does not qualify physical second-host deployment or any native model provider.
func TestOwnedLoopbackSSHProductReadBridge(t *testing.T) {
	if os.Getenv("KEELMUX_PEER_PRODUCT_READ_SSH") != "1" {
		t.Skip("set KEELMUX_PEER_PRODUCT_READ_SSH=1 with exact clean-source product binaries")
	}
	binDir := os.Getenv("KEELMUX_PEER_PRODUCT_BIN_DIR")
	source := os.Getenv("KEELMUX_PEER_PRODUCT_SOURCE_SHA")
	if !filepath.IsAbs(binDir) || filepath.Clean(binDir) != binDir || len(source) != 40 || strings.ContainsFunc(source, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) {
		t.Fatal("read product fixture requires an absolute binary directory and exact lowercase source SHA")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal("source root unavailable")
	}
	productReadCleanSource(t, root, source)
	for _, name := range []string{"keelmux", "keelmuxd", "keelmux-supervisor", "keelmux-entry"} {
		productReadBinary(t, filepath.Join(binDir, name), name, source)
	}
	if _, err := os.Stat("/usr/sbin/sshd"); err != nil {
		t.Fatal("opted-in read product fixture requires local sshd")
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal("local fixture owner unavailable")
	}
	dir, err := os.MkdirTemp("/tmp", "km-read-")
	if err != nil {
		t.Fatal("short private fixture directory unavailable")
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal("canonical fixture directory unavailable")
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error("read fixture directory cleanup failed")
		}
	})
	privateDir := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if os.Mkdir(path, 0700) != nil {
			t.Fatal("private fixture subdirectory unavailable")
		}
		return path
	}
	product := filepath.Join(binDir, "keelmux")
	home, clientState, entryState := filepath.Join(dir, "home"), filepath.Join(dir, "client"), filepath.Join(dir, "entry")
	absent := filepath.Join(dir, "absent-state")
	socket := filepath.Join(home, "control.sock")
	child := filepath.Join(dir, "read-child")
	fixtureSource := filepath.Join(root, "internal", "peers", "testdata", "read-child", "main.go")
	productReadHash(t, fixtureSource, "fixture_source")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 30*time.Second)
	build := exec.CommandContext(buildCtx, "go", "build", "-buildvcs=true", "-o", child, "./internal/peers/testdata/read-child")
	build.Dir = root
	build.WaitDelay = 200 * time.Millisecond
	build.Stdout, build.Stderr = io.Discard, io.Discard
	err = build.Run()
	buildCancel()
	if err != nil {
		t.Fatal("committed finite PTY child could not build")
	}
	productReadBinary(t, child, "read-child", source)
	metrics, input, quiet := filepath.Join(dir, "measurements.json"), filepath.Join(dir, "received-input"), filepath.Join(dir, "quiet")
	daemon := startProductFixtureProcess(t, privateDir("daemon-logs"), filepath.Join(binDir, "keelmuxd"), "--state", home)
	homeClient := protocol.NewClient(socket)
	t.Cleanup(func() { homeClient.HTTP.CloseIdleConnections() })
	status := productReadDaemonReady(t, homeClient)
	var inventory Snapshot
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err = homeClient.Call(ctx, "GET", InventoryPath, nil, &inventory)
	cancel()
	if err != nil || inventory.Version != Version || !domain.ValidObjectID(inventory.HostID, "host") || len(inventory.Sessions) != 0 {
		t.Fatal("actual empty home did not provide its stable host identity")
	}
	host := inventory.HostID
	ctx, cancel = context.WithTimeout(context.Background(), 6*time.Second)
	launched, err := homeClient.Launch(ctx, protocol.LaunchRequest{OperationID: domain.NewID("operation"), Name: "owned-read-child", Workspace: dir, Provider: "fixture-read-child", ProviderVersion: "local-test", Argv: []string{child, metrics, input, quiet}})
	cancel()
	session := launched.Session
	stopped := false
	if session.ID != "" {
		t.Cleanup(func() {
			if !stopped {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if homeClient.Stop(ctx, protocol.SessionRequest{SessionID: session.ID, ExecutionID: session.Execution.ID, Generation: session.Execution.Generation, OperationID: domain.NewID("operation")}) != nil {
					t.Error("exact owned child cleanup failed")
				}
			}
		})
	}
	if err != nil || launched.Operation.Outcome != domain.Completed || session.HostID != host || session.Execution.Runtime.PID <= 0 {
		t.Fatal("one actual PTY fixture launch failed; no replay")
	}
	baseline := productReadMeasurement(t, metrics, 0, false)
	productReadNoEffects(t, input, baseline, baseline)

	authorityDir := privateDir("export")
	authorityPath := filepath.Join(authorityDir, "authority.json")
	policy := peerbridge.ReadPolicy{Version: peerbridge.Version, ScopeID: domain.NewID("scope"), HostID: host, Principal: "fixture-owner", Socket: socket, Revision: 1, Enabled: true, Sessions: []peerbridge.SessionActions{{SessionID: session.ID, Inventory: true, TerminalRead: true}}}
	policyFile := filepath.Join(dir, "desired-policy.json")
	policyCLI := func(command string, expected uint64, next peerbridge.ReadPolicy) {
		t.Helper()
		productReadJSON(t, policyFile, next)
		args := []string{"peer-policy", command, "--file", policyFile}
		if command == "update" {
			args = append(args, "--expect-revision", strconv.FormatUint(expected, 10))
		}
		args = append(args, authorityPath)
		result, err := runProductFixture(product, absent, nil, args...)
		var actual peerbridge.ReadPolicy
		if err != nil || decodeStrict(bytes.NewReader(result.Stdout), 65536, &actual) != nil || actual.Revision != next.Revision || actual.ScopeID != next.ScopeID || actual.HostID != host || actual.Enabled != next.Enabled {
			t.Fatal("actual policy CLI did not durably confirm the exact desired revision")
		}
	}
	policyCLI("provision", 0, policy)
	// ForceCommand dispatches only the two complete fixed product commands. It
	// never evals SSH_ORIGINAL_COMMAND or permits generic arguments/commands.
	dispatcher := filepath.Join(dir, "fixed-export-dispatcher")
	dispatch := "#!/bin/sh\ncase \"${SSH_ORIGINAL_COMMAND-}\" in\n" +
		shellQuote(shellQuote(product)+" peer-export") + ") exec " + shellQuote(product) + " peer-export ;;\n" +
		shellQuote(shellQuote(product)+" peer-read-bridge "+shellQuote(authorityPath)) + ") exec " + shellQuote(product) + " peer-read-bridge " + shellQuote(authorityPath) + " ;;\n" +
		"*) exit 125 ;;\nesac\n"
	if os.WriteFile(dispatcher, []byte(dispatch), 0700) != nil {
		t.Fatal("fixed private SSH dispatcher unavailable")
	}
	productReadHash(t, dispatcher, "fixed_dispatcher")
	cfg := productSSHConfig(t, privateDir("ssh-logs"), u.Username, product, socket, host, session.ID, shellQuote(dispatcher))
	enrollment := filepath.Join(dir, "enrollment.json")
	productReadJSON(t, enrollment, cfg)
	enrolled, err := runProductFixture(product, absent, nil, "--state", clientState, "peers", "enroll", "--config", enrollment)
	var view View
	if err != nil || decodeStrict(bytes.NewReader(enrolled.Stdout), SnapshotLimit, &view) != nil || view.HostID != host || view.State != "available" || len(view.Sessions) != 1 || view.Sessions[0].ID != session.ID || view.Sessions[0].Capabilities.NativeAttach {
		t.Fatal("actual SSH enrollment did not remain inventory-only")
	}
	assertProductClientState(t, clientState)
	entryConfig := filepath.Join(dir, "entry.json")
	productReadJSON(t, entryConfig, map[string]any{
		"principal": "fixture-owner",
		"peers":     []any{map[string]any{"host_id": host, "inventory_store": filepath.Join(clientState, "peers"), "terminal_read": ReadSelection{PolicyPath: authorityPath, ScopeID: policy.ScopeID}}},
		"grants":    []any{map[string]any{"host_id": host, "sessions": []domain.ID{session.ID}}},
	})
	entrySocket := filepath.Join(entryState, "control.sock")
	entryClient := transport.NewClient(entrySocket)
	t.Cleanup(func() { entryClient.HTTP.CloseIdleConnections() })
	startEntry := func(name string, inventory bool) *productFixtureProcess {
		t.Helper()
		p := startProductFixtureProcess(t, privateDir(name), filepath.Join(binDir, "keelmux-entry"), "--state", entryState, "--config", entryConfig, "--companion", product)
		if inventory {
			productReadEntryReady(t, entryClient.Client, host, session)
		} else {
			productReadEntrySocket(t, entrySocket)
		}
		return p
	}
	entry := startEntry("entry-first-logs", true)
	makeRequest := func(label string) transport.Request {
		return transport.Request{HostID: host, Lease: protocol.LeaseRequest{SessionID: session.ID, ExecutionID: session.Execution.ID, Generation: session.Execution.Generation, OperationID: domain.NewID("attach"), ClientID: "read-fixture-" + label, ReadOnly: true}, Rows: 24, Cols: 80, Term: "xterm-256color"}
	}
	open := func(request transport.Request) (*productReadObserver, protocol.LeaseRequest) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		attached, err := entryClient.Attach(ctx, request.Lease)
		cancel()
		if err != nil || !attached.ReadOnly || attached.Lease.Token == "" || attached.Lease.SessionID != session.ID || attached.Lease.ExecutionID != session.Execution.ID || attached.Lease.Generation != session.Execution.Generation || attached.Lease.OperationID != request.Lease.OperationID || attached.Lease.ClientID != request.Lease.ClientID {
			t.Fatal("actual entry did not confirm the exact private observer acquisition")
		}
		release := request.Lease
		release.Token = attached.Lease.Token
		observer := productReadOpen(t, entryClient, request)
		observer.expectOutput(t)
		return observer, release
	}
	release := func(request protocol.LeaseRequest) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if entryClient.Release(ctx, request) != nil {
			t.Fatal("exact historical observer release was not confirmed")
		}
	}
	firstRequest := makeRequest("first")
	first, firstRelease := open(firstRequest)
	if first.stream.WriteInput([]byte("FORBIDDEN_PRODUCT_INPUT")) == nil || first.stream.Resize(39, 119) == nil {
		t.Fatal("readonly stream helpers accepted a writer action")
	}
	first.stop(t)
	before := productReadMeasurement(t, metrics, baseline.Tick, false)
	after := productReadMeasurement(t, metrics, before.Tick+2, false)
	productReadNoEffects(t, input, baseline, after)
	productReadOpenRefused(t, entryClient, firstRequest)
	productReadJournal(t, authorityPath, firstRequest.Lease, firstRelease.Token, true, true)
	release(firstRelease)
	release(firstRelease)
	// Restart the actual entry with its original private receipts and home
	// tombstone. Repeating the original operation never opens another native
	// stream; cleanup keeps the original private capability and exact tuple.
	if entry.cmd.Process.Signal(syscall.SIGTERM) != nil || entry.wait(3*time.Second) != nil {
		t.Fatal("owned entry did not stop cleanly for receipt recovery")
	}
	entryClient.HTTP.CloseIdleConnections()
	productReadBridgeIdle(t, authorityPath)
	entry = startEntry("entry-second-logs", true)
	productReadOpenRefused(t, entryClient, firstRequest)
	release(firstRelease)
	productReadJournal(t, authorityPath, firstRequest.Lease, firstRelease.Token, true, true)
	secondRequest := makeRequest("new-operation")
	second, secondRelease := open(secondRequest)
	second.stop(t)
	release(secondRelease)

	// Raw caller frames bypass the convenience methods. The actual entry/home
	// observer must still refuse them before the native child receives bytes or
	// a terminal size change. Each direction gets its own explicit operation.
	var resize [4]byte
	binary.BigEndian.PutUint16(resize[:2], 39)
	binary.BigEndian.PutUint16(resize[2:], 119)
	for _, control := range []struct {
		name  string
		frame transport.Frame
	}{{"raw-input", transport.Frame{Kind: transport.Input, Data: []byte("FORBIDDEN_RAW_INPUT")}}, {"raw-resize", transport.Frame{Kind: transport.Resize, Data: resize[:]}}} {
		request := makeRequest(control.name)
		observer, historical := open(request)
		// The ordinary entry socket client permits raw frames independently of
		// Receipt. Require actual transmission to exercise the live server fence,
		// rather than accepting a convenience-method-only rejection as proof.
		if observer.stream.WriteFrame(control.frame) != nil {
			t.Fatal("raw negative frame could not reach the actual private entry boundary")
		}
		observer.expectClosed(t, 2*time.Second)
		observer.stop(t)
		release(historical)
		after = productReadMeasurement(t, metrics, after.Tick+2, false)
		productReadNoEffects(t, input, baseline, after)
	}

	// The child becomes genuinely quiet while continuing its private heartbeat.
	// Revocation must close an idle observer without waiting for terminal output.
	quietRequest := makeRequest("quiet-revoke")
	quietObserver, quietRelease := open(quietRequest)
	if os.WriteFile(quiet, nil, 0600) != nil {
		t.Fatal("owned quiet fixture marker unavailable")
	}
	quietMeasurement := productReadMeasurement(t, metrics, after.Tick+2, true)
	policy.Enabled = false
	policy.Revision = 2
	policyCLI("update", 1, policy)
	quietObserver.expectClosed(t, 2*time.Second)
	quietObserver.stop(t)
	// A changed home revision permanently closes the old SSH incarnation.
	// A fresh, explicitly started entry recovers original private receipts and
	// can perform exact cleanup against the disabled scope without inventory.
	if entry.cmd.Process.Signal(syscall.SIGTERM) != nil || entry.wait(3*time.Second) != nil {
		t.Fatal("owned entry did not stop cleanly after quiet revocation")
	}
	entryClient.HTTP.CloseIdleConnections()
	productReadBridgeIdle(t, authorityPath)
	entry = startEntry("entry-cleanup-logs", false)
	release(quietRelease)
	policy.Enabled = true
	policy.Revision = 3
	policyCLI("update", 2, policy)
	productReadOpenRefused(t, entryClient, quietRequest)
	after = productReadMeasurement(t, metrics, quietMeasurement.Tick+2, true)
	productReadNoEffects(t, input, baseline, after)
	// A fresh entry/SSH incarnation and explicit new operation can use the
	// regranted policy; the old observer is never resurrected by grant ABA.
	if entry.cmd.Process.Signal(syscall.SIGTERM) != nil || entry.wait(3*time.Second) != nil {
		t.Fatal("owned entry did not stop cleanly after policy revision change")
	}
	entryClient.HTTP.CloseIdleConnections()
	productReadBridgeIdle(t, authorityPath)
	entry = startEntry("entry-third-logs", true)
	if os.Remove(quiet) != nil {
		t.Fatal("owned quiet marker cleanup failed")
	}
	third, thirdRelease := open(makeRequest("after-regrant"))
	third.stop(t)
	release(thirdRelease)
	if entry.cmd.Process.Signal(syscall.SIGTERM) != nil || entry.wait(3*time.Second) != nil {
		t.Fatal("owned final entry did not stop cleanly")
	}
	productReadBridgeIdle(t, authorityPath)
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	err = homeClient.Stop(ctx, protocol.SessionRequest{SessionID: session.ID, ExecutionID: session.Execution.ID, Generation: session.Execution.Generation, OperationID: domain.NewID("operation")})
	cancel()
	if err != nil {
		t.Fatal("exact finite child stop was not confirmed")
	}
	stopped = true
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	err = homeClient.ShutdownDaemon(ctx, status.Instance)
	cancel()
	if err != nil || daemon.wait(2*time.Second) != nil {
		t.Fatal("exact owned daemon shutdown was not confirmed")
	}
	// Contact the missing home through the actual typed SSH bridge, using the
	// existing enrolled store. No fallback may create a home/client daemon.
	registry, err := OpenReadRegistry(filepath.Join(clientState, "peers"))
	if err != nil {
		t.Fatal("existing enrolled registry unavailable")
	}
	defer registry.Close()
	sourceReader, err := NewReadSource(registry, host, "fixture-owner", ReadSelection{PolicyPath: authorityPath, ScopeID: policy.ScopeID})
	if err != nil {
		t.Fatal("explicit missing-home bridge selection unavailable")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 4*time.Second)
	missing, missingErr := sourceReader.List(ctx)
	cancel()
	closeErr := sourceReader.Close()
	if missingErr == nil || len(missing) != 0 || closeErr != nil {
		t.Fatal("actual SSH bridge accepted missing home or failed owned cleanup")
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatal("actual read bridge restarted the missing home daemon")
	}
	if _, err := os.Lstat(absent); !os.IsNotExist(err) {
		t.Fatal("actual read bridge resolved or created ambient state")
	}
	assertProductClientState(t, clientState)
	productReadCleanSource(t, root, source)
	t.Log("proof_scope=owned_same_machine_actual_product_read_SSH; actual_ready=true; unicode_ansi=true; forbidden_native_input=0; forbidden_native_resize=0; original_operation_no_reopen=true; quiet_revocation=true; missing_daemon_no_creation=true; independent_second_host=unqualified; native_model_providers=unqualified; model_turns=0")
}

func productReadCleanSource(t *testing.T, root, source string) {
	t.Helper()
	for _, check := range []struct {
		args []string
		want string
	}{{[]string{"rev-parse", "HEAD"}, source}, {[]string{"status", "--porcelain", "--untracked-files=all"}, ""}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		cmd := exec.CommandContext(ctx, "git", check.args...)
		cmd.Dir = root
		output, err := cmd.Output()
		cancel()
		if err != nil || strings.TrimSpace(string(output)) != check.want {
			t.Fatal("product read qualification requires the exact clean source checkout")
		}
	}
}

func productReadBinary(t *testing.T, path, name, source string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		t.Fatal("owned clean-source product binary unavailable", name)
	}
	build, err := buildinfo.ReadFile(path)
	if err != nil {
		t.Fatal("product build metadata unavailable", name)
	}
	revision, modified := "", ""
	for _, setting := range build.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
		}
		if setting.Key == "vcs.modified" {
			modified = setting.Value
		}
	}
	if revision != source || modified != "false" {
		t.Fatal("product binary is not bound to the exact clean source commit", name)
	}
	productReadHash(t, path, "binary="+name)
}

func productReadHash(t *testing.T, path, label string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal("fixture source/binary unavailable for hashing")
	}
	defer file.Close()
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(file, (128<<20)+1))
	if err != nil || n > 128<<20 {
		t.Fatal("fixture source/binary hash exceeded its bound")
	}
	t.Logf("%s sha256=%s", label, hex.EncodeToString(digest.Sum(nil)))
}

func productReadJSON(t *testing.T, path string, value any) {
	t.Helper()
	bytes, err := json.Marshal(value)
	if err != nil || os.WriteFile(path, bytes, 0600) != nil {
		t.Fatal("private read fixture JSON unavailable")
	}
}

func productReadDaemonReady(t *testing.T, client *protocol.Client) protocol.DaemonStatus {
	t.Helper()
	for deadline := time.Now().Add(4 * time.Second); ; {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		status, err := client.Daemon(ctx)
		cancel()
		if err == nil {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatal("actual read fixture daemon did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func productReadEntryReady(t *testing.T, client *protocol.Client, host domain.ID, session domain.Session) {
	t.Helper()
	for deadline := time.Now().Add(6 * time.Second); ; {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		sessions, err := client.List(ctx)
		cancel()
		if err == nil {
			if len(sessions) != 1 {
				t.Fatal("actual read entry exposed unexpected inventory")
			}
			s := sessions[0]
			if s.ID != session.ID || s.HostID != host || s.Execution.ID != session.Execution.ID || s.Execution.Generation != session.Execution.Generation || s.Execution.Runtime != (domain.RuntimeRef{}) || !s.Capabilities.NativeAttach || !s.Capabilities.ReadOnlyAttach || !s.Capabilities.ObserverOnly || s.Capabilities.Stop || s.Capabilities.SemanticPrompt || s.Capabilities.NativeResume {
				t.Fatal("actual read entry changed identity or advertised forbidden authority")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("actual read entry did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func productReadEntrySocket(t *testing.T, socket string) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); ; {
		conn, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("explicit cleanup-only entry socket did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The fixture owns this provisioned serve guard. Wait for the old exporter to
// release it before explicitly starting a new incarnation; never reconnect or
// resend an opening merely because the first acknowledgement was lost.
func productReadBridgeIdle(t *testing.T, authority string) {
	t.Helper()
	fd, err := syscall.Open(authority+".serve", syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal("owned exporter cleanup guard unavailable")
	}
	defer syscall.Close(fd)
	for deadline := time.Now().Add(5 * time.Second); ; {
		if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			syscall.Flock(fd, syscall.LOCK_UN)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("owned remote exporter cleanup was not confirmed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type productReadMetrics struct {
	Tick          uint64 `json:"tick"`
	ReceivedBytes uint64 `json:"received_bytes"`
	Rows          int    `json:"rows"`
	Cols          int    `json:"cols"`
	SizeChanges   uint64 `json:"size_changes"`
	WinchSignals  uint64 `json:"winch_signals"`
	Quiet         bool   `json:"quiet"`
}

func productReadMeasurement(t *testing.T, path string, after uint64, quiet bool) productReadMetrics {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); ; {
		bytes, err := os.ReadFile(path)
		var m productReadMetrics
		if err == nil && decodeStrict(strings.NewReader(string(bytes)), 4096, &m) == nil && m.Tick > after && m.Quiet == quiet && m.Rows > 0 && m.Cols > 0 {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatal("owned native child did not produce a fresh complete measurement")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func productReadNoEffects(t *testing.T, input string, before, after productReadMetrics) {
	t.Helper()
	info, err := os.Lstat(input)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 || after.ReceivedBytes != 0 || before.ReceivedBytes != 0 || after.Rows != before.Rows || after.Cols != before.Cols || after.SizeChanges != before.SizeChanges || after.WinchSignals != before.WinchSignals {
		t.Fatal("forbidden bytes or terminal size change reached the owned native child")
	}
}

type productReadObserver struct {
	stream *transport.Stream
	cancel context.CancelFunc
	seen   chan struct{}
	done   chan struct{}
	once   sync.Once
}

func productReadOpen(t *testing.T, client *transport.Client, request transport.Request) *productReadObserver {
	t.Helper()
	// The stream lifetime exceeds control/handshake timeouts. A helper below
	// bounds each expected observation and closes this owned connection on error.
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.Open(ctx, request)
	if err != nil {
		cancel()
		t.Fatal("actual entry/home stream did not return a matching Ready")
	}
	want := transport.Receipt{HostID: request.HostID, SessionID: request.Lease.SessionID, ExecutionID: request.Lease.ExecutionID, Generation: request.Lease.Generation, OperationID: request.Lease.OperationID, ClientID: request.Lease.ClientID, ReadOnly: true}
	if stream.Receipt != want {
		cancel()
		stream.Close()
		t.Fatal("actual Ready changed the original observer identity")
	}
	b, _ := json.Marshal(stream.Receipt)
	if bytes.Contains(b, []byte("token")) || bytes.Contains(b, []byte("socket")) || bytes.Contains(b, []byte("argv")) {
		cancel()
		stream.Close()
		t.Fatal("actual public Ready exposed private home capability data")
	}
	o := &productReadObserver{stream: stream, cancel: cancel, seen: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(o.done)
		var retained []byte
		for {
			frame, err := stream.ReadFrame()
			if err != nil || frame.Kind == transport.Close || frame.Kind == transport.Failure {
				return
			}
			if frame.Kind != transport.Output {
				return
			}
			retained = append(retained, frame.Data...)
			if bytes.Contains(retained, []byte(productReadOutput)) {
				o.once.Do(func() { close(o.seen) })
			}
			if len(retained) > 65536 {
				retained = append([]byte(nil), retained[len(retained)-65536:]...)
			}
		}
	}()
	t.Cleanup(func() { o.stop(t) })
	return o
}

func (o *productReadObserver) expectOutput(t *testing.T) {
	t.Helper()
	select {
	case <-o.seen:
	case <-o.done:
		t.Fatal("actual observer closed before Unicode/ANSI output")
	case <-time.After(3 * time.Second):
		t.Fatal("actual observer did not preserve fresh Unicode/ANSI output")
	}
}

func (o *productReadObserver) expectClosed(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-o.done:
	case <-time.After(timeout):
		t.Fatal("actual observer remained open after forbidden direction or quiet revocation")
	}
}

func (o *productReadObserver) stop(t *testing.T) {
	t.Helper()
	o.cancel()
	_ = o.stream.Close()
	select {
	case <-o.done:
	case <-time.After(time.Second):
		t.Error("owned observer reader cleanup was not confirmed")
	}
}

func productReadOpenRefused(t *testing.T, client *transport.Client, request transport.Request) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := client.Open(ctx, request)
	if stream != nil {
		stream.Close()
	}
	var rejection *protocol.HTTPError
	if err == nil || !errors.As(err, &rejection) || rejection.StatusCode < 400 {
		t.Fatal("original attempted operation was not explicitly refused without another Ready")
	}
}

// Test-only private inspection confirms one unchanged attempted tombstone.
// It never logs a token or private request. Native opening is not inferred from
// a directory route or reconstructed historical Ready.
func productReadJournal(t *testing.T, path string, request protocol.LeaseRequest, token string, attempted, ready bool) {
	t.Helper()
	var a struct {
		Version    string                `json:"version"`
		Policy     peerbridge.ReadPolicy `json:"policy"`
		Operations []struct {
			Request          protocol.LeaseRequest `json:"request"`
			Revision         uint64                `json:"revision"`
			Lease            *domain.Lease         `json:"lease,omitempty"`
			OpenAttempted    bool                  `json:"open_attempted"`
			ReadyObserved    bool                  `json:"ready_observed"`
			ReleaseAttempted bool                  `json:"release_attempted"`
			Released         bool                  `json:"released"`
		} `json:"operations"`
	}
	b, err := os.ReadFile(path)
	if err != nil || decodeStrict(bytes.NewReader(b), 2<<20, &a) != nil || a.Version != peerbridge.Version {
		t.Fatal("private home operation evidence unavailable")
	}
	matches := 0
	for _, o := range a.Operations {
		if o.Request.OperationID != request.OperationID {
			continue
		}
		matches++
		if o.Request != request || o.Revision != 1 || o.Lease == nil || *o.Lease != (domain.Lease{SessionID: request.SessionID, ExecutionID: request.ExecutionID, Generation: request.Generation, OperationID: request.OperationID, ClientID: request.ClientID, Token: token}) || o.OpenAttempted != attempted || o.ReadyObserved != ready {
			t.Fatal("home changed the original durable operation binding or attempted-open evidence")
		}
	}
	if matches != 1 {
		t.Fatal("home lost or duplicated the original attempted-open tombstone")
	}
}

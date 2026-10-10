package peers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/federation"
	"github.com/seanheiney/keelmux/internal/protocol"
)

// This proves the actual product CLI/exporter/daemon over owned loopback SSH.
// It is separate from the exporter-helper test and independent second-host proof.
// The explicit binaries must have been built from a clean, identified commit.
func TestOwnedLoopbackSSHProductInventory(t *testing.T) {
	if os.Getenv("KEELMUX_PEER_PRODUCT_SSH") != "1" {
		t.Skip("set KEELMUX_PEER_PRODUCT_SSH=1 with an explicit product binary directory and source SHA")
	}
	binDir, source := os.Getenv("KEELMUX_PEER_PRODUCT_BIN_DIR"), os.Getenv("KEELMUX_PEER_PRODUCT_SOURCE_SHA")
	if !filepath.IsAbs(binDir) || filepath.Clean(binDir) != binDir || len(source) != 40 || strings.ContainsFunc(source, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) {
		t.Fatal("product fixture requires an absolute binary directory and exact lowercase source SHA")
	}
	for _, name := range []string{"keelmux", "keelmuxd", "keelmux-supervisor"} {
		path := filepath.Join(binDir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			t.Fatalf("owned built product executable %s unavailable", name)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("cannot hash built product executable")
		}
		sum := sha256.Sum256(b)
		t.Logf("product_source=%s binary=%s sha256=%s", source, name, hex.EncodeToString(sum[:]))
		if build, err := buildinfo.ReadFile(path); err == nil {
			for _, setting := range build.Settings {
				if setting.Key == "vcs.revision" && setting.Value != source || setting.Key == "vcs.modified" && setting.Value != "false" {
					t.Fatal("binary build information differs from the explicit clean source commit")
				}
			}
		}
	}
	if _, err := os.Stat("/usr/sbin/sshd"); err != nil {
		t.Fatal("opted-in product fixture requires local sshd")
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	// Keep both the control and execution sockets below portable Unix bounds;
	// macOS's ambient TMPDIR is commonly too long after canonicalization.
	dir, err := os.MkdirTemp("/tmp", "km-prod-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error("fixture directory cleanup failed")
		}
	})
	product := filepath.Join(binDir, "keelmux")
	home, clientState, absent := filepath.Join(dir, "home"), filepath.Join(dir, "client"), filepath.Join(dir, "absent-state")
	socket := filepath.Join(home, "control.sock")
	daemon := startProductFixtureProcess(t, dir, filepath.Join(binDir, "keelmuxd"), "--state", home)
	client := protocol.NewClient(socket)
	t.Cleanup(func() { client.HTTP.CloseIdleConnections() })
	var status protocol.DaemonStatus
	for deadline := time.Now().Add(4 * time.Second); ; {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		status, err = client.Daemon(ctx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual isolated daemon did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var snapshot Snapshot
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	err = client.Call(ctx, "GET", InventoryPath, nil, &snapshot)
	cancel()
	if err != nil || !domain.ValidObjectID(snapshot.HostID, "host") || snapshot.Version != Version || len(snapshot.Sessions) != 0 {
		t.Fatal("actual empty home endpoint did not return its stable host identity", err)
	}
	host := snapshot.HostID
	cfg := productSSHConfig(t, dir, u.Username, product, socket, host, domain.NewID("session"))
	empty, err := (SSHTransport{}).Fetch(context.Background(), cfg)
	if err != nil || empty.HostID != host || len(empty.Sessions) != 0 {
		t.Fatal("actual product exporter over SSH lost the empty home's stable identity", err)
	}
	wrongAuthority := cfg
	wrongAuthority.HostID = domain.NewID("host")
	wrongAuthority.Grant.HostID = wrongAuthority.HostID
	if _, err := (SSHTransport{}).Fetch(context.Background(), wrongAuthority); err == nil {
		t.Fatal("actual product exporter over SSH accepted a different home identity with zero sessions")
	}
	var sessions []domain.Session
	stopSessions := func() {
		for _, s := range sessions {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := client.Stop(ctx, protocol.SessionRequest{SessionID: s.ID, ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, OperationID: domain.NewID("operation")})
			cancel()
			if err != nil {
				t.Error("exact fixture session cleanup failed", err)
			}
		}
		sessions = nil
	}
	t.Cleanup(stopSessions)
	for _, name := range []string{"permitted-shell", "hidden-shell"} {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		response, err := client.Launch(ctx, protocol.LaunchRequest{OperationID: domain.NewID("operation"), Name: name, Workspace: dir, Provider: "fixture-shell", ProviderVersion: "local-test", Argv: []string{"/bin/sh", "-c", "exec /bin/sleep 45"}})
		cancel()
		if response.Session.ID != "" {
			sessions = append(sessions, response.Session)
		}
		if err != nil || response.Operation.Outcome != domain.Completed || response.Session.Execution.Runtime.PID <= 0 {
			t.Fatal("one actual fixture launch failed; no replay", err)
		}
	}
	allowed, hidden := sessions[0], sessions[1]
	cfg.Grant.Sessions = []domain.ID{allowed.ID}
	writeJSON := func(path string, value any) {
		b, err := json.Marshal(value)
		if err != nil || os.WriteFile(path, b, 0600) != nil {
			t.Fatal("private fixture configuration write failed")
		}
	}
	configFile := filepath.Join(dir, "enroll.json")
	writeJSON(configFile, cfg)
	cli := func(args ...string) ProcessOutput {
		t.Helper()
		result, err := runProductFixture(product, absent, nil, append([]string{"--state", clientState, "peers"}, args...)...)
		if err != nil {
			t.Fatalf("actual product peers command %s failed: %v", args[0], err)
		}
		return result
	}
	view := func(args ...string) View {
		t.Helper()
		var v View
		if decodeStrict(bytes.NewReader(cli(args...).Stdout), SnapshotLimit, &v) != nil {
			t.Fatal("actual peers CLI returned malformed view")
		}
		return v
	}
	checkSession := func(v View, state string) {
		t.Helper()
		if v.State != state || v.HostID != host || len(v.Sessions) != 1 || len(v.Workspaces) != 1 || v.Workspaces[0].State != "recorded" || v.Workspaces[0].Path != dir {
			t.Fatal("actual product inventory state or grant projection incorrect")
		}
		s := v.Sessions[0]
		if s.ID != allowed.ID || s.ID == hidden.ID || s.HostID != allowed.HostID || s.WorkID != allowed.WorkID || s.ThreadID != allowed.ThreadID || s.LaneID != allowed.LaneID || s.Execution.ID != allowed.Execution.ID || s.Execution.Generation != allowed.Execution.Generation || s.Execution.Runtime != (domain.RuntimeRef{}) || s.Capabilities.NativeAttach || s.Capabilities.ReadOnlyAttach || s.Capabilities.Stop || s.Capabilities.SemanticPrompt || s.Capabilities.NativeResume {
			t.Fatal("actual product inventory changed stable identity, exposed hidden metadata or supplied action authority")
		}
	}
	checkSession(view("enroll", "--config", configFile), "available")
	checkSession(view("inspect", "--principal", "fixture-owner", string(host)), "available")
	var directory Directory
	if decodeStrict(bytes.NewReader(cli("list", "--principal", "fixture-owner", "--refresh").Stdout), SnapshotLimit, &directory) != nil || len(directory.Sessions) != 1 || directory.Sessions[0].ID != allowed.ID || len(directory.Places) != 1 || directory.Places[0].State != "available" {
		t.Fatal("actual list/refresh returned incorrect permitted directory")
	}
	// No CLI inventory operation may start a client daemon or create local host/runtime authority.
	assertProductClientState(t, clientState)
	result, err := runProductFixture(product, absent, nil, "--state", clientState, "peers", "inspect", "--principal", "other-owner", string(host))
	if err == nil || bytes.Contains(result.Stdout, []byte(allowed.ID)) {
		t.Fatal("actual CLI disclosed another principal's inventory")
	}
	result, err = runProductFixture(product, absent, nil, "--state", clientState, "peers", "list", "--principal", "fixture-owner", "--refresh=PRIVATE_ARGUMENT_MARKER")
	if err == nil || bytes.Contains(result.Stdout, []byte("PRIVATE_ARGUMENT_MARKER")) || bytes.Contains(result.Stderr, []byte("PRIVATE_ARGUMENT_MARKER")) {
		t.Fatal("actual peers CLI echoed invalid private flag value")
	}
	// Grant shrink and subsequent expansion cannot resurrect cached metadata.
	if v := view("grant", "--principal", "fixture-owner", "--sessions", "", string(host)); v.State != "revoked" || len(v.Sessions) != 0 {
		t.Fatal("actual empty grant retained metadata")
	}
	if v := view("grant", "--principal", "fixture-owner", "--sessions", string(allowed.ID), string(host)); len(v.Sessions) != 0 {
		t.Fatal("actual grant expansion resurrected old cache")
	}
	checkSession(view("refresh", "--principal", "fixture-owner", string(host)), "available")
	wrong := cfg
	wrong.KnownHostsFile = filepath.Join(dir, "wrong_known_hosts")
	writeKnownKey(t, &wrong)
	if _, err := (SSHTransport{}).Fetch(context.Background(), wrong); !errors.Is(err, ErrHostKey) {
		t.Fatal("actual product SSH accepted another selected host key", err)
	}
	// Natural TTL expiration is visible while the real daemon is still healthy.
	fresh := view("refresh", "--principal", "fixture-owner", string(host))
	time.Sleep(time.Until(fresh.FreshUntil) + 30*time.Millisecond)
	checkSession(view("inspect", "--principal", "fixture-owner", string(host)), "stale")
	checkSession(view("refresh", "--principal", "fixture-owner", string(host)), "available")
	stopSessions()
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	err = client.ShutdownDaemon(ctx, status.Instance)
	cancel()
	if err != nil {
		t.Fatal("exact fixture daemon shutdown failed", err)
	}
	if err := daemon.wait(2 * time.Second); err != nil {
		t.Fatal("actual fixture daemon did not exit cleanly", err)
	}
	checkSession(view("refresh", "--principal", "fixture-owner", string(host)), "stale")
	// The real exporter must not resolve ambient state or start a missing daemon.
	request, _ := json.Marshal(ExportRequest{Version: Version, Socket: socket, Grant: cfg.Grant})
	result, err = runProductFixture(product, absent, request, "peer-export")
	if err == nil || len(result.Stdout) != 0 || bytes.Contains(result.Stderr, []byte(socket)) {
		t.Fatal("actual stopped-home export succeeded or disclosed private socket")
	}
	result, err = runProductFixture(product, absent, request, "--state", absent, "peer-export")
	if err == nil || len(result.Stdout) != 0 {
		t.Fatal("actual exporter accepted a global state flag")
	}
	if _, err := os.Lstat(absent); !os.IsNotExist(err) {
		t.Fatal("actual exporter or peer CLI created ambient state")
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatal("actual peer refresh/export restarted the stopped home")
	}
	if v := view("revoke", "--principal", "fixture-owner", string(host)); v.State != "revoked" || len(v.Sessions) != 0 {
		t.Fatal("actual revoke retained metadata")
	}
	if v := view("refresh", "--principal", "fixture-owner", string(host)); v.State != "revoked" || len(v.Sessions) != 0 {
		t.Fatal("actual refresh revived a revoked enrollment")
	}
	assertProductClientState(t, clientState)
	t.Log("proof_scope=owned_same_machine_actual_product_SSH; independent_second_host=unqualified; model_turns=0")
}

func runProductFixture(executable, absent string, input []byte, args ...string) (ProcessOutput, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = append(os.Environ(), "KEELMUX_STATE_DIR="+absent, "BP_STATE_DIR="+absent)
	cmd.Stdin = bytes.NewReader(input)
	cmd.WaitDelay = 200 * time.Millisecond
	stdout, stderr := &boundedWriter{limit: SnapshotLimit, cancel: cancel}, &boundedWriter{limit: 8192, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	return ProcessOutput{Stdout: stdout.b.Bytes(), Stderr: stderr.b.Bytes()}, err
}

func assertProductClientState(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "peers" || !entries[0].IsDir() {
		t.Fatal("actual peer CLI created client host/daemon/runtime state")
	}
}

type productFixtureProcess struct {
	cmd      *exec.Cmd
	ended    chan error
	finished bool
}

func (p *productFixtureProcess) wait(timeout time.Duration) error {
	if p.finished {
		return nil
	}
	select {
	case err := <-p.ended:
		p.finished = true
		return err
	case <-time.After(timeout):
		return errors.New("fixture process did not exit within its bound")
	}
}

func startProductFixtureProcess(t *testing.T, dir, executable string, args ...string) *productFixtureProcess {
	t.Helper()
	log, err := os.OpenFile(filepath.Join(dir, filepath.Base(executable)+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("private fixture log unavailable")
	}
	cmd := exec.Command(executable, args...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatal("owned fixture process could not start", err)
	}
	p := &productFixtureProcess{cmd: cmd, ended: make(chan error, 1)}
	go func() { p.ended <- cmd.Wait() }()
	t.Cleanup(func() {
		defer log.Close()
		if p.finished {
			return
		}
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		if err := p.wait(time.Second); err != nil {
			_ = p.cmd.Process.Kill()
			if err := p.wait(time.Second); err != nil {
				t.Error("owned fixture process forced cleanup", err)
			}
		}
	})
	return p
}

func productSSHConfig(t *testing.T, processDir, username, product, socket string, host, session domain.ID) Config {
	t.Helper()
	// OpenSSH StrictModes checks authorized_keys ancestors as well as the file.
	// Socket state needs short /tmp paths, but key material uses the user's owned
	// ambient temp directory instead of a world-writable /tmp ancestor.
	dir, err := os.MkdirTemp("", "km-prod-ssh-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error("fixture key directory cleanup failed")
		}
	})
	keygen := func(name string) string {
		path := filepath.Join(dir, name)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path).Run(); err != nil {
			t.Fatal("ephemeral fixture key generation failed", err)
		}
		return path
	}
	hostKey, loginKey := keygen("host-key"), keygen("login-key")
	public, err := os.ReadFile(loginKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	authorized := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authorized, append([]byte("restrict "), public...), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	serverConfig := filepath.Join(dir, "sshd_config")
	content := fmt.Sprintf("ListenAddress 127.0.0.1\nPort %d\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nStrictModes yes\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nPermitRootLogin prohibit-password\nAllowUsers %s\nAllowAgentForwarding no\nAllowTcpForwarding no\nX11Forwarding no\nPermitTTY no\nPermitUserRC no\nForceCommand %s peer-export\nLogLevel ERROR\n", port, hostKey, filepath.Join(dir, "sshd.pid"), authorized, username, shellQuote(product))
	if err := os.WriteFile(serverConfig, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	server := startProductFixtureProcess(t, processDir, "/usr/sbin/sshd", "-D", "-e", "-f", serverConfig)
	for deadline := time.Now().Add(2 * time.Second); ; {
		select {
		case err := <-server.ended:
			server.finished = true
			t.Fatal("owned product sshd exited before readiness", err)
		default:
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned product sshd did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cfg := Config{HostID: host, Alias: "product-fixture", KnownHostsFile: filepath.Join(dir, "known_hosts"), SSHConfigFile: filepath.Join(dir, "ssh_config"), RemoteExecutable: product, RemoteSocket: socket, Grant: federation.Grant{Principal: "fixture-owner", HostID: host, Sessions: []domain.ID{session}}, FreshForSeconds: 2, RetainForSeconds: 60}
	client := fmt.Sprintf("Host %s\n HostName 127.0.0.1\n Port %d\n User %s\n IdentityFile %s\n IdentitiesOnly yes\n RemoteCommand touch %s\n", cfg.Alias, port, username, loginKey, filepath.Join(dir, "ambient-command-must-not-run"))
	if err := os.WriteFile(cfg.SSHConfigFile, []byte(client), 0600); err != nil {
		t.Fatal(err)
	}
	public, err = os.ReadFile(hostKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(public))
	cfg.Fingerprint, _ = keyFingerprint(fields[0], fields[1])
	if err := os.WriteFile(cfg.KnownHostsFile, []byte(cfg.Alias+" "+fields[0]+" "+fields[1]+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Lstat(filepath.Join(dir, "ambient-command-must-not-run")); !os.IsNotExist(err) {
			t.Error("ambient SSH RemoteCommand executed")
		}
	})
	return cfg
}

package peers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
)

// This helper is the owned remote executable in the loopback transport proof.
// Exit before the Go test harness can append PASS text to the strict envelope.
func TestSSHExportSubprocess(t *testing.T) {
	if os.Getenv("KEELMUX_PEER_EXPORT_CHILD") != "1" {
		return
	}
	if err := Export(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Explicitly opt in because an owned SSH listener is an integration fixture.
// It is never an independent second server or a production service change.
func TestOwnedLoopbackSSHEnrollmentDiscoveryAndHostKeyRejection(t *testing.T) {
	if os.Getenv("KEELMUX_PEER_REAL_SSH") != "1" {
		t.Skip("set KEELMUX_PEER_REAL_SSH=1 for an owned loopback sshd fixture")
	}
	sshd := "/usr/sbin/sshd"
	if _, err := os.Stat(sshd); err != nil {
		t.Skip("local sshd executable unavailable")
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "km-sshd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	keygen := func(name string) string {
		path := filepath.Join(dir, name)
		if b, err := exec.Command("/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path).CombinedOutput(); err != nil {
			t.Fatalf("owned key generation: %v %s", err, b)
		}
		return path
	}
	hostKey, loginKey := keygen("host"), keygen("login")
	loginPublic, err := os.ReadFile(loginKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	authorized := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authorized, append([]byte("restrict "), loginPublic...), 0600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "keelmux-fixture")
	script := "#!/bin/sh\n[ \"$#\" -eq 1 ] && [ \"$1\" = peer-export ] || exit 64\nexport KEELMUX_PEER_EXPORT_CHILD=1\nexec " + shellQuote(self) + " -test.run=^TestSSHExportSubprocess$\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	serverConfig := filepath.Join(dir, "sshd_config")
	content := "ListenAddress 127.0.0.1\nPort " + strconv.Itoa(port) + "\nHostKey " + hostKey + "\nPidFile " + filepath.Join(dir, "sshd.pid") + "\nAuthorizedKeysFile " + authorized + "\nStrictModes yes\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nPermitRootLogin prohibit-password\nAllowUsers " + u.Username + "\nAllowAgentForwarding no\nAllowTcpForwarding no\nX11Forwarding no\nPermitTTY no\nPermitUserRC no\nForceCommand " + shellQuote(helper) + " peer-export\nLogLevel ERROR\n"
	if err := os.WriteFile(serverConfig, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, sshd, "-D", "-e", "-f", serverConfig)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Skipf("owned sshd cannot start: %v", err)
	}
	ended := make(chan error, 1)
	go func() { ended <- cmd.Wait() }()
	defer func() {
		cancel()
		select {
		case <-ended:
		case <-time.After(time.Second):
			_ = cmd.Process.Kill()
		}
	}()
	ready := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-ended:
			t.Skipf("owned sshd unavailable under this account: %v %s", err, logs.String())
		default:
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("owned sshd did not become available")
	}
	c, s := fixture(t)
	c.RemoteSocket = ownedSocket(t, HomeHandler(c.HostID, inventoryFunc(func(context.Context) ([]domain.Session, error) { return s.Sessions, nil })))
	c.RemoteExecutable = helper
	c.SSHConfigFile = filepath.Join(dir, "ssh_config")
	clientContent := "Host " + c.Alias + "\n HostName 127.0.0.1\n Port " + strconv.Itoa(port) + "\n User " + u.Username + "\n IdentityFile " + loginKey + "\n IdentitiesOnly yes\n RemoteCommand touch " + filepath.Join(dir, "ambient-command-must-not-run") + "\n"
	if err := os.WriteFile(c.SSHConfigFile, []byte(clientContent), 0600); err != nil {
		t.Fatal(err)
	}
	hostPublic, err := os.ReadFile(hostKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(hostPublic))
	c.Fingerprint, _ = keyFingerprint(fields[0], fields[1])
	if err := os.WriteFile(c.KnownHostsFile, []byte(c.Alias+" "+fields[0]+" "+fields[1]+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := openFixture(t, SSHTransport{})
	v, err := r.Enroll(context.Background(), c)
	if err != nil {
		t.Fatalf("loopback SSH enrollment: %v (sshd logs kept private in the disposable fixture)", err)
	}
	if v.State != "available" || len(v.Sessions) != 1 || v.Sessions[0].HostID != c.HostID || v.Sessions[0].ID != s.Sessions[0].ID {
		t.Fatal("real SSH identity/scope incorrect", v)
	}
	if _, err := os.Stat(filepath.Join(dir, "ambient-command-must-not-run")); !os.IsNotExist(err) {
		t.Fatal("ambient SSH RemoteCommand executed")
	}
	// A different explicitly selected public key at the same live SSH endpoint
	// must fail host verification, without bypass, password prompt or TOFU.
	wrong := c
	writeKnownKey(t, &wrong)
	if _, err := (SSHTransport{}).Fetch(context.Background(), wrong); !errors.Is(err, ErrHostKey) {
		t.Fatal("real SSH host-key mismatch accepted", err)
	}
}

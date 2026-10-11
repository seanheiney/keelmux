package peers

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/federation"
)

// Config records the reviewed execution-home identity, one existing SSH alias,
// one selected known-host key, and an exact inventory grant. Neither an alias
// nor a public host key substitutes for the stable product HostID.
type Config struct {
	HostID           domain.ID        `json:"host_id"`
	Alias            string           `json:"alias"`
	KnownHostsFile   string           `json:"known_hosts_file"`
	KnownHost        string           `json:"known_host,omitempty"`
	SSHConfigFile    string           `json:"ssh_config_file,omitempty"`
	Fingerprint      string           `json:"fingerprint"`
	RemoteExecutable string           `json:"remote_executable"`
	RemoteSocket     string           `json:"remote_socket"`
	Grant            federation.Grant `json:"grant"`
	FreshForSeconds  int              `json:"fresh_for_seconds"`
	RetainForSeconds int              `json:"retain_for_seconds"`
}

func (c Config) Validate() error {
	if !domain.ValidObjectID(c.HostID, "host") || !validAlias(c.Alias) || !cleanPath(c.KnownHostsFile, 4096) || !validFingerprint(c.Fingerprint) || !cleanPath(c.RemoteExecutable, 4096) || !cleanPath(c.RemoteSocket, 100) || c.Grant.HostID != c.HostID || validateGrant(c.Grant) != nil || c.FreshForSeconds < 1 || c.FreshForSeconds > 300 || c.RetainForSeconds < c.FreshForSeconds || c.RetainForSeconds > 86400 {
		return ErrMalformed
	}
	if c.SSHConfigFile != "" && !cleanPath(c.SSHConfigFile, 4096) {
		return ErrMalformed
	}
	if c.KnownHost != "" && !validHostToken(c.KnownHost) {
		return ErrMalformed
	}
	return nil
}

func validHostToken(s string) bool {
	if s == "" || len(s) > 256 || s[0] == '-' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.-[]:", c)) {
			return false
		}
	}
	return true
}

func validAlias(s string) bool {
	if len(s) == 0 || len(s) > 128 || s[0] == '-' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

func validFingerprint(s string) bool {
	if !strings.HasPrefix(s, "SHA256:") {
		return false
	}
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(s, "SHA256:"))
	return err == nil && len(b) == sha256.Size
}

// SelectKnownHost requires an explicit matching alias and SHA256 fingerprint
// from an existing owned, non-writable known_hosts file. Hashed aliases work;
// wildcard trust, host certificates and @cert-authority are not imported.
// A matching @revoked key overrides any unmarked copy of that same key.
func SelectKnownHost(path, alias, fingerprint string) (string, error) {
	if !cleanPath(path, 4096) || !validHostToken(alias) || !validFingerprint(fingerprint) {
		return "", ErrHostKey
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", ErrHostKey
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || info.Size() > SnapshotLimit {
		return "", ErrHostKey
	}
	b, err := io.ReadAll(io.LimitReader(f, SnapshotLimit+1))
	if err != nil || len(b) > SnapshotLimit {
		return "", ErrHostKey
	}
	selected := ""
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		marker := ""
		if strings.HasPrefix(fields[0], "@") {
			marker, fields = fields[0], fields[1:]
		}
		if len(fields) < 3 {
			continue
		}
		fp, ok := keyFingerprint(fields[1], fields[2])
		if !ok || fp != fingerprint {
			continue
		}
		if marker == "@revoked" {
			return "", ErrHostKey
		}
		if marker != "" || !knownAlias(fields[0], alias) {
			continue
		}
		selected = alias + " " + fields[1] + " " + fields[2] + "\n"
	}
	if selected == "" {
		return "", ErrHostKey
	}
	return selected, nil
}

func knownAlias(hosts, alias string) bool {
	for _, name := range strings.Split(hosts, ",") {
		if name == alias {
			return true
		}
		parts := strings.Split(name, "|")
		if len(parts) != 4 || parts[0] != "" || parts[1] != "1" {
			continue
		}
		salt, e1 := base64.StdEncoding.DecodeString(parts[2])
		want, e2 := base64.StdEncoding.DecodeString(parts[3])
		if e1 != nil || e2 != nil || len(salt) != sha1.Size || len(want) != sha1.Size {
			continue
		}
		mac := hmac.New(sha1.New, salt)
		_, _ = mac.Write([]byte(alias))
		if hmac.Equal(mac.Sum(nil), want) {
			return true
		}
	}
	return false
}

func keyFingerprint(kind, value string) (string, bool) {
	if kind != "ssh-ed25519" && kind != "ssh-rsa" && kind != "ecdsa-sha2-nistp256" && kind != "ecdsa-sha2-nistp384" && kind != "ecdsa-sha2-nistp521" {
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(b) < 4 || len(b) > 16384 {
		return "", false
	}
	n := int(binary.BigEndian.Uint32(b[:4]))
	if n > len(b)-4 || string(b[4:4+n]) != kind {
		return "", false
	}
	remaining := b[4+n:]
	part := func() ([]byte, bool) {
		if len(remaining) < 4 {
			return nil, false
		}
		n := int(binary.BigEndian.Uint32(remaining[:4]))
		if n > len(remaining)-4 {
			return nil, false
		}
		v := remaining[4 : 4+n]
		remaining = remaining[4+n:]
		return v, true
	}
	first, ok := part()
	if !ok {
		return "", false
	}
	switch kind {
	case "ssh-ed25519":
		if len(first) != 32 {
			return "", false
		}
	case "ssh-rsa":
		modulus, ok := part()
		if !ok || len(first) == 0 || len(modulus) == 0 || first[0]&0x80 != 0 || modulus[0]&0x80 != 0 {
			return "", false
		}
		e := new(big.Int).SetBytes(first)
		m := new(big.Int).SetBytes(modulus)
		if e.BitLen() > 32 || e.Cmp(big.NewInt(3)) < 0 || e.Bit(0) != 1 || m.BitLen() < 1024 || m.BitLen() > 8192 {
			return "", false
		}
	default:
		curveName := strings.TrimPrefix(kind, "ecdsa-sha2-")
		if string(first) != curveName {
			return "", false
		}
		point, ok := part()
		if !ok {
			return "", false
		}
		curve := elliptic.P256()
		if curveName == "nistp384" {
			curve = elliptic.P384()
		}
		if curveName == "nistp521" {
			curve = elliptic.P521()
		}
		x, y := elliptic.Unmarshal(curve, point)
		if x == nil || y == nil {
			return "", false
		}
	}
	if len(remaining) != 0 {
		return "", false
	}
	sum := sha256.Sum256(b)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), true
}

type Transport interface {
	Fetch(context.Context, Config) (Snapshot, error)
}

type ProcessOutput struct{ Stdout, Stderr []byte }
type Runner interface {
	Run(context.Context, string, []string, []byte) (ProcessOutput, error)
}

// SSHTransport delegates native login to OpenSSH. No password, native model
// credential or agent forwarding is sent to the home. Runner can be supplied
// for adverse transport conformance; the default executes the local ssh binary.
type SSHTransport struct {
	Executable string
	Runner     Runner
}

func (t SSHTransport) Fetch(ctx context.Context, c Config) (Snapshot, error) {
	if err := c.Validate(); err != nil {
		return Snapshot{}, err
	}
	selectedName := c.KnownHost
	if selectedName == "" {
		selectedName = c.Alias
	}
	key, err := SelectKnownHost(c.KnownHostsFile, selectedName, c.Fingerprint)
	if err != nil {
		return Snapshot{}, err
	}
	// The selected source record may name the real host/port rather than its
	// user SSH alias. Bind its one public key to the explicit HostKeyAlias.
	fields := strings.Fields(key)
	key = c.Alias + " " + fields[1] + " " + fields[2] + "\n"
	algorithm := fields[1]
	if algorithm == "ssh-rsa" {
		algorithm = "rsa-sha2-512,rsa-sha2-256"
	}
	// A private single-key file constrains trust even when known_hosts contains
	// several keys for this alias. Recheck selection on every new connection.
	f, err := os.CreateTemp("", "keelmux-peer-key-*")
	if err != nil {
		return Snapshot{}, ErrHostKey
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(key); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err != nil {
		return Snapshot{}, ErrHostKey
	}
	executable := t.Executable
	if executable == "" {
		executable = "/usr/bin/ssh"
	}
	if !filepath.IsAbs(executable) {
		return Snapshot{}, ErrMalformed
	}
	args := []string{
		"-T", "-oBatchMode=yes", "-oStrictHostKeyChecking=yes",
		"-oUserKnownHostsFile=" + f.Name(), "-oGlobalKnownHostsFile=/dev/null",
		"-oHostKeyAlias=" + c.Alias, "-oUpdateHostKeys=no", "-oVerifyHostKeyDNS=no",
		"-oHostKeyAlgorithms=" + algorithm,
		"-oForwardAgent=no", "-oForwardX11=no", "-oClearAllForwardings=yes",
		"-oPermitLocalCommand=no", "-oRemoteCommand=none", "-oRequestTTY=no",
		"-oControlMaster=no", "-oControlPath=none", "-oCanonicalizeHostname=no",
		"-oConnectionAttempts=1", "-oConnectTimeout=2", "-oServerAliveInterval=1", "-oServerAliveCountMax=1",
		"--", c.Alias, shellQuote(c.RemoteExecutable) + " peer-export",
	}
	if c.SSHConfigFile != "" {
		fd, err := syscall.Open(c.SSHConfigFile, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
		if err != nil {
			return Snapshot{}, ErrMalformed
		}
		file := os.NewFile(uintptr(fd), c.SSHConfigFile)
		info, err := file.Stat()
		_ = file.Close()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			return Snapshot{}, ErrMalformed
		}
		args = append([]string{"-F", c.SSHConfigFile}, args...)
	}
	b, err := json.Marshal(ExportRequest{Version: Version, Socket: c.RemoteSocket, Grant: c.Grant})
	if err != nil || len(b) > RequestLimit {
		return Snapshot{}, ErrMalformed
	}
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	runner := t.Runner
	if runner == nil {
		runner = ProcessRunner{}
	}
	result, err := runner.Run(ctx, executable, args, b)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, ErrTimeout) {
			return Snapshot{}, ErrTimeout
		}
		if errors.Is(err, ErrMalformed) {
			return Snapshot{}, ErrMalformed
		}
		diagnostic := strings.ToLower(string(result.Stderr))
		if strings.Contains(diagnostic, "host key verification failed") || strings.Contains(diagnostic, "remote host identification has changed") || strings.Contains(diagnostic, "host key is revoked") {
			return Snapshot{}, ErrHostKey
		}
		if strings.Contains(diagnostic, "permission denied") || strings.Contains(diagnostic, "no supported authentication methods") {
			return Snapshot{}, ErrAuthentication
		}
		return Snapshot{}, ErrUnavailable // Raw stderr may contain private paths.
	}
	if len(result.Stdout) > SnapshotLimit || len(result.Stderr) > 8192 {
		return Snapshot{}, ErrMalformed
	}
	var s Snapshot
	if err := decodeStrict(bytes.NewReader(result.Stdout), SnapshotLimit, &s); err != nil {
		return Snapshot{}, err
	}
	if err := s.validate(c.HostID); err != nil {
		return Snapshot{}, err
	}
	return project(s, c.Grant), nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// ProcessRunner bounds both streams and process lifetime. Exceeding a bound
// cancels the command immediately, rather than retaining arbitrary banners or
// terminal transcripts in memory. WaitDelay also bounds inherited open pipes.
type ProcessRunner struct{}

func (ProcessRunner) Run(ctx context.Context, executable string, args []string, input []byte) (ProcessOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = 200 * time.Millisecond
	cmd.Stdin = bytes.NewReader(input)
	out := &boundedWriter{limit: SnapshotLimit, cancel: cancel}
	errout := &boundedWriter{limit: 8192, cancel: cancel}
	cmd.Stdout, cmd.Stderr = out, errout
	err := cmd.Run()
	result := ProcessOutput{Stdout: out.b.Bytes(), Stderr: errout.b.Bytes()}
	if out.exceeded || errout.exceeded {
		return result, ErrMalformed
	}
	if ctx.Err() != nil {
		return result, ErrTimeout
	}
	return result, err
}

type boundedWriter struct {
	b        bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func (w *boundedWriter) Write(b []byte) (int, error) {
	if len(b) > w.limit-w.b.Len() {
		w.exceeded = true
		w.cancel()
		return 0, ErrMalformed
	}
	return w.b.Write(b)
}

package peers

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
)

func makeFIFO(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
}

func knownKey(t *testing.T, c Config) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for _, part := range [][]byte{[]byte("ssh-ed25519"), pub} {
		_ = binary.Write(&b, binary.BigEndian, uint32(len(part)))
		_, _ = b.Write(part)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(b.Bytes())
}
func writeKnownKey(t *testing.T, c *Config) string {
	t.Helper()
	key := knownKey(t, *c)
	fields := strings.Fields(key)
	c.Fingerprint, _ = keyFingerprint(fields[0], fields[1])
	if err := os.WriteFile(c.KnownHostsFile, []byte(c.Alias+" "+key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return key
}

type runnerFunc func(context.Context, string, []string, []byte) (ProcessOutput, error)

func (f runnerFunc) Run(ctx context.Context, p string, a []string, b []byte) (ProcessOutput, error) {
	return f(ctx, p, a, b)
}

func TestSSHSelectsOneExplicitKnownKeyAndPinsCommandAndScope(t *testing.T) {
	c, s := fixture(t)
	selected := writeKnownKey(t, &c)
	other := knownKey(t, c)
	if err := os.WriteFile(c.KnownHostsFile, []byte(c.Alias+" "+other+"\n"+c.Alias+" "+selected+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c.RemoteExecutable = "/private/quoted ' binary/$(touch injected)"
	var pinFile string
	tr := SSHTransport{Runner: runnerFunc(func(ctx context.Context, executable string, args []string, input []byte) (ProcessOutput, error) {
		if executable != "/usr/bin/ssh" {
			t.Fatal("unexpected SSH executable")
		}
		for _, required := range []string{"-T", "-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "-oGlobalKnownHostsFile=/dev/null", "-oHostKeyAlias=" + c.Alias, "-oUpdateHostKeys=no", "-oForwardAgent=no", "-oClearAllForwardings=yes", "-oRemoteCommand=none", "-oPermitLocalCommand=no", "-oControlMaster=no", "-oControlPath=none"} {
			found := false
			for _, arg := range args {
				if arg == required {
					found = true
				}
			}
			if !found {
				t.Fatal("SSH safety option missing", required)
			}
		}
		for _, arg := range args {
			if strings.HasPrefix(arg, "-oUserKnownHostsFile=") {
				pinFile = strings.TrimPrefix(arg, "-oUserKnownHostsFile=")
			}
		}
		b, err := os.ReadFile(pinFile)
		if err != nil || string(b) != c.Alias+" "+selected+"\n" || strings.Contains(string(b), other) {
			t.Fatal("SSH trusted more than selected identity", string(b), err)
		}
		info, _ := os.Stat(pinFile)
		if info.Mode().Perm() != 0600 {
			t.Fatal("pin file not private")
		}
		if args[len(args)-2] != c.Alias || args[len(args)-3] != "--" || args[len(args)-1] != shellQuote(c.RemoteExecutable)+" peer-export" {
			t.Fatal("host/command not constrained", args)
		}
		if strings.Contains(args[len(args)-1], c.RemoteSocket) || strings.Contains(args[len(args)-1], string(c.HostID)) {
			t.Fatal("scope interpolated into shell")
		}
		var r ExportRequest
		if decodeStrict(bytes.NewReader(input), RequestLimit, &r) != nil || r.Socket != c.RemoteSocket || r.Grant.HostID != c.HostID || len(r.Grant.Sessions) != 1 {
			t.Fatal("fixed stdin scope lost", r)
		}
		b, _ = json.Marshal(s)
		return ProcessOutput{Stdout: b}, nil
	})}
	got, err := tr.Fetch(context.Background(), c)
	if err != nil || len(got.Sessions) != 1 || got.Sessions[0].Execution.Runtime != (domain.RuntimeRef{}) {
		t.Fatal(got, err)
	}
	if _, err := os.Stat(pinFile); !os.IsNotExist(err) {
		t.Fatal("temporary trust file survived", err)
	}
}

func TestKnownHostAliasHashRevocationAndUnsafeTrust(t *testing.T) {
	c, _ := fixture(t)
	key := writeKnownKey(t, &c)
	salt := bytes.Repeat([]byte{3}, sha1.Size)
	mac := hmac.New(sha1.New, salt)
	_, _ = mac.Write([]byte(c.Alias))
	hashed := "|1|" + base64.StdEncoding.EncodeToString(salt) + "|" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for _, test := range []struct {
		line   string
		permit bool
	}{
		{c.Alias + " " + key, true}, {hashed + " " + key, true}, {"other," + c.Alias + " " + key, true}, {"* " + key, false}, {"other " + key, false}, {"@cert-authority " + c.Alias + " " + key, false}, {c.Alias + " " + key + "\n@revoked " + c.Alias + " " + key, false}, {c.Alias + " " + key + "\n@revoked * " + key, false}, {c.Alias + " ssh-ed25519 broken", false},
	} {
		if err := os.WriteFile(c.KnownHostsFile, []byte(test.line+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		pin, err := SelectKnownHost(c.KnownHostsFile, c.Alias, c.Fingerprint)
		if (err == nil) != test.permit || test.permit && pin != c.Alias+" "+key+"\n" {
			t.Fatal("trust selector", test.line, err)
		}
	}
	if err := os.WriteFile(c.KnownHostsFile, []byte(c.Alias+" "+key+"\n"), 0666); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(c.KnownHostsFile, 0666)
	if _, err := SelectKnownHost(c.KnownHostsFile, c.Alias, c.Fingerprint); !errors.Is(err, ErrHostKey) {
		t.Fatal("writable trust source accepted")
	}
	_ = os.Remove(c.KnownHostsFile)
	_ = os.Symlink(filepath.Join(t.TempDir(), "missing"), c.KnownHostsFile)
	if _, err := SelectKnownHost(c.KnownHostsFile, c.Alias, c.Fingerprint); !errors.Is(err, ErrHostKey) {
		t.Fatal("linked trust source accepted")
	}
}

func TestSSHFailsClosedOnKeyChangeHostCloneMalformedAndTimeout(t *testing.T) {
	c, s := fixture(t)
	writeKnownKey(t, &c)
	calls := 0
	tr := SSHTransport{Runner: runnerFunc(func(context.Context, string, []string, []byte) (ProcessOutput, error) {
		calls++
		return ProcessOutput{}, nil
	})}
	if err := os.WriteFile(c.KnownHostsFile, []byte(c.Alias+" "+knownKey(t, c)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Fetch(context.Background(), c); !errors.Is(err, ErrHostKey) || calls != 0 {
		t.Fatal("changed configured key contacted home", err, calls)
	}
	writeKnownKey(t, &c)
	valid, _ := json.Marshal(s)
	foreign := s
	foreign.HostID = domain.NewID("host")
	wrong, _ := json.Marshal(foreign)
	for _, test := range []struct {
		output    ProcessOutput
		err, want error
	}{
		{ProcessOutput{Stderr: []byte("Host key verification failed. private-credential-text")}, errors.New("exit 255"), ErrHostKey},
		{ProcessOutput{Stderr: []byte("Permission denied (publickey).")}, errors.New("exit 255"), ErrAuthentication},
		{ProcessOutput{Stderr: []byte("Connection refused")}, errors.New("exit 255"), ErrUnavailable},
		{ProcessOutput{Stdout: wrong}, nil, ErrIdentity},
		{ProcessOutput{Stdout: append(valid, []byte("{}")...)}, nil, ErrMalformed},
		{ProcessOutput{Stdout: []byte(`{"version":"a","version":"b"}`)}, nil, ErrMalformed},
		{ProcessOutput{Stdout: []byte("Welcome to host!\n" + string(valid))}, nil, ErrMalformed},
		{ProcessOutput{Stdout: bytes.Repeat([]byte{' '}, SnapshotLimit+1)}, nil, ErrMalformed},
		{ProcessOutput{}, ErrTimeout, ErrTimeout},
	} {
		tr.Runner = runnerFunc(func(context.Context, string, []string, []byte) (ProcessOutput, error) { return test.output, test.err })
		_, err := tr.Fetch(context.Background(), c)
		if !errors.Is(err, test.want) || strings.Contains(err.Error(), "private-credential-text") {
			t.Fatal("unsafe error/output accepted", err, test.want)
		}
	}
}

func TestConfigRejectsShellHostOptionsAndInvalidScopes(t *testing.T) {
	c, _ := fixture(t)
	for _, alias := range []string{"-oProxyCommand=oops", "user@host", "host; touch bad", "host\nother", "*"} {
		changed := c
		changed.Alias = alias
		if changed.Validate() == nil {
			t.Fatal("unsafe alias accepted", alias)
		}
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.RemoteExecutable = "keelmux" }, func(c *Config) { c.RemoteSocket = "/tmp/../home.sock" }, func(c *Config) { c.Fingerprint = "MD5:old" }, func(c *Config) { c.Grant.HostID = domain.NewID("host") }, func(c *Config) { c.Grant.Sessions = append(c.Grant.Sessions, c.Grant.Sessions[0]) }, func(c *Config) { c.RetainForSeconds = 0 }} {
		changed := c
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatal("invalid configuration accepted", changed)
		}
	}
}

func TestProcessRunnerBoundsTimeAndBothOutputStreams(t *testing.T) {
	for _, test := range []struct {
		name    string
		script  string
		timeout time.Duration
		want    error
	}{
		{"timeout", "exec sleep 30", 50 * time.Millisecond, ErrTimeout},
		{"stdout", "exec head -c 1100000 /dev/zero", FetchTimeout, ErrMalformed},
		{"stderr", "exec head -c 9000 /dev/zero >&2", FetchTimeout, ErrMalformed},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Only the timeout control requires cancellation before output. The
			// overflow controls retain the product deadline and the original
			// one-second wall bound, without racing a 50ms process-start budget.
			ctx, cancel := context.WithTimeout(context.Background(), test.timeout)
			start := time.Now()
			out, err := (ProcessRunner{}).Run(ctx, "/bin/sh", []string{"-c", test.script}, nil)
			cancel()
			elapsed := time.Since(start)
			if !errors.Is(err, test.want) || len(out.Stdout) > SnapshotLimit || len(out.Stderr) > 8192 || elapsed > time.Second {
				t.Fatal("process bounds failed", err, len(out.Stdout), len(out.Stderr), elapsed)
			}
		})
	}
}

func TestBoundedWriterExactLimitAndOverflowCancellation(t *testing.T) {
	for _, stream := range []struct {
		name  string
		limit int
	}{{"stdout", SnapshotLimit}, {"stderr", 8192}} {
		t.Run(stream.name, func(t *testing.T) {
			t.Run("exact-limit-then-one-byte", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				w := &boundedWriter{limit: stream.limit, cancel: cancel}
				allowed := bytes.Repeat([]byte{'x'}, stream.limit)
				n, err := w.Write(allowed)
				if n != stream.limit || err != nil || w.exceeded || ctx.Err() != nil || !bytes.Equal(w.b.Bytes(), allowed) {
					t.Fatal("exact limit must be retained without cancellation", n, err, w.exceeded, ctx.Err(), w.b.Len())
				}
				n, err = w.Write([]byte{'y'})
				if n != 0 || !errors.Is(err, ErrMalformed) || !w.exceeded || !errors.Is(ctx.Err(), context.Canceled) || !bytes.Equal(w.b.Bytes(), allowed) {
					t.Fatal("limit+1 must cancel immediately without retaining excess", n, err, w.exceeded, ctx.Err(), w.b.Len())
				}
			})
			t.Run("one-oversized-write", func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				w := &boundedWriter{limit: stream.limit, cancel: cancel}
				n, err := w.Write(bytes.Repeat([]byte{'x'}, stream.limit+1))
				if n != 0 || !errors.Is(err, ErrMalformed) || !w.exceeded || !errors.Is(ctx.Err(), context.Canceled) || w.b.Len() != 0 {
					t.Fatal("oversized first write must cancel without retaining output", n, err, w.exceeded, ctx.Err(), w.b.Len())
				}
			})
		})
	}
}

func TestShellQuoteCannotRunWorkspaceText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "injected")
	malicious := "/missing/' ; touch " + path + " ; echo '"
	cmd := exec.Command("/bin/sh", "-c", shellQuote(malicious)+" peer-export")
	_ = cmd.Run()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("command injection executed", err)
	}
}

func TestSelectedKnownHostnameCanDifferFromSSHConfigAlias(t *testing.T) {
	c, s := fixture(t)
	key := writeKnownKey(t, &c)
	c.KnownHost = "[server.example]:2222"
	if err := os.WriteFile(c.KnownHostsFile, []byte(c.KnownHost+" "+key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tr := SSHTransport{Runner: runnerFunc(func(ctx context.Context, p string, args []string, b []byte) (ProcessOutput, error) {
		for _, arg := range args {
			if strings.HasPrefix(arg, "-oUserKnownHostsFile=") {
				pin, err := os.ReadFile(strings.TrimPrefix(arg, "-oUserKnownHostsFile="))
				if err != nil || string(pin) != c.Alias+" "+key+"\n" {
					t.Fatal("selected hostname not pinned to alias", string(pin), err)
				}
			}
		}
		out, _ := json.Marshal(s)
		return ProcessOutput{Stdout: out}, nil
	})}
	if _, err := tr.Fetch(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

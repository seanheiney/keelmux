package peers

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// startReadSSH starts exactly one pinned owner-managed exporter. Its lifetime
// belongs to ReadSource, never to a control/handshake timeout context.
func startReadSSH(ctx context.Context, c Config, selection ReadSelection) (io.ReadWriteCloser, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !cleanPath(selection.PolicyPath, 4096) {
		return nil, ErrMalformed
	}
	name := c.KnownHost
	if name == "" {
		name = c.Alias
	}
	key, err := SelectKnownHost(c.KnownHostsFile, name, c.Fingerprint)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(key)
	if len(fields) < 3 {
		return nil, ErrHostKey
	}
	algorithm := fields[1]
	if algorithm == "ssh-rsa" {
		algorithm = "rsa-sha2-512,rsa-sha2-256"
	}
	pin, err := os.CreateTemp("", "keelmux-read-key-*")
	if err != nil {
		return nil, ErrHostKey
	}
	remove := func() { _ = os.Remove(pin.Name()) }
	if _, err = pin.WriteString(c.Alias + " " + fields[1] + " " + fields[2] + "\n"); err == nil {
		err = pin.Close()
	} else {
		_ = pin.Close()
	}
	if err != nil {
		remove()
		return nil, ErrHostKey
	}
	args := []string{
		"-T", "-oBatchMode=yes", "-oStrictHostKeyChecking=yes",
		"-oUserKnownHostsFile=" + pin.Name(), "-oGlobalKnownHostsFile=/dev/null",
		"-oHostKeyAlias=" + c.Alias, "-oUpdateHostKeys=no", "-oVerifyHostKeyDNS=no", "-oHostKeyAlgorithms=" + algorithm,
		"-oForwardAgent=no", "-oForwardX11=no", "-oClearAllForwardings=yes",
		"-oPermitLocalCommand=no", "-oRemoteCommand=none", "-oRequestTTY=no",
		"-oControlMaster=no", "-oControlPath=none", "-oCanonicalizeHostname=no",
		"-oConnectionAttempts=1", "-oConnectTimeout=2", "-oServerAliveInterval=1", "-oServerAliveCountMax=1",
		"--", c.Alias, shellQuote(c.RemoteExecutable) + " peer-read-bridge " + shellQuote(selection.PolicyPath),
	}
	if c.SSHConfigFile != "" {
		if !filepath.IsAbs(c.SSHConfigFile) {
			remove()
			return nil, ErrMalformed
		}
		fd, e := syscall.Open(c.SSHConfigFile, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
		if e != nil {
			remove()
			return nil, ErrMalformed
		}
		file := os.NewFile(uintptr(fd), c.SSHConfigFile)
		info, e := file.Stat()
		_ = file.Close()
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			remove()
			return nil, ErrMalformed
		}
		args = append([]string{"-F", c.SSHConfigFile}, args...)
	}
	lifetime, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(lifetime, "/usr/bin/ssh", args...)
	cmd.WaitDelay = 200 * time.Millisecond
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		remove()
		return nil, ErrUnavailable
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		cancel()
		remove()
		return nil, ErrUnavailable
	}
	diagnostics := &readSSHStderr{cancel: cancel}
	cmd.Stderr = diagnostics
	if err = cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		cancel()
		remove()
		return nil, ErrUnavailable
	}
	conn := &readSSHConnection{input: in, output: out, cancel: cancel, done: make(chan struct{}), remove: remove, diagnostics: diagnostics}
	go func() { _ = cmd.Wait(); close(conn.done) }()
	return conn, nil
}

// No raw stderr is retained or forwarded; arbitrary banner bytes have a hard
// lifetime bound and terminate this owned process on overflow.
type readSSHStderr struct {
	mu       sync.Mutex
	bytes    int
	exceeded bool
	cancel   context.CancelFunc
}

func (w *readSSHStderr) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > 65536-w.bytes {
		w.exceeded = true
		w.cancel()
		return 0, ErrMalformed
	}
	w.bytes += len(p)
	return len(p), nil
}
func (w *readSSHStderr) overflow() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.exceeded }

type readSSHConnection struct {
	input       io.WriteCloser
	output      io.ReadCloser
	cancel      context.CancelFunc
	done        chan struct{}
	remove      func()
	diagnostics *readSSHStderr
	once        sync.Once
	closeErr    error
}

func (c *readSSHConnection) Read(p []byte) (int, error) {
	n, err := c.output.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = ErrUnavailable
	}
	if c.diagnostics.overflow() {
		return 0, ErrMalformed
	}
	return n, err
}
func (c *readSSHConnection) Write(p []byte) (int, error) {
	n, err := c.input.Write(p)
	if err != nil {
		err = ErrUnavailable
	}
	return n, err
}
func (c *readSSHConnection) SetReadDeadline(t time.Time) error {
	if pipe, ok := c.output.(interface{ SetReadDeadline(time.Time) error }); ok {
		return pipe.SetReadDeadline(t)
	}
	return ErrUnavailable
}
func (c *readSSHConnection) SetWriteDeadline(t time.Time) error {
	if pipe, ok := c.input.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return pipe.SetWriteDeadline(t)
	}
	return ErrUnavailable
}
func (c *readSSHConnection) Close() error {
	c.once.Do(func() {
		_ = c.input.Close()
		_ = c.output.Close()
		c.cancel()
		select {
		case <-c.done:
		case <-time.After(time.Second):
			c.closeErr = errors.New("owned SSH cleanup unconfirmed")
		}
		c.remove()
	})
	return c.closeErr
}

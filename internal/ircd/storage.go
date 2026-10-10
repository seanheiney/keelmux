package ircd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/seanheiney/keelmux/internal/config"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func New(dir string) (*Manager, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || strings.ContainsAny(dir, "\x00\r\n") || len(dir) > 2048 {
		return nil, ErrRepair
	}
	if _, e := os.Lstat(dir); e == nil {
		if config.EnsurePrivateDirectory(dir) != nil {
			return nil, ErrRepair
		}
	} else if !os.IsNotExist(e) {
		return nil, ErrRepair
	}
	return &Manager{dir: dir}, nil
}
func (m *Manager) lock(ctx context.Context, create bool) (func(), error) {
	if create {
		if config.EnsurePrivateDirectory(m.dir) != nil {
			return nil, ErrRepair
		}
	} else {
		if _, e := os.Lstat(m.dir); os.IsNotExist(e) {
			return nil, ErrNotInitialized
		}
		if config.EnsurePrivateDirectory(m.dir) != nil {
			return nil, ErrRepair
		}
	}
	flags := syscall.O_RDWR | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
	if create {
		flags |= syscall.O_CREAT
	}
	fd, e := syscall.Open(filepath.Join(m.dir, "manager.lock"), flags, 0600)
	if e != nil {
		return nil, ErrRepair
	}
	f := os.NewFile(uintptr(fd), "managed IRC lock")
	info, e := f.Stat()
	if e != nil || !privateInfo(info, 0600) {
		f.Close()
		return nil, ErrRepair
	}
	for {
		e := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			return func() { syscall.Flock(fd, syscall.LOCK_UN); f.Close() }, nil
		}
		if !errors.Is(e, syscall.EWOULDBLOCK) && !errors.Is(e, syscall.EAGAIN) {
			f.Close()
			return nil, ErrRepair
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func privateInfo(i os.FileInfo, mode os.FileMode) bool {
	if i == nil {
		return false
	}
	s, ok := i.Sys().(*syscall.Stat_t)
	return ok && i.Mode().IsRegular() && i.Mode().Perm() == mode && s.Uid == uint32(os.Geteuid())
}
func readFile(path string, bound int64, mode os.FileMode) ([]byte, error) {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	i, e := f.Stat()
	if e != nil || !privateInfo(i, mode) || i.Size() > bound {
		return nil, ErrRepair
	}
	b, e := io.ReadAll(io.LimitReader(f, bound+1))
	if e != nil || int64(len(b)) > bound {
		return nil, ErrRepair
	}
	return b, nil
}
func readJSON(path string, out any) error {
	b, e := readFile(path, 65536, 0600)
	if e != nil {
		return e
	}
	if json.Unmarshal(b, out) != nil {
		return ErrRepair
	}
	canonical, e := json.Marshal(out)
	if e != nil || !bytes.Equal(b, canonical) {
		return ErrRepair
	}
	return nil
}
func publish(path string, b []byte, mode os.FileMode, replace bool) error {
	if replace {
		if _, e := readFile(path, 1<<20, mode); e != nil {
			return ErrRepair
		}
	}
	tmp, e := os.CreateTemp(filepath.Dir(path), ".write-")
	if e != nil {
		return ErrRepair
	}
	defer os.Remove(tmp.Name())
	if tmp.Chmod(mode) != nil {
		tmp.Close()
		return ErrRepair
	}
	if _, e = tmp.Write(b); e != nil || tmp.Sync() != nil {
		tmp.Close()
		return ErrRepair
	}
	if tmp.Close() != nil {
		return ErrRepair
	}
	if replace {
		e = os.Rename(tmp.Name(), path)
	} else {
		e = os.Link(tmp.Name(), path)
	}
	if e != nil {
		return ErrRepair
	}
	return syncDir(filepath.Dir(path))
}
func writeJSON(path string, v any, replace bool) error {
	b, e := json.Marshal(v)
	if e != nil {
		return ErrRepair
	}
	return publish(path, b, 0600, replace)
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return ErrRepair
	}
	defer f.Close()
	if f.Sync() != nil {
		return ErrRepair
	}
	return nil
}
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", ErrUnavailable
	}
	return hex.EncodeToString(b), nil
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Commands inherit no native login, cloud token or Ergo environment override.
func (m *Manager) command(ctx context.Context, binary string, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	argv := append([]string{"-c", `umask 077; exec "$@"`, "keelmux-private-command", binary}, args...)
	cmd := exec.CommandContext(bounded, "/bin/sh", argv...)
	cmd.Dir = m.dir
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + m.dir, "TMPDIR=" + m.dir, "LC_ALL=C"}
	out := &boundedOutput{}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil || out.exceeded {
		return nil, ErrUnavailable
	}
	return out.data, nil
}

type boundedOutput struct {
	data     []byte
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.data)+n > 128<<10 {
		b.exceeded = true
		return n, nil
	}
	b.data = append(b.data, p...)
	return n, nil
}

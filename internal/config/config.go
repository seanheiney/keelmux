// Package config owns private per-user paths and single-daemon locking.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/seanheiney/keelmux/internal/domain"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

type Paths struct{ Dir, Control, Tmux, DB, Place string }

func Resolve(dir string) (Paths, error) {
	if dir == "" {
		dir = os.Getenv("KEELMUX_STATE_DIR")
	}
	if dir == "" {
		dir = os.Getenv("BP_STATE_DIR")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, err
		}
		// Keep a prior installation's authority in its original directory.
		// Ensure validates it; an invalid legacy path must never cause a
		// second registry to be created under the new product name.
		legacy := filepath.Join(home, ".local", "state", "agent-backplane")
		if _, err := os.Lstat(legacy); err == nil {
			dir = legacy
		} else if !os.IsNotExist(err) {
			return Paths{}, err
		} else {
			dir = filepath.Join(home, ".local", "state", "keelmux")
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Paths{}, err
	}
	p := Paths{Dir: abs, Control: filepath.Join(abs, "control.sock"), Tmux: filepath.Join(abs, "tmux.sock"), DB: filepath.Join(abs, "registry.sqlite"), Place: filepath.Join(abs, "place.json")}
	if len(p.Control) > 100 || len(p.Tmux) > 100 {
		return Paths{}, errors.New("state path too long for Unix sockets; choose shorter --state")
	}
	return p, nil
}
func (p Paths) Ensure() error {
	return EnsurePrivateDirectory(p.Dir)
}
func privateFile(path string, flags int) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		_ = f.Close()
		return nil, errors.New("state file must be owned, regular and private (0600)")
	}
	return f, nil
}

// OpenPrivateLog refuses links, special files and unsafe existing permissions.
func OpenPrivateLog(path string) (*os.File, error) {
	return privateFile(path, syscall.O_CREAT|syscall.O_WRONLY|syscall.O_APPEND)
}

// HostID accepts only the product-generated host_<32 lowercase hex> identity.
// Malformed legacy or interrupted writes require explicit repair; they remain
// untouched rather than silently assigning a different execution authority.
func (p Paths) HostID() (domain.ID, error) {
	path := filepath.Join(p.Dir, "host-id")
	f, err := privateFile(path, syscall.O_RDONLY)
	if err == nil {
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, 257))
		if err != nil {
			return "", err
		}
		if !validHostID(b) {
			return "", errors.New("host identity must be host_ followed by 32 lowercase hex digits; repair required")
		}
		if err := syncDirectory(p.Dir); err != nil {
			return "", err
		}
		return domain.ID(b), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	id := domain.NewID("host")
	f, err = privateFile(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL)
	if errors.Is(err, os.ErrExist) {
		return p.HostID()
	}
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(string(id))
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = syncDirectory(p.Dir)
	}
	return id, err
}
func validHostID(value []byte) bool {
	if len(value) != len("host_")+32 || !bytes.HasPrefix(value, []byte("host_")) {
		return false
	}
	for _, b := range value[len("host_"):] {
		if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')) {
			return false
		}
	}
	return true
}
func (p Paths) Lock() (*os.File, error) {
	f, err := privateFile(filepath.Join(p.Dir, "daemon.lock"), syscall.O_CREAT|syscall.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("daemon already owns state: %w", err)
	}
	return f, nil
}
func (p Paths) Listen() (net.Listener, error) {
	if i, err := os.Lstat(p.Control); err == nil {
		if i.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("control socket path contains another file")
		}
		if err = os.Remove(p.Control); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	l, err := net.Listen("unix", p.Control)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(p.Control, 0600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

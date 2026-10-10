// Package config owns private per-user paths and single-daemon locking.
package config

import (
	"errors"
	"fmt"
	"github.com/seanheiney/agent-backplane/internal/domain"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

type Paths struct{ Dir, Control, Tmux, DB, Place string }

func Resolve(dir string) (Paths, error) {
	if dir == "" {
		dir = os.Getenv("BP_STATE_DIR")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, err
		}
		dir = filepath.Join(home, ".local", "state", "agent-backplane")
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
	if err := os.MkdirAll(p.Dir, 0700); err != nil {
		return err
	}
	i, err := os.Lstat(p.Dir)
	if err != nil {
		return err
	}
	if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
		return errors.New("state must be a real private directory")
	}
	if st, ok := i.Sys().(*syscall.Stat_t); !ok || st.Uid != uint32(os.Getuid()) {
		return errors.New("state directory owner differs from current user")
	}
	return os.Chmod(p.Dir, 0700)
}
func (p Paths) HostID() (domain.ID, error) {
	path := filepath.Join(p.Dir, "host-id")
	if b, err := os.ReadFile(path); err == nil {
		if len(b) == 0 {
			return "", errors.New("empty host identity")
		}
		return domain.ID(b), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	id := domain.NewID("host")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
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
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	return id, err
}
func (p Paths) Lock() (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(p.Dir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
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

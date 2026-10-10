package clientstate

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// Lock guards one client-local place file for the switcher's entire lifetime.
// The lock inode is retained after Close so concurrent openers cannot lock
// separate replacement files. Kernel ownership disappears when a client dies.
type Lock struct {
	file *os.File
	once sync.Once
	err  error
}

func AcquireLock(statePath string) (*Lock, error) {
	if statePath == "" {
		return nil, errors.New("client place path is required")
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		return nil, err
	}
	fd, err := unix.Open(statePath+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), statePath+".lock")
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.New("client place is already open in another switcher; close it or use an independent client state path")
		}
		return nil, err
	}
	return &Lock{file: f}, nil
}

func (l *Lock) Close() error {
	l.once.Do(func() { l.err = l.file.Close() })
	return l.err
}

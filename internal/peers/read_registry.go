package peers

import (
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type readRegistryBinding struct {
	path            string
	directory, lock os.FileInfo
}

// OpenReadRegistry opens existing enrollment state only. It does not inherit
// inventory enrollment's directory/lock creation or recovery behavior.
func OpenReadRegistry(path string) (*Registry, error) {
	if !cleanPath(path, 4096) {
		return nil, ErrMalformed
	}
	d, err := openDirectory(path)
	if err != nil {
		return nil, ErrConflict
	}
	r := &Registry{directory: d, transport: SSHTransport{}, now: func() time.Time { return time.Now().UTC() }}
	var binding readRegistryBinding
	if err = readRegistryTransaction(r, &binding, func(rs *records) error {
		if len(rs.Peers) == 0 {
			return ErrGrant
		}
		return nil
	}); err != nil {
		d.Close()
		return nil, err
	}
	return r, nil
}

func readRegistryTransaction(r *Registry, binding *readRegistryBinding, fn func(*records) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !ownedPrivate(r.directory, true) {
		return ErrConflict
	}
	path := r.directory.Name()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return ErrConflict
	}
	dir, err := r.directory.Stat()
	if err != nil {
		return ErrConflict
	}
	visible, err := os.Lstat(path)
	if err != nil || !os.SameFile(dir, visible) {
		return ErrConflict
	}
	lock, err := r.file("peers.lock", unix.O_RDWR)
	if err != nil {
		return ErrConflict
	}
	defer lock.Close()
	lockInfo, err := lock.Stat()
	if err != nil {
		return ErrConflict
	}
	if binding.path == "" {
		binding.path, binding.directory, binding.lock = path, dir, lockInfo
	} else if binding.path != path || !os.SameFile(binding.directory, dir) || !os.SameFile(binding.lock, lockInfo) {
		return ErrConflict
	}
	check := func() error {
		visible, err := os.Lstat(path)
		if err != nil || !os.SameFile(binding.directory, visible) || !ownedPrivate(r.directory, true) {
			return ErrConflict
		}
		visible, err = os.Lstat(filepath.Join(path, "peers.lock"))
		if err != nil || !os.SameFile(binding.lock, visible) || !ownedPrivate(lock, false) {
			return ErrConflict
		}
		canonical, err = filepath.EvalSymlinks(path)
		if err != nil || canonical != path {
			return ErrConflict
		}
		return nil
	}
	if err = check(); err != nil {
		return err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return ErrConflict
	}
	rs, err := r.load()
	if err != nil {
		return ErrConflict
	}
	if err = check(); err != nil {
		return err
	}
	if err = fn(&rs); err != nil {
		return err
	}
	return check()
}

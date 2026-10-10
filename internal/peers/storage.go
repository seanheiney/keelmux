package peers

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"syscall"

	"github.com/seanheiney/keelmux/internal/domain"
	"golang.org/x/sys/unix"
)

const registryLimit = 18 << 20

func ownedPrivate(f *os.File, dir bool) bool {
	info, err := f.Stat()
	if err != nil || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return false
	}
	if dir {
		return info.IsDir() && info.Mode().Perm() == 0700
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600
}

func openDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("private peer directory unavailable")
	}
	f := os.NewFile(uintptr(fd), path)
	if !ownedPrivate(f, true) {
		_ = f.Close()
		return nil, errors.New("peer directory must be owned and private (0700)")
	}
	return f, nil
}

func (r *Registry) file(name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(r.directory.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if !ownedPrivate(f, false) {
		_ = f.Close()
		return nil, errors.New("peer state file must be owned, regular and private (0600)")
	}
	return f, nil
}

func (r *Registry) transaction(write bool, fn func(*records) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("peer registry is closed")
	}
	if !ownedPrivate(r.directory, true) {
		return errors.New("peer directory permissions changed; repair required")
	}
	lock, err := r.file("peers.lock", unix.O_CREAT|unix.O_RDWR)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("peer registry transaction busy; retry the read or reviewed operation")
	}
	rs, err := r.load()
	if err != nil {
		return err
	}
	if err := fn(&rs); err != nil {
		return err
	}
	if !write {
		return nil
	}
	sort.Slice(rs.Peers, func(i, j int) bool { return rs.Peers[i].Config.HostID < rs.Peers[j].Config.HostID })
	return r.save(rs)
}

func (r *Registry) load() (records, error) {
	rs := records{Version: Version, Peers: []record{}}
	f, err := r.file("peers.json", unix.O_RDONLY)
	if os.IsNotExist(err) {
		return rs, nil
	}
	if err != nil {
		return rs, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > registryLimit {
		return rs, errors.New("private peer registry exceeds storage bounds")
	}
	if err := decodeStrict(f, registryLimit, &rs); err != nil {
		return rs, errors.New("peer registry malformed; preserve it for explicit repair")
	}
	if rs.Version != Version || len(rs.Peers) > MaxPeers {
		return rs, ErrMalformed
	}
	hosts, aliases := map[domain.ID]bool{}, map[string]bool{}
	for _, p := range rs.Peers {
		if p.Config.Validate() != nil || p.Revision == 0 || hosts[p.Config.HostID] || aliases[p.Config.Alias] || !validReason(p.LastError) {
			return rs, ErrMalformed
		}
		hosts[p.Config.HostID], aliases[p.Config.Alias] = true, true
		if p.Snapshot == nil {
			if !p.ReceivedAt.IsZero() && !p.Revoked {
				return rs, ErrMalformed
			}
			continue
		}
		if p.Revoked || p.ReceivedAt.IsZero() || p.Snapshot.validate(p.Config.HostID) != nil {
			return rs, ErrMalformed
		}
		b, err := json.Marshal(p.Snapshot)
		if err != nil || len(b) > SnapshotLimit {
			return rs, ErrMalformed
		}
		allowed := map[domain.ID]bool{}
		for _, id := range p.Config.Grant.Sessions {
			allowed[id] = true
		}
		for _, v := range p.Snapshot.Sessions {
			if !allowed[v.ID] || v.Execution.Runtime != (domain.RuntimeRef{}) || v.Capabilities.NativeAttach || v.Capabilities.ReadOnlyAttach || v.Capabilities.Stop || v.Capabilities.SemanticPrompt || v.Capabilities.NativeResume {
				return rs, ErrMalformed
			}
		}
	}
	return rs, nil
}

func validReason(s string) bool {
	switch s {
	case "", "grant_changed", "revoked", "host_key_rejected", "host_identity_changed", "malformed_inventory", "authentication_unavailable", "timeout", "stale_advertisement", "transport_unavailable":
		return true
	}
	return false
}

func (r *Registry) save(rs records) error {
	b, err := json.Marshal(rs)
	if err != nil || len(b) > registryLimit {
		return errors.New("peer registry exceeds storage bounds")
	}
	name := ".pending-" + string(domain.NewID("write"))
	f, err := r.file(name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(r.directory.Fd()), name, 0)
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = unix.Renameat(int(r.directory.Fd()), name, int(r.directory.Fd()), "peers.json"); err != nil {
		return err
	}
	if err = r.directory.Sync(); err != nil {
		return errors.New("peer registry persistence unconfirmed; read back the reviewed operation")
	}
	return nil
}

// ReadConfig requires the reviewed setup file to be private and bounded. It
// contains nonsecret native locations and identity pins, never credentials.
func ReadConfig(path string) (Config, error) {
	var c Config
	if !cleanPath(path, 4096) {
		return c, ErrMalformed
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return c, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	if !ownedPrivate(f, false) {
		return c, errors.New("peer configuration must be owned, regular and private (0600)")
	}
	if err := decodeStrict(io.LimitReader(f, RequestLimit+1), RequestLimit, &c); err != nil {
		return c, err
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return normalize(c), nil
}

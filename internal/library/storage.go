package library

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const catalogLimit = 4 << 20

type records struct {
	Schema   int                `json:"schema"`
	Revision uint64             `json:"revision"`
	Packages map[string]Package `json:"packages"`
	Archived map[string]Package `json:"archived"`
	Profiles map[string]Profile `json:"profiles"`
}
type move struct {
	From    string  `json:"from"`
	To      string  `json:"to"`
	Package Package `json:"package"`
}
type intent struct {
	Schema int     `json:"schema"`
	Before string  `json:"before"`
	After  records `json:"after"`
	Move   *move   `json:"move,omitempty"`
}
type store struct {
	root  *os.File
	state *os.File
	lock  *os.File
}

func (s *store) close() {
	if s.lock != nil {
		s.lock.Close()
	}
	s.root.Close()
	s.state.Close()
}

func private(info os.FileInfo, dir bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return false
	}
	if dir {
		return info.IsDir() && info.Mode().Perm() == 0700
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && st.Nlink == 1
}
func ownedSource(info os.FileInfo, dir bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0022 == 0 && (dir && info.IsDir() || !dir && info.Mode().IsRegular() && st.Nlink == 1)
}
func absolute(path string) bool {
	return path != "" && len(path) <= 4096 && filepath.IsAbs(path) && filepath.Clean(path) == path && validText(path, 4096)
}
func openAt(parent *os.File, name string, dir, create, priv bool) (*os.File, error) {
	if strings.Contains(name, "/") || name == "." || name == ".." || name == "" {
		return nil, ErrUnsafe
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if dir {
		flags |= unix.O_DIRECTORY
		if create {
			err := unix.Mkdirat(int(parent.Fd()), name, 0700)
			if err != nil && !errors.Is(err, unix.EEXIST) {
				return nil, err
			}
			if err == nil {
				if err = parent.Sync(); err != nil {
					return nil, err
				}
			}
		}
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name))
	info, err := f.Stat()
	if err != nil || priv && !private(info, dir) || !priv && !ownedSource(info, dir) {
		f.Close()
		return nil, ErrUnsafe
	}
	return f, nil
}

// Walk absolute components without following any symlink, including ancestors.
// Existing ancestors may have ordinary permissions; the selected root is private.
func openPath(path string, create, priv bool) (*os.File, error) {
	if !absolute(path) || path == "/" {
		return nil, ErrUnsafe
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, name := range parts {
		if create {
			err = unix.Mkdirat(int(f.Fd()), name, 0700)
			if err != nil && !errors.Is(err, unix.EEXIST) {
				f.Close()
				return nil, err
			}
			if err == nil {
				if err = f.Sync(); err != nil {
					f.Close()
					return nil, err
				}
			}
		}
		fd, err = unix.Openat(int(f.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		nextPath := filepath.Join(f.Name(), name)
		f.Close()
		if err != nil {
			return nil, err
		}
		f = os.NewFile(uintptr(fd), nextPath)
		if i == len(parts)-1 {
			info, e := f.Stat()
			if e != nil || priv && !private(info, true) || !priv && !ownedSource(info, true) {
				f.Close()
				return nil, ErrUnsafe
			}
		}
	}
	return f, nil
}
func sameAt(parent *os.File, name string, f *os.File) bool {
	var actual, entry unix.Stat_t
	if unix.Fstat(int(f.Fd()), &actual) != nil || unix.Fstatat(int(parent.Fd()), name, &entry, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return false
	}
	return actual.Dev == entry.Dev && actual.Ino == entry.Ino && actual.Mode == entry.Mode && actual.Uid == entry.Uid
}
func samePath(f *os.File) bool {
	current, err := openPath(f.Name(), false, true)
	if err != nil {
		return false
	}
	defer current.Close()
	a, err := f.Stat()
	if err != nil {
		return false
	}
	b, err := current.Stat()
	return err == nil && os.SameFile(a, b)
}
func (s *store) anchored() bool {
	return samePath(s.state) && sameAt(s.state, "library", s.root) && func() bool {
		i, e := s.root.Stat()
		return e == nil && private(i, true) && (s.lock == nil || sameAt(s.root, "library.lock", s.lock))
	}()
}
func (r *registry) openStore(ctx context.Context, write bool) (*store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, err := openPath(r.stateDir, write, true)
	if err != nil {
		return nil, err
	}
	root, err := openAt(state, "library", true, write, true)
	if err != nil {
		state.Close()
		return nil, err
	}
	s := &store{state: state, root: root}
	// A published catalog or durable intent always has a retained lock inode.
	// Do not silently replace a missing lock for existing state.
	for _, name := range []string{"catalog.json", "pending.json"} {
		var info unix.Stat_t
		if unix.Fstatat(int(root.Fd()), name, &info, unix.AT_SYMLINK_NOFOLLOW) == nil {
			var lockInfo unix.Stat_t
			if unix.Fstatat(int(root.Fd()), "library.lock", &lockInfo, unix.AT_SYMLINK_NOFOLLOW) != nil {
				s.close()
				return nil, ErrCorrupt
			}
		}
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if write {
		flags = unix.O_RDWR | unix.O_CREAT | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	}
	fd, err := unix.Openat(int(root.Fd()), "library.lock", flags, 0600)
	if os.IsNotExist(err) && !write {
		return s, nil
	}
	if err != nil {
		s.close()
		return nil, err
	}
	s.lock = os.NewFile(uintptr(fd), filepath.Join(root.Name(), "library.lock"))
	info, e := s.lock.Stat()
	if e != nil || !private(info, false) {
		s.close()
		return nil, ErrUnsafe
	}
	kind := unix.LOCK_SH
	if write {
		kind = unix.LOCK_EX
	}
	for {
		err = unix.Flock(fd, kind|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			s.close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			s.close()
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !s.anchored() || !sameAt(root, "library.lock", s.lock) {
		s.close()
		return nil, ErrUnsafe
	}
	return s, nil
}
func readPrivate(parent *os.File, name string, limit int) ([]byte, error) {
	f, err := openAt(parent, name, false, false, true)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || before.Size() > int64(limit) {
		return nil, ErrCorrupt
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil, ErrCorrupt
	}
	after, err := f.Stat()
	if err != nil || !private(after, false) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || !sameAt(parent, name, f) {
		return nil, ErrUnsafe
	}
	return raw, nil
}
func emptyRecords() records {
	return records{Schema: SchemaVersion, Packages: map[string]Package{}, Archived: map[string]Package{}, Profiles: map[string]Profile{"default": {ID: "default", Name: "Default", Revision: 1, Packages: map[string]Selection{}}}}
}
func loadRecords(s *store) (records, error) {
	rs := emptyRecords()
	raw, err := readPrivate(s.root, "catalog.json", catalogLimit)
	if os.IsNotExist(err) {
		return rs, nil
	}
	if err != nil {
		return rs, err
	}
	if strictJSON(raw, &rs, catalogLimit) != nil || validateRecords(rs) != nil {
		return rs, ErrCorrupt
	}
	return rs, nil
}
func validateRecords(rs records) error {
	if rs.Schema != SchemaVersion || rs.Revision == 0 || rs.Packages == nil || rs.Archived == nil || rs.Profiles == nil || len(rs.Packages)+len(rs.Archived) > 256 || len(rs.Profiles) > 64 {
		return ErrCorrupt
	}
	if _, ok := rs.Profiles["default"]; !ok {
		return ErrCorrupt
	}
	for digest, p := range rs.Packages {
		if digest != p.Digest || !digestPattern.MatchString(digest) || validateManifest(p.Manifest) != nil || packDigest(normalizeManifest(p.Manifest)) != digest {
			return ErrCorrupt
		}
		if _, ok := rs.Archived[digest]; ok {
			return ErrCorrupt
		}
	}
	for digest, p := range rs.Archived {
		if digest != p.Digest || !digestPattern.MatchString(digest) || validateManifest(p.Manifest) != nil || packDigest(normalizeManifest(p.Manifest)) != digest {
			return ErrCorrupt
		}
	}
	for id, p := range rs.Profiles {
		if id != p.ID || !validID(id) || p.Name == "" || !validText(p.Name, 128) || p.Revision == 0 || p.Packages == nil || len(p.Packages) > 256 {
			return ErrCorrupt
		}
		for id, sel := range p.Packages {
			pack, active := rs.Packages[sel.Digest]
			if !active {
				pack = rs.Archived[sel.Digest]
			}
			if !validID(id) || pack.Manifest.ID != id || sel.Enabled && !active || len(sel.Components) > 512 {
				return ErrCorrupt
			}
			for c := range sel.Components {
				if !componentExists(pack.Manifest, c) {
					return ErrCorrupt
				}
			}
		}
	}
	return nil
}
func recordsHash(rs records) string { raw, _ := json.Marshal(rs); return hashBytes(raw) }
func fileIdentity(parent *os.File, name string) (string, error) {
	raw, err := readPrivate(parent, name, catalogLimit)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return hashBytes(raw), nil
}
func atomicFile(parent *os.File, name string, value any, expected string) error {
	// Bind publication to the exact existing bytes and inode, or to absence.
	identity, err := fileIdentity(parent, name)
	if err != nil {
		return err
	}
	if identity != expected {
		return ErrCorrupt
	}
	old, err := openAt(parent, name, false, false, true)
	if err == nil {
		defer old.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > catalogLimit {
		return ErrCorrupt
	}
	nameTemp, err := randomName(".write-")
	if err != nil {
		return err
	}
	fd, err := unix.Openat(int(parent.Fd()), nameTemp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	temp := os.NewFile(uintptr(fd), filepath.Join(parent.Name(), nameTemp))
	defer unix.Unlinkat(int(parent.Fd()), nameTemp, 0)
	_, err = temp.Write(raw)
	if err == nil {
		err = temp.Sync()
	}
	if e := temp.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	// The temp path is anchored to the same still-selected private directory.
	if !samePath(parent) {
		return ErrUnsafe
	}
	identity, err = fileIdentity(parent, name)
	if err != nil {
		return err
	}
	if identity != expected || old != nil && !sameAt(parent, name, old) {
		return ErrCorrupt
	}
	if err = unix.Renameat(int(parent.Fd()), nameTemp, int(parent.Fd()), name); err != nil {
		return err
	}
	return parent.Sync()
}
func existingIntent(s *store) (*intent, error) {
	raw, err := readPrivate(s.root, "pending.json", catalogLimit)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var in intent
	if strictJSON(raw, &in, catalogLimit) != nil || in.Schema != SchemaVersion || !digestPattern.MatchString(in.Before) || validateRecords(in.After) != nil {
		return nil, ErrCorrupt
	}
	if in.Move != nil {
		m := in.Move
		if m.Package.Digest == "" || validateManifest(m.Package.Manifest) != nil || packDigest(normalizeManifest(m.Package.Manifest)) != m.Package.Digest {
			return nil, ErrCorrupt
		}
		importMove := strings.HasPrefix(m.From, ".stage-") && !strings.Contains(m.From, "/") && m.To == "packs/"+m.Package.Digest
		removeMove := m.From == "packs/"+m.Package.Digest && m.To == "archive/"+m.Package.Digest
		if !importMove && !removeMove {
			return nil, ErrCorrupt
		}
		if importMove {
			if p, ok := in.After.Packages[m.Package.Digest]; !ok || packDigest(p.Manifest) != m.Package.Digest {
				return nil, ErrCorrupt
			}
		} else {
			if _, ok := in.After.Archived[m.Package.Digest]; !ok {
				return nil, ErrCorrupt
			}
		}
	}
	return &in, nil
}
func location(s *store, where string) (*os.File, string, error) {
	parts := strings.Split(where, "/")
	if len(parts) == 1 {
		return s.root, parts[0], nil
	}
	if len(parts) != 2 || (parts[0] != "packs" && parts[0] != "archive") || !digestPattern.MatchString(parts[1]) {
		return nil, "", ErrCorrupt
	}
	dir, err := openAt(s.root, parts[0], true, false, true)
	return dir, parts[1], err
}
func unchanged(s *store, name, expected string) error {
	current, err := fileIdentity(s.root, name)
	if err != nil {
		return err
	}
	if current != expected || !s.anchored() {
		return ErrCorrupt
	}
	return nil
}
func performMove(s *store, m *move, check func() error) error {
	from, name, err := location(s, m.From)
	if err != nil {
		return err
	}
	if from != s.root {
		defer from.Close()
	}
	to, target, err := location(s, m.To)
	if err != nil {
		return err
	}
	if to != s.root {
		defer to.Close()
	}
	a, aerr := openAt(from, name, true, false, true)
	b, berr := openAt(to, target, true, false, true)
	if aerr == nil {
		defer a.Close()
	}
	if berr == nil {
		defer b.Close()
	}
	if aerr == nil && berr == nil {
		return ErrCorrupt
	}
	if aerr != nil && !os.IsNotExist(aerr) {
		return aerr
	}
	if berr != nil && !os.IsNotExist(berr) {
		return berr
	}
	if aerr != nil && berr != nil {
		return ErrCorrupt
	}
	if berr == nil {
		_, err = verifyBundle(b, true, &m.Package)
		if err != nil {
			return err
		}
		return check()
	}
	if _, err = verifyBundle(a, true, &m.Package); err != nil {
		return err
	}
	if !sameAt(from, name, a) || !s.anchored() {
		return ErrUnsafe
	}
	if err = check(); err != nil {
		return err
	}
	if err = unix.Renameat(int(from.Fd()), name, int(to.Fd()), target); err != nil {
		return err
	}
	if err = from.Sync(); err != nil {
		return err
	}
	return to.Sync()
}
func (r *registry) recover(s *store, rs records) (records, error) {
	in, err := existingIntent(s)
	if err != nil || in == nil {
		return rs, err
	}
	if recordsHash(rs) != in.Before && recordsHash(rs) != recordsHash(in.After) {
		return rs, ErrCorrupt
	}
	catalogIdentity, err := fileIdentity(s.root, "catalog.json")
	if err != nil {
		return rs, err
	}
	pendingIdentity, err := fileIdentity(s.root, "pending.json")
	if err != nil {
		return rs, err
	}
	if in.Move != nil {
		if err = performMove(s, in.Move, func() error {
			if err := unchanged(s, "catalog.json", catalogIdentity); err != nil {
				return err
			}
			return unchanged(s, "pending.json", pendingIdentity)
		}); err != nil {
			return rs, err
		}
	}
	if err = verifyRecords(s, in.After); err != nil {
		return rs, err
	}
	if !s.anchored() {
		return rs, ErrUnsafe
	}
	if err = atomicFile(s.root, "catalog.json", in.After, catalogIdentity); err != nil {
		return rs, err
	}
	current, err := fileIdentity(s.root, "pending.json")
	if err != nil {
		return rs, err
	}
	if current != pendingIdentity {
		return rs, ErrCorrupt
	}
	if err = unix.Unlinkat(int(s.root.Fd()), "pending.json", 0); err != nil {
		return rs, err
	}
	if err = s.root.Sync(); err != nil {
		return rs, err
	}
	return in.After, nil
}
func verifyRecords(s *store, rs records) error {
	for dir, packs := range map[string]map[string]Package{"packs": rs.Packages, "archive": rs.Archived} {
		if rs.Revision == 0 && len(packs) == 0 {
			continue
		}
		parent, err := openAt(s.root, dir, true, false, true)
		if err != nil {
			return err
		}
		for digest, p := range packs {
			d, err := openAt(parent, digest, true, false, true)
			if err != nil {
				parent.Close()
				return err
			}
			_, err = verifyBundle(d, true, &p)
			d.Close()
			if err != nil {
				parent.Close()
				return err
			}
		}
		parent.Close()
	}
	return nil
}
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

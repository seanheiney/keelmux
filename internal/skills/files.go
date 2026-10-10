package skills

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/seanheiney/agent-backplane/internal/domain"
	"golang.org/x/sys/unix"
)

// All traversal uses directory descriptors with O_NOFOLLOW. Replacing a
// symlink between preview and apply cannot redirect a write outside this root.
type tree struct {
	root *os.File
	path string
}

func openTree(root string) (*tree, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("skill root must be an explicit absolute user or project directory")
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("skill root must be a real directory, not a symlink")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(canonical, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &tree{os.NewFile(uintptr(fd), canonical), canonical}, nil
}
func (t *tree) close() { t.root.Close() }
func components(path string) ([]string, error) {
	if filepath.IsAbs(path) || path == "" {
		return nil, errors.New("invalid relative skill path")
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil, errors.New("invalid skill path component")
		}
	}
	return parts, nil
}
func (t *tree) dir(path string, create bool) (*os.File, error) {
	fd, err := unix.Dup(int(t.root.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	current := os.NewFile(uintptr(fd), t.path)
	if path == "." {
		return current, nil
	}
	parts, err := components(path)
	if err != nil {
		current.Close()
		return nil, err
	}
	for _, p := range parts {
		next, err := unix.Openat(int(current.Fd()), p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) && create {
			if err = unix.Mkdirat(int(current.Fd()), p, 0700); err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(int(current.Fd()), p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		current.Close()
		if err != nil {
			return nil, err
		}
		current = os.NewFile(uintptr(next), p)
	}
	return current, nil
}
func (t *tree) read(path string) ([]byte, error) {
	if _, err := components(path); err != nil {
		return nil, err
	}
	d, err := t.dir(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	fd, err := unix.Openat(int(d.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("owned skill file is not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, errors.New("owned skill file exceeds size limit")
	}
	return b, nil
}
func (t *tree) write(path string, b []byte, replace bool) error {
	if _, err := components(path); err != nil {
		return err
	}
	d, err := t.dir(filepath.Dir(path), true)
	if err != nil {
		return err
	}
	defer d.Close()
	tmp := string(domain.NewID(".skill-write"))
	fd, err := unix.Openat(int(d.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	defer f.Close()
	defer unix.Unlinkat(int(d.Fd()), tmp, 0)
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if replace {
		err = unix.Renameat(int(d.Fd()), tmp, int(d.Fd()), filepath.Base(path))
	} else {
		err = renameExclusive(int(d.Fd()), tmp, int(d.Fd()), filepath.Base(path))
	}
	if err != nil {
		return err
	}
	return d.Sync()
}
func (t *tree) publish(from, to string) error {
	if _, err := components(from); err != nil {
		return err
	}
	if _, err := components(to); err != nil {
		return err
	}
	a, err := t.dir(filepath.Dir(from), false)
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := t.dir(filepath.Dir(to), true)
	if err != nil {
		return err
	}
	defer b.Close()
	if err := renameExclusive(int(a.Fd()), filepath.Base(from), int(b.Fd()), filepath.Base(to)); err != nil {
		return err
	}
	if err := b.Sync(); err != nil {
		return err
	}
	return a.Sync()
}
func (t *tree) remove(path string, isDir bool) error {
	if _, err := components(path); err != nil {
		return err
	}
	d, err := t.dir(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer d.Close()
	flags := 0
	if isDir {
		flags = unix.AT_REMOVEDIR
	}
	if err := unix.Unlinkat(int(d.Fd()), filepath.Base(path), flags); err != nil {
		return err
	}
	return d.Sync()
}
func (t *tree) createDir(path string) error {
	if _, err := components(path); err != nil {
		return err
	}
	d, err := t.dir(filepath.Dir(path), true)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := unix.Mkdirat(int(d.Fd()), filepath.Base(path), 0700); err != nil {
		return err
	}
	return d.Sync()
}

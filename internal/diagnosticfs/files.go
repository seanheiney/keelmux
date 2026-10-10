// Package diagnosticfs opens existing private state without creating, repairing,
// syncing or following private child links. Handles anchor subsequent reads.
package diagnosticfs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var ErrUnsafe = errors.New("state is not an owned private regular object")

type Dir struct {
	File *os.File
	Path string
}

func OpenDir(path string) (*Dir, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnsafe
	}
	// Permit OS ancestor aliases such as /var, but never a private leaf alias.
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	return openDirAt(fd, filepath.Base(path), path)
}

func openDirAt(fd int, name, path string) (*Dir, error) {
	n, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(n), path)
	var s unix.Stat_t
	if unix.Fstat(n, &s) != nil || s.Uid != uint32(os.Geteuid()) || s.Mode&07777 != 0700 {
		f.Close()
		return nil, ErrUnsafe
	}
	return &Dir{File: f, Path: path}, nil
}

func nameOK(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}
func (d *Dir) Close() error { return d.File.Close() }
func (d *Dir) Child(name string) (*Dir, error) {
	if !nameOK(name) {
		return nil, ErrUnsafe
	}
	return openDirAt(int(d.File.Fd()), name, filepath.Join(d.Path, name))
}
func (d *Dir) Stat(name string) (unix.Stat_t, error) {
	var s unix.Stat_t
	if !nameOK(name) {
		return s, ErrUnsafe
	}
	err := unix.Fstatat(int(d.File.Fd()), name, &s, unix.AT_SYMLINK_NOFOLLOW)
	return s, err
}
func (d *Dir) Open(name string, mode uint32, bound int64) (*os.File, error) {
	if !nameOK(name) {
		return nil, ErrUnsafe
	}
	fd, err := unix.Openat(int(d.File.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Join(d.Path, name))
	var s unix.Stat_t
	if unix.Fstat(fd, &s) != nil || s.Mode&unix.S_IFMT != unix.S_IFREG || uint32(s.Mode)&07777 != mode || s.Uid != uint32(os.Geteuid()) || s.Nlink != 1 || s.Size > bound {
		f.Close()
		return nil, ErrUnsafe
	}
	return f, nil
}
func (d *Dir) Read(name string, mode uint32, bound int64) ([]byte, error) {
	f, err := d.Open(name, mode, bound)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, bound+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > bound {
		return nil, ErrUnsafe
	}
	return b, nil
}
func (d *Dir) Unchanged() bool {
	opened, e := d.File.Stat()
	if e != nil {
		return false
	}
	now, e := os.Lstat(d.Path)
	return e == nil && now.IsDir() && os.SameFile(opened, now)
}

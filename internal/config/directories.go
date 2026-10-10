package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// EnsurePrivateDirectory creates missing components with mode 0700 and syncs
// each new directory's parent before returning. Existing platform aliases in
// ancestors (such as macOS /var) are resolved before taking a directory handle;
// the requested leaf and every newly created component must be real directories.
// Existing leaf permissions are checked rather than silently tightened.
func EnsurePrivateDirectory(path string) error {
	return ensurePrivateDirectory(path, unix.Fsync)
}

func ensurePrivateDirectory(path string, syncDirectory func(int) error) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("private directory needs a clean absolute path")
	}
	ancestor := path
	var missing []string
	for {
		info, err := os.Lstat(ancestor)
		if err == nil {
			if ancestor == path && !info.IsDir() {
				return errors.New("private directory must be a real directory")
			}
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, filepath.Base(ancestor))
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return errors.New("private directory ancestor unavailable")
		}
		ancestor = parent
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return err
	}
	fd, err := unix.Open(resolved, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	// Confirm an existing ancestor's entry too: an earlier mkdir may have
	// succeeded while its parent sync failed before this retry.
	parent, err := unix.Open(filepath.Dir(resolved), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	err = syncDirectory(parent)
	_ = unix.Close(parent)
	if err != nil {
		return err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		name := missing[i]
		if name == "" || strings.ContainsRune(name, '/') {
			return errors.New("invalid private directory component")
		}
		created := false
		if err := unix.Mkdirat(fd, name, 0700); err == nil {
			created = true
		} else if !errors.Is(err, unix.EEXIST) {
			return err
		}
		child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		if created {
			if err := syncDirectory(fd); err != nil {
				_ = unix.Close(child)
				return err
			}
		}
		_ = unix.Close(fd)
		fd = child
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0777 != 0700 {
		return errors.New("private directory must be owned and mode 0700")
	}
	return syncDirectory(fd)
}

func syncDirectory(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Fsync(fd)
}

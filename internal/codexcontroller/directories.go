package codexcontroller

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// Native realm paths are private local configuration. Persist filesystem
// identity as well as the canonical path, so a symlink alias or replaced
// directory cannot rebind an existing primary to a different credential realm.
type directoryIdentity struct {
	Device uint64
	Inode  uint64
}

func ownedDirectory(path string) (string, directoryIdentity, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || !cleanPath(canonical) {
		return "", directoryIdentity{}, ErrInvalid
	}
	fd, err := unix.Open(canonical, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", directoryIdentity{}, ErrInvalid
	}
	f := os.NewFile(uintptr(fd), canonical)
	defer f.Close()
	i, err := f.Stat()
	if err != nil || !i.IsDir() {
		return "", directoryIdentity{}, ErrInvalid
	}
	s, ok := i.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != uint32(os.Geteuid()) || s.Ino == 0 {
		return "", directoryIdentity{}, ErrInvalid
	}
	return canonical, directoryIdentity{Device: uint64(s.Dev), Inode: uint64(s.Ino)}, nil
}

func sameDirectory(path string, want directoryIdentity) bool {
	canonical, got, err := ownedDirectory(path)
	return err == nil && canonical == path && got == want
}

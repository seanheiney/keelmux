package config

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// ReadPrivateSecret opens a local credential reference without following its
// leaf symlink or changing permissions. Secret bytes must never enter receipts,
// daemon logs, protocol responses or the durable channel registry.
func ReadPrivateSecret(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("credential reference must be a clean absolute path")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("credential file could not be opened")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, errors.New("credential file could not be inspected")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() < 1 || info.Size() > 8192 {
		return nil, errors.New("credential file must be owned, private, regular and at most 8192 bytes")
	}
	b, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(b) == 0 || len(b) > 8192 {
		return nil, errors.New("credential file could not be read within its bound")
	}
	return b, nil
}

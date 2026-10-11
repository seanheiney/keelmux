package nativepty

import (
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
)

func openStdioExecutable(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return nil, ps.ErrInvalid
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ps.ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), path)
	i, err := f.Stat()
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0111 == 0 || i.Size() > 512<<20 {
		f.Close()
		return nil, ps.ErrUnavailable
	}
	return f, nil
}

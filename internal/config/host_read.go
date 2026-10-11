package config

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/seanheiney/keelmux/internal/domain"
)

// ReadHostID observes an existing private host identity. It never creates,
// repairs, chmods or synchronizes state. An absent identity stays absent.
func ReadHostID(dir string) (id domain.ID, err error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || strings.ContainsRune(dir, 0) {
		return "", errors.New("host identity needs a clean absolute state directory")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if resolved != dir {
		return "", errors.New("host identity state directory must be canonical")
	}
	before, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !privateHostDirectory(before) {
		return "", errors.New("host identity state directory must be owned and private (0700)")
	}
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	directory := os.NewFile(uintptr(fd), dir)
	defer func() {
		if closeErr := directory.Close(); err == nil && closeErr != nil {
			id, err = "", closeErr
		}
	}()
	held, err := directory.Stat()
	if err != nil || !privateHostDirectory(held) || !os.SameFile(before, held) {
		return "", errors.New("host identity state directory changed")
	}
	path := filepath.Join(dir, "host-id")
	f, err := privateFile(path, syscall.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			id, err = "", closeErr
		}
	}()
	initial, err := f.Stat()
	if err != nil || initial.Size() > 256 || initial.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || initial.Sys().(*syscall.Stat_t).Nlink != 1 {
		return "", errors.New("host identity must be bounded and have one owned link")
	}
	b, err := io.ReadAll(io.LimitReader(f, 257))
	if err != nil {
		return "", err
	}
	if !validHostID(b) {
		return "", errors.New("host identity must be host_ followed by 32 lowercase hex digits; repair required")
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(initial, current) || !current.Mode().IsRegular() || current.Mode().Perm() != 0600 || current.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || current.Size() != initial.Size() || !current.ModTime().Equal(initial.ModTime()) || current.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || current.Sys().(*syscall.Stat_t).Nlink != 1 {
		return "", errors.New("host identity changed during observation")
	}
	currentDirectory, err := os.Lstat(dir)
	if err != nil || !privateHostDirectory(currentDirectory) || !os.SameFile(held, currentDirectory) {
		return "", errors.New("host identity state directory changed")
	}
	return domain.ID(b), nil
}

func privateHostDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm() == 0700 && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && info.Sys().(*syscall.Stat_t).Uid == uint32(os.Geteuid())
}

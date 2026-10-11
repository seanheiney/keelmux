package nativepty

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	ps "github.com/seanheiney/keelmux/internal/primarystdio"
	"golang.org/x/sys/unix"
)

// A visible atomic rename does not establish that its directory sync succeeded.
// Reconciliation confirms persistence of the exact validated terminal evidence
// without rewriting bytes, creating state, or performing another native effect.
// sync is private to permit deterministic held/failing filesystem controls.
func (b *Backend) confirmTerminalStdio(ctx context.Context, r receipt, state stdioState, sync func(*os.File) error) error {
	if ctx.Err() != nil {
		return ps.ErrUncertain
	}
	fd, err := unix.Open(b.state, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ps.ErrStorage
	}
	dir := os.NewFile(uintptr(fd), b.state)
	defer dir.Close()
	dirInfo, err := dir.Stat()
	if err != nil || !privateTerminalDirectory(dirInfo) {
		return ps.ErrStorage
	}
	type heldFile struct {
		file *os.File
		info os.FileInfo
		path string
		raw  []byte
	}
	var files []heldFile
	defer func() {
		for _, held := range files {
			_ = held.file.Close()
		}
	}()
	for _, item := range []struct {
		path  string
		value any
	}{
		{b.receiptPath(r.Ref.ExecutionID), r},
		{b.stdioPath(r.Ref.ExecutionID), state},
	} {
		raw, e := json.Marshal(item.value)
		if e != nil || len(raw) > ps.MaxEventBytes {
			return ps.ErrStorage
		}
		fd, e := unix.Openat(int(dir.Fd()), filepath.Base(item.path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if e != nil {
			return ps.ErrStorage
		}
		file := os.NewFile(uintptr(fd), item.path)
		info, e := file.Stat()
		files = append(files, heldFile{file, info, item.path, raw})
		if e != nil || !privateTerminalFile(info) {
			return ps.ErrStorage
		}
	}
	check := func() error {
		if ctx.Err() != nil {
			return ps.ErrUncertain
		}
		currentDir, e := os.Lstat(b.state)
		if e != nil || !privateTerminalDirectory(currentDir) || !os.SameFile(dirInfo, currentDir) {
			return ps.ErrStorage
		}
		for _, held := range files {
			info, e := held.file.Stat()
			if e != nil || !privateTerminalFile(info) || !os.SameFile(held.info, info) {
				return ps.ErrStorage
			}
			current, e := os.Lstat(held.path)
			if e != nil || !privateTerminalFile(current) || !os.SameFile(held.info, current) {
				return ps.ErrStorage
			}
			raw, e := io.ReadAll(io.NewSectionReader(held.file, 0, int64(len(held.raw))+1))
			if e != nil || !bytes.Equal(raw, held.raw) {
				return ps.ErrStorage
			}
		}
		return nil
	}
	if err = check(); err != nil {
		return err
	}
	for _, file := range []*os.File{files[0].file, files[1].file, dir} {
		if ctx.Err() != nil {
			return ps.ErrUncertain
		}
		if sync(file) != nil {
			return ps.ErrStorage
		}
		if err = check(); err != nil {
			return err
		}
	}
	return nil
}

func privateTerminalDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm()&0077 == 0 && owned(info)
}
func privateTerminalFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0 && owned(info)
}

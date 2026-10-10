package nativepty

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
)

func fileDescriptor(f *os.File) (int, error) {
	raw, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	var fd int
	err = raw.Control(func(value uintptr) { fd = int(value) })
	return fd, err
}
func writePTY(f *os.File, p []byte) error {
	fd, err := fileDescriptor(f)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ioTimeout)
	defer cancel()
	for len(p) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
		_, err := unix.Poll(poll, 50)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if poll[0].Revents == 0 {
			continue
		}
		n, err := unix.Write(fd, p)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
func readTerminal(ctx context.Context, fd int, p []byte) (int, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(poll, 50)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if poll[0].Revents == 0 {
			continue
		}
		n, err := unix.Read(fd, p)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if errors.Is(err, unix.EIO) {
			return 0, io.EOF
		}
		if n == 0 && err == nil {
			return 0, io.EOF
		}
		return n, err
	}
}

package transport

import (
	"context"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// descriptor avoids File.Fd: that method can put a Go-owned descriptor back
// into blocking mode. Keep the File alive, and do not close it while using fd.
func descriptor(file *os.File) (fd, flags int, err error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return 0, 0, err
	}
	var flagErr error
	err = raw.Control(func(value uintptr) {
		fd = int(value)
		flags, flagErr = unix.FcntlInt(value, unix.F_GETFL, 0)
	})
	if err == nil {
		err = flagErr
	}
	return fd, flags, err
}

func setFlags(fd, flags int) error {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags)
	return err
}

func pollTerminal(ctx context.Context, fd int, events int16) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: events}}
		_, err := unix.Poll(fds, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if fds[0].Revents != 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			if fds[0].Revents&unix.POLLNVAL != 0 {
				return unix.EBADF
			}
			return nil
		}
	}
}

// fd must remain nonblocking throughout these operations. Poll readiness can
// race with a signal or another consumer and never makes a blocking fd safe.
func readTerminal(ctx context.Context, fd int, data []byte) (int, error) {
	for {
		if err := pollTerminal(ctx, fd, unix.POLLIN); err != nil {
			return 0, err
		}
		n, err := unix.Read(fd, data)
		if err == unix.EIO {
			return 0, io.EOF // Exited Unix PTY client.
		}
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if n == 0 && err == nil {
			return 0, io.EOF
		}
		return n, err
	}
}

func writeTerminal(ctx context.Context, fd int, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, ioTimeout)
	defer cancel()
	for len(data) > 0 {
		if err := pollTerminal(ctx, fd, unix.POLLOUT); err != nil {
			return err
		}
		n, err := unix.Write(fd, data)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

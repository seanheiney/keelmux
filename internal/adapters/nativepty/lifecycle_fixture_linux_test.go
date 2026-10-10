package nativepty

import "golang.org/x/sys/unix"

func lifecycleFixtureTermios() error {
	t, err := unix.IoctlGetTermios(0, unix.TCGETS)
	if err != nil {
		return err
	}
	t.Lflag &^= unix.ECHO | unix.ECHONL
	t.Lflag |= unix.ICANON | unix.ISIG
	t.Cc[unix.VINTR] = 3
	return unix.IoctlSetTermios(0, unix.TCSETS, t)
}

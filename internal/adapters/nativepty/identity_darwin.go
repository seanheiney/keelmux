package nativepty

import (
	"fmt"
	"golang.org/x/sys/unix"
	"net"
	"os"
)

func bootID() (string, error) {
	v, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("darwin:%d:%d", v.Sec, v.Usec), nil
}
func processStart(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid PID")
	}
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", err
	}
	if p.Proc.P_pid != int32(pid) || p.Proc.P_stat == 5 {
		return "", os.ErrNotExist
	}
	return fmt.Sprintf("darwin:%d:%d", p.Proc.P_starttime.Sec, p.Proc.P_starttime.Usec), nil
}
func peerIdentity(c *net.UnixConn) (pid, uid int, err error) {
	raw, e := c.SyscallConn()
	if e != nil {
		return 0, 0, e
	}
	var inner error
	err = raw.Control(func(fd uintptr) {
		var u *unix.Xucred
		u, inner = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if inner != nil {
			return
		}
		uid = int(u.Uid)
		pid, inner = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if err == nil {
		err = inner
	}
	return
}

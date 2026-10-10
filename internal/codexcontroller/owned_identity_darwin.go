package codexcontroller

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/seanheiney/keelmux/internal/domain"
	"golang.org/x/sys/unix"
)

func ownedImageSupported() bool                        { return false }
func verifyOwnedImage(domain.RuntimeRef, string) error { return ErrUnavailable }
func verifyOwnedPeer(conn *net.UnixConn, ref domain.RuntimeRef) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return ErrUnavailable
	}
	var inner error
	err = raw.Control(func(fd uintptr) {
		credential, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil || credential.Uid != uint32(os.Geteuid()) {
			inner = ErrConflict
			return
		}
		pid, e := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if e != nil || pid != ref.PID {
			inner = ErrConflict
			return
		}
		boot, e := unix.Sysctl("kern.bootsessionuuid")
		if e != nil || "darwin:"+strings.ToLower(boot) != ref.BootID {
			inner = ErrConflict
			return
		}
		p, e := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if e != nil || p.Proc.P_pid != int32(pid) || p.Proc.P_stat == 5 || fmt.Sprintf("darwin:%d:%d", p.Proc.P_starttime.Sec, p.Proc.P_starttime.Usec) != ref.ProcessStart {
			inner = ErrConflict
		}
	})
	if err != nil {
		return ErrUnavailable
	}
	return inner
}

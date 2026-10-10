package diagnostics

import (
	"golang.org/x/sys/unix"
	"net"
	"os"
	"strconv"
	"strings"
)

func processIdentity(pid int) (string, string, error) {
	if pid <= 0 {
		return "", "", errIdentity
	}
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", "", err
	}
	boot := strings.TrimSpace(string(b))
	if len(boot) != 36 {
		return "", "", errIdentity
	}
	v, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", "", err
	}
	end := strings.LastIndexByte(string(v), ')')
	if end < 0 {
		return "", "", errIdentity
	}
	fields := strings.Fields(string(v)[end+1:])
	if len(fields) < 20 {
		return "", "", errIdentity
	}
	if fields[0] == "Z" {
		return "", "", os.ErrNotExist
	}
	return "linux:" + strings.ToLower(boot), "linux-ticks:" + fields[19], nil
}
func peerIdentity(c *net.UnixConn) (pid, uid int, err error) {
	raw, e := c.SyscallConn()
	if e != nil {
		return 0, 0, e
	}
	var inner error
	err = raw.Control(func(fd uintptr) {
		var u *unix.Ucred
		u, inner = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if inner == nil {
			pid = int(u.Pid)
			uid = int(u.Uid)
		}
	})
	if err == nil {
		err = inner
	}
	return
}

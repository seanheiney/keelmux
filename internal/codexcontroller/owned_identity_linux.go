package codexcontroller

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/seanheiney/keelmux/internal/domain"
	"golang.org/x/sys/unix"
)

func ownedImageSupported() bool { return true }
func ownedLifetime(ref domain.RuntimeRef) error {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || "linux:"+strings.TrimSpace(string(boot)) != ref.BootID {
		return ErrConflict
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(ref.PID) + "/stat")
	if err != nil {
		return ErrUnavailable
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return ErrUnavailable
	}
	fields := strings.Fields(string(stat)[end+1:])
	if len(fields) < 20 || fields[0] == "Z" || "linux-ticks:"+fields[19] != ref.ProcessStart {
		return ErrConflict
	}
	return nil
}
func verifyOwnedPeer(conn *net.UnixConn, ref domain.RuntimeRef) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return ErrUnavailable
	}
	var inner error
	err = raw.Control(func(fd uintptr) {
		cred, e := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e != nil || cred.Pid != int32(ref.PID) || cred.Uid != uint32(os.Geteuid()) {
			inner = ErrConflict
			return
		}
		inner = ownedLifetime(ref)
	})
	if err != nil {
		return ErrUnavailable
	}
	return inner
}
func verifyOwnedImage(ref domain.RuntimeRef, want string) error {
	if !digest(want) || ownedLifetime(ref) != nil {
		return ErrConflict
	}
	file, err := os.Open("/proc/" + strconv.Itoa(ref.PID) + "/exe")
	if err != nil {
		return ErrUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512<<20 {
		return ErrUnavailable
	}
	sum := sha256.New()
	if _, err = io.Copy(sum, file); err != nil {
		return ErrUnavailable
	}
	if hex.EncodeToString(sum.Sum(nil)) != want {
		return ErrConflict
	}
	return ownedLifetime(ref)
}

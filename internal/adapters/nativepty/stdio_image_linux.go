//go:build linux

package nativepty

import (
	"crypto/sha256"
	"encoding/hex"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
	"io"
	"os"
	"strconv"
	"syscall"
)

func stdioPlatformImageAvailable() bool { return true }
func verifyStdioRunningImage(c ps.Channel) error {
	return verifyStdioLinuxImage(c, bootID, processStart, func(pid int) (*os.File, error) {
		return os.Open("/proc/" + strconv.Itoa(pid) + "/exe")
	})
}

// The opened procfs executable retains the backing object across exec. A PID's
// start time does not change at exec, so re-open the kernel executable after
// hashing and compare its backing identity. This establishes a checked instant;
// it does not prevent a future exec or prove there was no intervening exec ABA.
// Dependencies are fixed by the production wrapper; no launch/wire option can
// replace them. Tests use owned descriptors to control a change during hashing.
func verifyStdioLinuxImage(c ps.Channel, readBoot func() (string, error), readStart func(int) (string, error), openImage func(int) (*os.File, error)) error {
	r := c.Ref.Runtime
	boot, err := readBoot()
	if err != nil || boot != r.BootID {
		return ps.ErrUnavailable
	}
	start, err := readStart(r.PID)
	if err != nil || start != r.ProcessStart {
		return ps.ErrConflict
	}
	f, err := openImage(r.PID)
	if err != nil {
		return ps.ErrUnavailable
	}
	defer f.Close()
	i, err := f.Stat()
	if err != nil || !i.Mode().IsRegular() || i.Size() <= 0 || i.Size() > 512<<20 {
		return ps.ErrUnavailable
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (512<<20)+1))
	if err != nil {
		return ps.ErrUnavailable
	}
	if n != i.Size() || hex.EncodeToString(h.Sum(nil)) != c.Spec.BinarySHA256 {
		return ps.ErrConflict
	}
	current, err := openImage(r.PID)
	if err != nil {
		return ps.ErrUnavailable
	}
	defer current.Close()
	currentInfo, err := current.Stat()
	if err != nil || !sameStdioLinuxImageMetadata(i, currentInfo) {
		return ps.ErrConflict
	}
	heldInfo, err := f.Stat()
	if err != nil || !sameStdioLinuxImageMetadata(i, heldInfo) {
		return ps.ErrConflict
	}
	boot, err = readBoot()
	if err != nil || boot != r.BootID {
		return ps.ErrUnavailable
	}
	start, err = readStart(r.PID)
	if err != nil || start != r.ProcessStart {
		return ps.ErrConflict
	}
	return nil
}

func sameStdioLinuxImageMetadata(before, after os.FileInfo) bool {
	if before == nil || after == nil || !before.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return false
	}
	a, okA := before.Sys().(*syscall.Stat_t)
	b, okB := after.Sys().(*syscall.Stat_t)
	return okA && okB && a.Size == b.Size && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

//go:build linux

package nativepty

import (
	"crypto/sha256"
	"encoding/hex"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
	"io"
	"os"
	"strconv"
)

func stdioPlatformImageAvailable() bool { return true }
func verifyStdioRunningImage(c ps.Channel) error {
	r := c.Ref.Runtime
	boot, err := bootID()
	if err != nil || boot != r.BootID {
		return ps.ErrUnavailable
	}
	start, err := processStart(r.PID)
	if err != nil || start != r.ProcessStart {
		return ps.ErrConflict
	}
	f, err := os.Open("/proc/" + strconv.Itoa(r.PID) + "/exe")
	if err != nil {
		return ps.ErrUnavailable
	}
	defer f.Close()
	i, err := f.Stat()
	if err != nil || !i.Mode().IsRegular() || i.Size() > 512<<20 {
		return ps.ErrUnavailable
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil || hex.EncodeToString(h.Sum(nil)) != c.Spec.BinarySHA256 {
		return ps.ErrConflict
	}
	start, err = processStart(r.PID)
	if err != nil || start != r.ProcessStart {
		return ps.ErrConflict
	}
	return nil
}

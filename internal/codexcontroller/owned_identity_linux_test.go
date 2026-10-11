package codexcontroller

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/seanheiney/keelmux/internal/domain"
	"os"
	"strconv"
	"strings"
	"testing"
)

func ownedCurrentRef(t *testing.T, pid int) domain.RuntimeRef {
	t.Helper()
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		t.Fatal(e)
	}
	p, e := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if e != nil {
		t.Fatal(e)
	}
	end := strings.LastIndexByte(string(p), ')')
	f := strings.Fields(string(p)[end+1:])
	return domain.RuntimeRef{PID: pid, BootID: "linux:" + strings.TrimSpace(string(boot)), ProcessStart: "linux-ticks:" + f[19]}
}

func TestOwnedLinuxImageDigestUsesRunningExecutable(t *testing.T) {
	ref := ownedCurrentRef(t, os.Getpid())
	bytes, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bytes)
	if err = verifyOwnedImage(ref, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if verifyOwnedImage(ref, strings.Repeat("0", 64)) == nil {
		t.Fatal("wrong image accepted")
	}
	ref.ProcessStart = "linux-ticks:wrong"
	if verifyOwnedImage(ref, hex.EncodeToString(sum[:])) == nil {
		t.Fatal("reused PID lifetime accepted")
	}
}

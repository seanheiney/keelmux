package codexcontroller

import (
	"fmt"
	"github.com/seanheiney/keelmux/internal/domain"
	"golang.org/x/sys/unix"
	"strings"
	"testing"
)

func ownedCurrentRef(t *testing.T, pid int) domain.RuntimeRef {
	t.Helper()
	boot, e := unix.Sysctl("kern.bootsessionuuid")
	if e != nil {
		t.Fatal(e)
	}
	p, e := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if e != nil {
		t.Fatal(e)
	}
	return domain.RuntimeRef{PID: pid, BootID: "darwin:" + strings.ToLower(boot), ProcessStart: fmt.Sprintf("darwin:%d:%d", p.Proc.P_starttime.Sec, p.Proc.P_starttime.Usec)}
}

package diagnosticfs

import (
	"golang.org/x/sys/unix"
	"testing"
)

func makeFIFO(t *testing.T, path string) {
	t.Helper()
	if e := unix.Mkfifo(path, 0600); e != nil {
		t.Fatal(e)
	}
}

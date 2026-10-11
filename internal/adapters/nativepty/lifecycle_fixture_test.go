package nativepty

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A shell builtin read can lose a byte or miss its trap around SIGINT on Darwin.
// This finite test-mode child keeps the real PTY/signal path and exact payload
// assertions while removing shell interpreter behavior from the fixture.
func runPTYLifecycleFixture() int {
	if err := lifecycleFixtureTermios(); err != nil {
		return 70
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, syscall.SIGINT)
	defer signal.Stop(interrupts)
	go func() {
		for range interrupts {
			fmt.Fprintln(os.Stdout, "INTERRUPTED")
		}
	}()
	time.AfterFunc(45*time.Second, func() { os.Exit(71) })
	fmt.Fprintln(os.Stdout, "READY")
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "size" {
			winsize, err := unix.IoctlGetWinsize(0, unix.TIOCGWINSZ)
			if err != nil {
				return 72
			}
			fmt.Fprintf(os.Stdout, "%d %d\n", winsize.Row, winsize.Col)
		} else {
			fmt.Fprintf(os.Stdout, "REPLY:%s\n", line)
		}
	}
}

func TestPTYLifecycleNoSignalControl(t *testing.T) {
	testPTYLifecycle(t, false)
}

func testPTYLifecycle(t *testing.T, interrupt bool) {
	t.Helper()
	b := testBackend(t, 4096)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec("")
	spec.Argv = []string{self}
	spec.Env = []string{"KEELMUX_TEST_PTY_LIFECYCLE_CHILD=1"}
	ref := launchTest(t, b, spec, false)
	s := openWriter(t, b, ref)
	if err := s.WriteInput([]byte("héllo 世界 🛟\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, s, "REPLY:héllo 世界 🛟")
	if err := s.Resize(37, 113); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteInput([]byte("size\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, s, "37 113")
	if interrupt {
		if err := s.WriteInput([]byte{3}); err != nil {
			t.Fatal(err)
		}
		readUntil(t, s, "INTERRUPTED")
	}
	s.Close()
	waitNoWriter(t, b, ref)
	reopened, err := NewWithOptions(b.state, Options{HelperPath: helperPath})
	if err != nil {
		t.Fatal(err)
	}
	obs, err := reopened.Lookup(context.Background(), spec.Session.Execution.ID, spec.OperationID)
	if err != nil || !obs.Alive || obs.Ref != ref {
		t.Fatalf("recover %+v %v", obs, err)
	}
	s2 := openWriter(t, reopened, ref)
	if err := s2.WriteInput([]byte("reconnected\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, s2, "REPLY:reconnected")
}

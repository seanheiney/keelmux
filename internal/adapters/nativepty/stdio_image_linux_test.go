//go:build linux

package nativepty

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/domain"
	ps "github.com/seanheiney/keelmux/internal/primarystdio"
)

func TestStdioLinuxRunningImageRejectsExecAfterImageOpen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "printf 'ready\\n'; read value; exec /bin/sleep 30")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, e := bufio.NewReader(out).ReadString('\n'); e != nil || line != "ready\n" {
		t.Fatal("owned shell not ready", line, e)
	}
	pid := cmd.Process.Pid
	boot, err := bootID()
	if err != nil {
		t.Fatal(err)
	}
	start, err := processStart(pid)
	if err != nil {
		t.Fatal(err)
	}
	shell, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := stdioFileDigest(shell)
	if err != nil {
		t.Fatal(err)
	}
	c := ps.Channel{Spec: ps.LaunchSpec{BinarySHA256: digest}, Ref: ps.ChannelRef{Runtime: domain.RuntimeRef{PID: pid, BootID: boot, ProcessStart: start}}}
	if err = verifyStdioRunningImage(c); err != nil {
		t.Fatal("initial owned image rejected", err)
	}
	exe := "/proc/" + strconv.Itoa(pid) + "/exe"
	before, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	opens := 0
	open := func(gotPID int) (*os.File, error) {
		if gotPID != pid {
			t.Fatal("foreign process lookup")
		}
		opens++
		file, e := os.Open(exe)
		if e != nil {
			return nil, e
		}
		if opens == 1 {
			if _, e := in.Write([]byte("continue\n")); e != nil {
				t.Fatal(e)
			}
			for {
				current, e := os.Stat(exe)
				if e != nil {
					t.Fatal(e)
				}
				if !os.SameFile(before, current) {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("owned exec not observed")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			if after, e := processStart(pid); e != nil || after != start {
				t.Fatal("exec changed lifetime control", after, e)
			}
		}
		return file, nil
	}
	if err = verifyStdioLinuxImage(c, bootID, processStart, open); !errors.Is(err, ps.ErrConflict) {
		t.Fatal("changed running image accepted", err)
	}
	if opens != 2 {
		t.Fatal("kernel executable not re-opened", opens)
	}
	if err = verifyStdioRunningImage(c); !errors.Is(err, ps.ErrConflict) {
		t.Fatal("sustained replacement accepted", err)
	}
}

func TestStdioLinuxImageIdentityNegatives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image")
	if err := os.WriteFile(path, []byte("owned-image"), 0700); err != nil {
		t.Fatal(err)
	}
	digest, err := stdioFileDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	c := ps.Channel{Spec: ps.LaunchSpec{BinarySHA256: digest}, Ref: ps.ChannelRef{Runtime: domain.RuntimeRef{PID: 42, BootID: "boot", ProcessStart: "start"}}}
	for _, kind := range []string{"stable", "denied", "disappeared", "boot_changed", "start_changed", "digest", "metadata", "same_bytes_new_inode", "oversized", "empty"} {
		t.Run(kind, func(t *testing.T) {
			candidate := c
			if kind == "digest" {
				candidate.Spec.BinarySHA256 = strings.Repeat("0", 64)
			}
			bootReads, startReads, opens := 0, 0, 0
			boot := func() (string, error) {
				bootReads++
				if kind == "boot_changed" && bootReads == 2 {
					return "other", nil
				}
				return "boot", nil
			}
			start := func(pid int) (string, error) {
				if pid != 42 {
					t.Fatal("wrong PID")
				}
				startReads++
				if kind == "start_changed" && startReads == 2 {
					return "other", nil
				}
				return "start", nil
			}
			file := filepath.Join(t.TempDir(), "image")
			if e := os.WriteFile(file, []byte("owned-image"), 0700); e != nil {
				t.Fatal(e)
			}
			if kind == "oversized" || kind == "empty" {
				size := int64((512 << 20) + 1)
				if kind == "empty" {
					size = 0
				}
				if e := os.Truncate(file, size); e != nil {
					t.Fatal(e)
				}
			}
			open := func(pid int) (*os.File, error) {
				if pid != 42 {
					t.Fatal("wrong PID")
				}
				opens++
				if kind == "denied" || kind == "disappeared" && opens == 2 {
					return nil, os.ErrPermission
				}
				if opens == 2 && kind == "metadata" {
					if e := os.Chmod(file, 0600); e != nil {
						t.Fatal(e)
					}
				}
				if opens == 2 && kind == "same_bytes_new_inode" {
					if e := os.Rename(file, file+".old"); e != nil {
						t.Fatal(e)
					}
					if e := os.WriteFile(file, []byte("owned-image"), 0700); e != nil {
						t.Fatal(e)
					}
				}
				return os.Open(file)
			}
			e := verifyStdioLinuxImage(candidate, boot, start, open)
			if kind == "stable" {
				if e != nil {
					t.Fatal(e)
				}
			} else if e == nil {
				t.Fatal("adverse image accepted")
			}
		})
	}
}

// Uses the normal compiled supervisor and its production /proc image verifier.
// The child is still a deterministic Go protocol fixture, never native Codex.
func TestStdioLinuxProductionHelperDeterministicLifecycle(t *testing.T) {
	b, spec := testStdio(t, "")
	stdioImageAvailable, stdioImageVerifier = stdioPlatformImageAvailable, verifyStdioRunningImage
	b.helper = helperPath
	ref := launchStdioFixture(t, b, spec)
	o := readyStdio(t, b, ref)
	if !o.Snapshot.ExactImage || !o.Snapshot.ProcessAlive || !o.Snapshot.ExactDirectories {
		t.Fatal("production identity unavailable", o.Snapshot)
	}
	fresh, err := NewWithOptions(b.state, Options{HelperPath: helperPath})
	if err != nil {
		t.Fatal(err)
	}
	retained, err := fresh.RetainedStdio(context.Background(), ref, o.Through)
	if err != nil || retained.Through != o.Through {
		t.Fatal("fresh client lost retained evidence", err)
	}
	called := false
	q := ps.Quiescence{Cursor: o.Through, SnapshotSHA256: o.Snapshot.SnapshotSHA256}
	if err = fresh.WithStdioQuiescence(context.Background(), ref, q, func(context.Context) error { called = true; return nil }); err != nil || !called {
		t.Fatal("production guard unavailable", err)
	}
	stop := ps.StopRequest{OperationID: domain.NewID("stop"), Quiescence: q}
	if err = fresh.StopStdio(context.Background(), ref, stop); err != nil {
		t.Fatal("production stop failed", err)
	}
	if err = fresh.StopStdio(context.Background(), ref, stop); err != nil {
		t.Fatal("durable stop retry failed", err)
	}
	r, err := fresh.readReceipt(ref.Runtime.ExecutionID)
	if err != nil || r.Stage != "stopped" {
		t.Fatal("terminal receipt unavailable", r.Stage, err)
	}
	if got := stdioMethods(t, spec); len(got) != 7 {
		t.Fatal("unexpected native fixture requests", got)
	}
}

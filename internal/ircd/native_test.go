package ircd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/adapters/nativepty"
	"github.com/seanheiney/keelmux/internal/domain"
)

type nativeControlResult struct {
	Status        Status
	Ref           domain.RuntimeRef
	SupervisorPID int
}

// This child is a real short-lived control process. It exits without owning or
// retaining an os.Process handle for the foreground Ergo or native helper.
func TestNativeErgoControl(t *testing.T) {
	if os.Getenv("KEELMUX_NATIVE_IRCD_CHILD") != "1" {
		t.Skip("owned control subprocess only")
	}
	marker := -1
	for i, a := range os.Args {
		if a == "--" {
			marker = i
			break
		}
	}
	if marker < 0 || len(os.Args) < marker+6 {
		os.Exit(2)
	}
	args := os.Args[marker+1:]
	root, mode, op, expected := args[0], args[1], domain.ID(args[2]), domain.ID(args[3])
	backend, e := nativepty.NewWithOptions(filepath.Join(root, "native"), nativepty.Options{HelperPath: args[4]})
	if e != nil {
		os.Exit(3)
	}
	m, e := NewWithSupervisor(filepath.Join(root, "ircd"), backend)
	if e != nil {
		os.Exit(4)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var status Status
	switch mode {
	case "start":
		f, e := os.Open(os.Getenv("KEELMUX_ERGO_ARCHIVE"))
		if e != nil {
			os.Exit(5)
		}
		_, e = m.InstallArchive(ctx, f)
		f.Close()
		if e != nil {
			os.Exit(6)
		}
		if _, e = m.Init(ctx, InitOptions{}); e != nil {
			os.Exit(7)
		}
		status, e = m.Start(ctx, op)
	case "status":
		status, e = m.Status(ctx)
	case "restart":
		status, e = m.Restart(ctx, expected, op)
	case "stale-stop":
		e = m.Stop(ctx, expected)
		if e != ErrConflict {
			os.Exit(8)
		}
		status, e = m.Status(ctx)
	case "stop":
		e = m.Stop(ctx, expected)
		if e == nil {
			status, e = m.Status(ctx)
		}
	default:
		os.Exit(9)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, "managed native operation failed")
		os.Exit(10)
	}
	if status.Alive {
		if _, e = m.EnsureAccount(ctx, status.Config.Account); e != nil {
			os.Exit(11)
		}
		if _, e = m.EnsureAccount(ctx, "operator-one"); e != nil {
			os.Exit(11)
		}
	}
	c, e := m.readConfig(ctx)
	if e != nil {
		os.Exit(12)
	}
	record, e := m.loadLifecycle(c, m.lifecyclePath())
	if e != nil {
		os.Exit(13)
	}
	sum := sha256.Sum256([]byte(record.Ref.ExecutionID))
	receiptPath := filepath.Join(root, "native", hex.EncodeToString(sum[:])[:24]+".json")
	raw, e := readFile(receiptPath, 1<<20, 0600)
	if e != nil {
		os.Exit(14)
	}
	var receipt struct {
		SupervisorPID int `json:"supervisor_pid"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.SupervisorPID < 1 {
		os.Exit(15)
	}
	b, e := json.Marshal(nativeControlResult{Status: status, Ref: record.Ref, SupervisorPID: receipt.SupervisorPID})
	if e != nil {
		os.Exit(16)
	}
	fmt.Print(string(b))
	os.Exit(0)
}
func TestRealNativeErgoControlExit(t *testing.T) {
	archive, helper := os.Getenv("KEELMUX_ERGO_ARCHIVE"), os.Getenv("KEELMUX_NATIVE_HELPER")
	if archive == "" || helper == "" {
		t.Skip("explicit pinned archive and built native helper required")
	}
	if !filepath.IsAbs(helper) {
		t.Fatal("helper path must be explicit absolute")
	}
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	root, e := os.MkdirTemp("/private/tmp", "kmi-native-")
	if e != nil {
		root, e = os.MkdirTemp("/tmp", "kmi-native-")
	}
	if e != nil {
		t.Fatal(e)
	}
	var current nativeControlResult
	var results []nativeControlResult
	var controlPIDs []int
	cleanup := func() {
		backend, e := nativepty.NewWithOptions(filepath.Join(root, "native"), nativepty.Options{HelperPath: helper})
		if e == nil {
			if m, e := NewWithSupervisor(filepath.Join(root, "ircd"), backend); e == nil {
				if s, e := m.Status(context.Background()); e == nil && s.Instance != "" {
					_ = m.Stop(context.Background(), s.Instance)
				}
			}
		}
		for _, r := range results {
			for _, pid := range []int{r.Ref.PID, r.SupervisorPID} {
				deadline := time.Now().Add(5 * time.Second)
				for processPresent(pid) && time.Now().Before(deadline) {
					time.Sleep(20 * time.Millisecond)
				}
				if processPresent(pid) {
					t.Errorf("owned native process %d cleanup unconfirmed; state retained", pid)
					return
				}
			}
		}
		if e = os.RemoveAll(root); e != nil {
			t.Error(e)
		}
	}
	t.Cleanup(cleanup)
	run := func(mode string, op, expected domain.ID) nativeControlResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "-test.run=^TestNativeErgoControl$", "--", root, mode, string(op), string(expected), helper)
		cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + root, "TMPDIR=" + root, "KEELMUX_NATIVE_IRCD_CHILD=1", "KEELMUX_ERGO_ARCHIVE=" + archive}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		pid := cmd.Process.Pid
		controlPIDs = append(controlPIDs, pid)
		if e = cmd.Wait(); e != nil {
			t.Fatalf("control %s failed: %v (safe diagnostic %d bytes)", mode, e, stderr.Len())
		}
		if processPresent(pid) {
			t.Fatal("control process survived Wait")
		}
		var r nativeControlResult
		if json.Unmarshal(stdout.Bytes(), &r) != nil {
			t.Fatal("invalid owned control receipt")
		}
		results = append(results, r)
		current = r
		return r
	}
	operation := domain.NewID("operation")
	first := run("start", operation, "")
	if !first.Status.Alive || first.Ref.Backend != "native" || first.Ref.PID < 1 || first.SupervisorPID < 1 || first.Ref.PID == controlPIDs[0] || first.SupervisorPID == controlPIDs[0] {
		t.Fatal("native execution proof missing")
	}
	again := run("status", "", "")
	if !again.Status.Alive || again.Ref != first.Ref || again.Status.StartTime != first.Status.StartTime || again.SupervisorPID != first.SupervisorPID {
		t.Fatal("native instance did not survive control exit")
	}
	reopened := run("start", operation, "")
	if reopened.Ref != first.Ref || !reopened.Status.Alive {
		t.Fatal("admitted native launch was replayed")
	}
	replacement := run("restart", domain.NewID("operation"), first.Status.Instance)
	if !replacement.Status.Alive || replacement.Status.Instance == first.Status.Instance || replacement.Ref == first.Ref || replacement.Ref.Session == first.Ref.Session {
		t.Fatal("replacement reused runtime identity")
	}
	stale := run("stale-stop", "", first.Status.Instance)
	if !stale.Status.Alive || stale.Ref != replacement.Ref {
		t.Fatal("stale instance stopped replacement")
	}
	stopped := run("stop", "", replacement.Status.Instance)
	if stopped.Status.Alive || stopped.Status.State != "stopped" || current.Ref != replacement.Ref {
		t.Fatal("exact native stop unconfirmed")
	}
	t.Logf("native control-exit proof: %d separate control processes, preserved initial instance, explicit replacement, stale-stop refusal, confirmed stop", len(controlPIDs))
}

// Read-only process inspection is used solely to verify cleanup of owned test
// identities. No signal is ever sent through these persisted PID locators.
func processPresent(pid int) bool {
	if pid < 1 {
		return false
	}
	b, e := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "pid=").Output()
	return e == nil && strings.TrimSpace(string(b)) != ""
}

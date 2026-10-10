package ircd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seanheiney/keelmux/internal/adapters/nativepty"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/runtime"
)

type stoppedProofSupervisor struct {
	ref      domain.RuntimeRef
	alive    bool
	err      error
	inspects int
}

func (s *stoppedProofSupervisor) LaunchProcess(context.Context, domain.LaunchSpec) (domain.RuntimeRef, error) {
	return domain.RuntimeRef{}, ErrConflict
}
func (s *stoppedProofSupervisor) Lookup(context.Context, domain.ID, domain.ID) (runtime.Observation, error) {
	return runtime.Observation{}, ErrUnavailable
}
func (s *stoppedProofSupervisor) Inspect(context.Context, domain.RuntimeRef) (runtime.Observation, error) {
	s.inspects++
	return runtime.Observation{Ref: s.ref, Alive: s.alive}, s.err
}
func (s *stoppedProofSupervisor) Stop(context.Context, domain.RuntimeRef) error {
	return ErrConflict
}

func TestStoppedJournalRequiresCurrentDeathProof(t *testing.T) {
	op, execution := domain.NewID("operation"), domain.NewID("execution")
	ref := domain.RuntimeRef{Backend: "native", Socket: "/owned/native.sock", ExecutionID: execution, OperationID: op}
	r := lifecycleRecord{State: "stopped", Ref: ref, OperationID: op, Session: domain.Session{Execution: domain.Execution{ID: execution}}}
	for _, tc := range []struct {
		name  string
		alive bool
		err   error
	}{
		{"still alive", true, nil},
		{"legacy or unknown boot", false, nativepty.ErrUnavailable},
		{"confirmed dead", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			supervisor := &stoppedProofSupervisor{ref: ref, alive: tc.alive, err: tc.err}
			m := &Manager{supervisor: supervisor}
			status, err := m.inspectStatus(context.Background(), configRecord{}, r)
			if supervisor.inspects != 1 {
				t.Fatal("stopped journal bypassed exact execution inspection")
			}
			if tc.alive || tc.err != nil {
				if err != ErrUncertain || status.State != "uncertain" || status.Alive {
					t.Fatalf("stopped journal accepted without death proof: %+v %v", status, err)
				}
			} else if err != nil || status.State != "stopped" || status.Alive {
				t.Fatalf("confirmed dead stopped journal rejected: %+v %v", status, err)
			}
		})
	}
}

// Actual native supervisor and pinned Ergo prove that a prior false-stopped
// journal, including a legacy boot receipt, cannot authorize a replacement.
func TestRealNativeErgoStoppedJournalRequiresDeathProof(t *testing.T) {
	archive, helper := os.Getenv("KEELMUX_ERGO_ARCHIVE"), os.Getenv("KEELMUX_NATIVE_HELPER")
	if archive == "" || helper == "" {
		t.Skip("explicit pinned archive and built native helper required")
	}
	if !filepath.IsAbs(helper) {
		t.Fatal("helper path must be explicit absolute")
	}
	root, err := os.MkdirTemp("/tmp", "kmi-proof-")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := nativepty.NewWithOptions(filepath.Join(root, "native"), nativepty.Options{HelperPath: helper})
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewWithSupervisor(filepath.Join(root, "ircd"), backend)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.InstallArchive(ctx, f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Init(ctx, InitOptions{}); err != nil {
		t.Fatal(err)
	}
	c, err := m.readConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	operation := domain.NewID("operation")
	var original lifecycleRecord
	var originalBytes []byte
	var receiptPath string
	corrupted, stopped := false, false
	// Register before Start: a lost launch ACK is resolved only through this
	// exact operation's journal and supervisor, never a persisted PID signal.
	t.Cleanup(func() {
		// Restore only this isolated test's intentionally corrupted journal and
		// receipt, then stop with its original authenticated exact reference.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if corrupted && !stopped && (publish(receiptPath, originalBytes, 0600, true) != nil || m.saveLifecycle(original) != nil) {
			t.Errorf("owned test identity restoration failed; state retained at %s", root)
			return
		}
		current, err := m.loadLifecycle(c, m.lifecyclePath())
		if err != nil || current.OperationID != operation || m.Stop(cleanupCtx, current.Session.Execution.ID) != nil {
			t.Errorf("owned test cleanup unconfirmed; state retained at %s", root)
			return
		}
		current, err = m.loadLifecycle(c, m.lifecyclePath())
		if err != nil || current.OperationID != operation || current.State != "stopped" || current.Ref.PID < 1 {
			t.Errorf("owned test stop identity unconfirmed; state retained at %s", root)
			return
		}
		sum := sha256.Sum256([]byte(current.Ref.ExecutionID))
		path := filepath.Join(root, "native", hex.EncodeToString(sum[:])[:24]+".json")
		raw, err := readFile(path, 1<<20, 0600)
		var receipt struct {
			Ref           domain.RuntimeRef `json:"ref"`
			SupervisorPID int               `json:"supervisor_pid"`
		}
		if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.Ref != current.Ref || receipt.SupervisorPID < 1 {
			t.Errorf("owned test helper identity unconfirmed; state retained at %s", root)
			return
		}
		deadline := time.Now().Add(5 * time.Second)
		for (processPresent(current.Ref.PID) || processPresent(receipt.SupervisorPID)) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if processPresent(current.Ref.PID) || processPresent(receipt.SupervisorPID) {
			t.Errorf("owned test process cleanup unconfirmed; state retained at %s", root)
			return
		}
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	started, err := m.Start(ctx, operation)
	if err != nil || !started.Alive {
		t.Fatal("owned native start", err)
	}
	original, err = m.loadLifecycle(c, m.lifecyclePath())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(original.Ref.ExecutionID))
	receiptPath = filepath.Join(root, "native", hex.EncodeToString(sum[:])[:24]+".json")
	originalBytes, err = readFile(receiptPath, 1<<20, 0600)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(originalBytes, &fields) != nil {
		t.Fatal("invalid owned receipt")
	}
	for _, legacy := range []bool{false, true} {
		poisoned := original
		poisoned.State = "stopped"
		corrupted = true
		if legacy {
			poisoned.Ref.BootID = "darwin:1791497239:636714"
			fields["ref"], err = json.Marshal(poisoned.Ref)
			if err != nil {
				t.Fatal(err)
			}
			badBytes, err := json.Marshal(fields)
			if err != nil || publish(receiptPath, badBytes, 0600, true) != nil {
				t.Fatal("legacy test receipt mutation failed")
			}
		}
		if err := m.saveLifecycle(poisoned); err != nil {
			t.Fatal(err)
		}
		status, err := m.Status(ctx)
		if err != ErrUncertain || status.State != "uncertain" || status.Alive {
			t.Fatalf("false-stopped status accepted (legacy=%v): %+v %v", legacy, status, err)
		}
		if err := m.Stop(ctx, original.Ref.ExecutionID); err != ErrUncertain {
			t.Fatalf("false-stopped cleanup accepted (legacy=%v): %v", legacy, err)
		}
		replacementOp := domain.NewID("operation")
		if _, err := m.Start(ctx, replacementOp); err != ErrUncertain {
			t.Fatalf("false-stopped journal authorized replacement (legacy=%v): %v", legacy, err)
		}
		if _, err := m.Restart(ctx, original.Ref.ExecutionID, replacementOp); err != ErrUncertain {
			t.Fatalf("false-stopped journal authorized restart (legacy=%v): %v", legacy, err)
		}
		if _, err := os.Lstat(m.operationPath(replacementOp)); !os.IsNotExist(err) {
			t.Fatal("untrusted stopped journal created replacement intent")
		}
		after, err := m.loadLifecycle(c, m.lifecyclePath())
		if err != nil || after.Ref != poisoned.Ref || after.State != poisoned.State || after.OperationID != poisoned.OperationID {
			t.Fatal("untrusted stopped journal was rewritten", err)
		}
		if publish(receiptPath, originalBytes, 0600, true) != nil {
			t.Fatal("test receipt restore failed")
		}
		o, err := backend.Inspect(ctx, original.Ref)
		if err != nil || !o.Alive || o.Ref != original.Ref {
			t.Fatal("untrusted cleanup affected original native execution", err)
		}
	}
	if m.saveLifecycle(original) != nil || m.Stop(ctx, original.Ref.ExecutionID) != nil {
		t.Fatal("authenticated real stop failed")
	}
	stopped = true
	if m.Stop(ctx, original.Ref.ExecutionID) != nil {
		t.Fatal("authenticated real stop lost idempotence")
	}
	status, err := m.Status(ctx)
	if err != nil || status.State != "stopped" || status.Alive {
		t.Fatal("confirmed native terminal tombstone rejected", err)
	}
	t.Log("stable and legacy false-stopped journals refused status/stop/start/restart; no replacement intent; original owned execution survived; authenticated stop remained idempotent")
}

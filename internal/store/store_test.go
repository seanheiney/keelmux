package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state", "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func recordTest(t *testing.T, s *Store) (domain.Session, domain.Operation) {
	t.Helper()
	v := domain.NewSession("host", "Test", t.TempDir(), "fixture", "v1")
	op, _, err := s.Record(context.Background(), domain.Operation{ID: "op_test", Kind: "launch", SessionID: v.ID, ExecutionID: v.Execution.ID}, []byte(`{"argv":["fixture"]}`), &v)
	if err != nil {
		t.Fatal(err)
	}
	return v, op
}

func TestPrivateStoreAndSingleAuthority(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "registry.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + ".lock"} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s mode %o", p, info.Mode().Perm())
		}
	}
	if second, err := Open(path); err == nil {
		second.Close()
		t.Fatal("second authority opened the same registry")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	public := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(filepath.Join(public, "registry.db")); err == nil {
		s.Close()
		t.Fatal("accepted public state directory")
	}
	linkDir := t.TempDir()
	if err := os.Chmod(linkDir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(linkDir, "link.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(link); err == nil {
		s.Close()
		t.Fatal("accepted symlink database")
	}
}

func TestAuthorityCanRestartWhileLaunchedChildSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "registry.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	// Native runtimes may outlive the daemon. A descriptor inherited by this
	// unrelated child must not keep the old daemon's authority lock alive.
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		child.Process.Kill()
		child.Wait()
	})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := Open(path)
	if err != nil {
		t.Fatalf("surviving native child blocked replacement authority: %v", err)
	}
	replacement.Close()
}

func TestIntentAttemptOutcomeAndOrderedJournal(t *testing.T) {
	s := openTest(t)
	v, op := recordTest(t, s)
	ctx := context.Background()
	if op.Outcome != domain.Recorded {
		t.Fatalf("intent=%s", op.Outcome)
	}
	var wg sync.WaitGroup
	results := make(chan bool, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, claimed, err := s.Attempt(ctx, op.ID)
			if err != nil {
				t.Error(err)
			}
			results <- claimed
		}()
	}
	wg.Wait()
	close(results)
	claims := 0
	for claimed := range results {
		if claimed {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("external effect claims=%d", claims)
	}
	v.Execution.State = "outcome_uncertain"
	if _, err := s.Resolve(ctx, op.ID, domain.Uncertain, "ack lost", &v); err != nil {
		t.Fatal(err)
	}
	v.Execution.State = "running"
	if _, err := s.Resolve(ctx, op.ID, domain.Completed, "exact runtime found", &v); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(ctx, op.ID, domain.Attempted, "replay", nil); err == nil {
		t.Fatal("completed operation replayed")
	}
	events, err := s.Events(ctx, v.ThreadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("events=%d", len(events))
	}
	for i, e := range events {
		if e.Sequence != int64(i+1) || e.ThreadID != v.ThreadID || e.SessionID != v.ID {
			t.Fatalf("bad event %#v", e)
		}
	}
	tail, err := s.Events(ctx, v.ThreadID, 2)
	if err != nil || len(tail) != 2 {
		t.Fatalf("cursor events=%d err=%v", len(tail), err)
	}
	_, _, err = s.Record(ctx, op, []byte(`{"argv":["different"]}`), nil)
	var collision domain.ConflictError
	if !errors.As(err, &collision) {
		t.Fatalf("collision err=%v", err)
	}
}

func TestCrashHelper(t *testing.T) {
	path := os.Getenv("BP_TEST_CRASH_STORE")
	if path == "" {
		return
	}
	s, err := Open(path)
	if err != nil {
		panic(err)
	}
	v := domain.NewSession("host", "Crash", filepath.Dir(path), "fixture", "v1")
	v.ID = "session_crash"
	v.Execution.ID = "execution_crash"
	v.ThreadID = "thread_crash"
	op, _, err := s.Record(context.Background(), domain.Operation{ID: "op_crash", Kind: "launch", SessionID: v.ID, ExecutionID: v.Execution.ID}, []byte("crash-request"), &v)
	if err != nil {
		panic(err)
	}
	if _, _, err = s.Attempt(context.Background(), op.ID); err != nil {
		panic(err)
	}
	// Deliberately bypass Close and all deferred cleanup, as a dying daemon does.
	os.Exit(73)
}

func TestCommittedIntentSurvivesAbruptProcessExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "registry.db")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestCrashHelper$")
	cmd.Env = append(os.Environ(), "BP_TEST_CRASH_STORE="+path)
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("helper exit=%v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	op, p, err := s.Operation(context.Background(), "op_crash")
	if err != nil {
		t.Fatal(err)
	}
	if op.Outcome != domain.Attempted || string(p) != "crash-request" {
		t.Fatalf("lost durable attempt: %#v %q", op, p)
	}
	events, err := s.Events(context.Background(), "thread_crash", 0)
	if err != nil || len(events) != 2 {
		t.Fatalf("crash journal=%v err=%v", events, err)
	}
}

func TestLeaseExactReleaseReadOnlyAndPersistence(t *testing.T) {
	s := openTest(t)
	v, _ := recordTest(t, s)
	ctx := context.Background()
	l := domain.Lease{SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: 1, OperationID: "acquire", ClientID: "client", Token: "private-token"}
	if _, err := s.Acquire(ctx, l, false, []byte("request")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Acquire(ctx, domain.Lease{SessionID: v.ID, ExecutionID: v.Execution.ID, Generation: 1, OperationID: "acquire", ClientID: "client", Token: "new-unused-token"}, false, []byte("request")); err != nil || got != l {
		t.Fatalf("idempotent grant=%#v err=%v", got, err)
	}
	other := l
	other.OperationID = "other"
	other.Token = "other-token"
	if _, err := s.Acquire(ctx, other, false, []byte("other")); err == nil {
		t.Fatal("two writer leases admitted")
	}
	read := other
	read.OperationID = "read"
	if _, err := s.Acquire(ctx, read, true, []byte("read")); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, read); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.Lease){func(l *domain.Lease) { l.Token = "forged" }, func(l *domain.Lease) { l.ClientID = "forged" }, func(l *domain.Lease) { l.Generation++ }, func(l *domain.Lease) { l.ExecutionID = "replacement" }} {
		bad := l
		mutate(&bad)
		if err := s.Release(ctx, bad); err == nil {
			t.Fatalf("released forged lease %#v", bad)
		}
	}
	if _, err := s.Acquire(ctx, other, false, []byte("other")); err == nil {
		t.Fatal("read-only release freed writer")
	}
	if err := s.Release(ctx, l); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, l); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(ctx, l, false, []byte("request")); err == nil {
		t.Fatal("released operation silently reacquired")
	}
	if _, err := s.Acquire(ctx, other, false, []byte("other")); err != nil {
		t.Fatal(err)
	}
}

func TestOperationCannotActOnLaterGenerationOfSameExecution(t *testing.T) {
	for _, attempted := range []bool{false, true} {
		t.Run(fmt.Sprint(attempted), func(t *testing.T) {
			s := openTest(t)
			v, op := recordTest(t, s)
			ctx := context.Background()
			if attempted {
				if _, _, err := s.Attempt(ctx, op.ID); err != nil {
					t.Fatal(err)
				}
			}
			v.Execution.Generation++
			if _, err := s.db.Exec(`UPDATE sessions SET data=? WHERE id=?`, encoded(v), v.ID); err != nil {
				t.Fatal(err)
			}
			if attempted {
				if _, err := s.Resolve(ctx, op.ID, domain.Completed, "delayed outcome", &v); err == nil {
					t.Fatal("old outcome changed a new generation")
				}
			} else {
				if _, _, err := s.Attempt(ctx, op.ID); err == nil {
					t.Fatal("old operation attempted against a new generation")
				}
			}
		})
	}
}

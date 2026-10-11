package nativepty

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	ps "github.com/seanheiney/keelmux/internal/primarystdio"
)

func terminalStorageFixture(t *testing.T) (*stdioEngine, stdioState) {
	t.Helper()
	s := readyStdioReceiver(t)
	prepareSyntheticStopReceipt(t, s)
	s.r.Stage = "stopped"
	if e := s.b.writeReceipt(s.r); e != nil {
		t.Fatal(e)
	}
	state, e := s.b.readStdio(s.r)
	if e != nil {
		t.Fatal(e)
	}
	return s, state
}

func TestStdioTerminalReconciliationWaitsForPersistence(t *testing.T) {
	s, state := terminalStorageFixture(t)
	entered, release, result := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() {
		first := true
		result <- s.b.confirmTerminalStdio(context.Background(), s.r, state, func(f *os.File) error {
			if first {
				first = false
				close(entered)
				<-release
			}
			return f.Sync()
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("owned sync was not attempted")
	}
	select {
	case e := <-result:
		t.Fatal("terminal bytes acknowledged before persistence confirmation", e)
	default:
	}
	unblock()
	select {
	case e := <-result:
		if e != nil {
			t.Fatal("confirmed terminal persistence unavailable", e)
		}
	case <-time.After(time.Second):
		t.Fatal("owned sync did not finish")
	}
}

func TestStdioTerminalReconciliationSyncFailuresRefuseWithoutRewrite(t *testing.T) {
	for failAt, phase := range []string{"receipt", "spool", "directory"} {
		t.Run(phase, func(t *testing.T) {
			s, state := terminalStorageFixture(t)
			paths := []string{s.b.receiptPath(s.r.Ref.ExecutionID), s.b.stdioPath(s.r.Ref.ExecutionID)}
			before := make([][]byte, len(paths))
			for i, path := range paths {
				before[i], _ = os.ReadFile(path)
			}
			calls := 0
			e := s.b.confirmTerminalStdio(context.Background(), s.r, state, func(f *os.File) error {
				calls++
				if calls == failAt+1 {
					return os.ErrPermission
				}
				return f.Sync()
			})
			if !errors.Is(e, ps.ErrStorage) || calls != failAt+1 {
				t.Fatal("failed persistence confirmation acknowledged", e, calls)
			}
			for i, path := range paths {
				after, _ := os.ReadFile(path)
				if string(after) != string(before[i]) {
					t.Fatal("persistence reconciliation rewrote evidence")
				}
			}
			if e = s.b.confirmTerminalStdio(context.Background(), s.r, state, (*os.File).Sync); e != nil {
				t.Fatal("later independent persistence confirmation failed", e)
			}
		})
	}
}

func TestStdioTerminalReconciliationRejectsChangedEvidence(t *testing.T) {
	for _, kind := range []string{"receipt_replace", "spool_replace", "spool_symlink", "spool_content", "directory_replace"} {
		t.Run(kind, func(t *testing.T) {
			s, state := terminalStorageFixture(t)
			changed := false
			e := s.b.confirmTerminalStdio(context.Background(), s.r, state, func(f *os.File) error {
				if !changed {
					changed = true
					path := s.b.stdioPath(s.r.Ref.ExecutionID)
					if kind == "receipt_replace" {
						path = s.b.receiptPath(s.r.Ref.ExecutionID)
					}
					if kind == "directory_replace" {
						original := s.b.state + ".original"
						t.Cleanup(func() { _ = os.RemoveAll(original) })
						if e := os.Rename(s.b.state, original); e != nil {
							return e
						}
						if e := os.Mkdir(s.b.state, 0700); e != nil {
							return e
						}
						for _, name := range []string{filepath.Base(s.b.receiptPath(s.r.Ref.ExecutionID)), filepath.Base(path)} {
							raw, e := os.ReadFile(filepath.Join(original, name))
							if e != nil {
								return e
							}
							if e = os.WriteFile(filepath.Join(s.b.state, name), raw, 0600); e != nil {
								return e
							}
						}
					} else if kind == "spool_content" {
						if e := os.WriteFile(path, []byte("changed-in-place"), 0600); e != nil {
							return e
						}
					} else {
						raw, e := os.ReadFile(path)
						if e != nil {
							return e
						}
						if e = os.Rename(path, path+".original"); e != nil {
							return e
						}
						if kind == "spool_symlink" {
							e = os.Symlink(path+".original", path)
						} else {
							e = os.WriteFile(path, raw, 0600)
						}
						if e != nil {
							return e
						}
					}
				}
				return f.Sync()
			})
			if !changed || !errors.Is(e, ps.ErrStorage) {
				t.Fatal("changed evidence accepted after sync", kind, e)
			}
		})
	}
}

func TestStdioTerminalReconciliationCancellationRefusesSuccess(t *testing.T) {
	s, state := terminalStorageFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := s.b.confirmTerminalStdio(ctx, s.r, state, func(f *os.File) error {
		cancel()
		return f.Sync()
	})
	if !errors.Is(e, ps.ErrUncertain) {
		t.Fatal("canceled persistence confirmation acknowledged", e)
	}
}

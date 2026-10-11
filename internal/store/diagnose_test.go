package store

import (
	"bytes"
	"context"
	"errors"
	"github.com/seanheiney/keelmux/internal/diagnosticfs"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestDiagnoseOfflineRegistryUnchanged(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	s, e := Open(filepath.Join(d, "registry.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	recordTest(t, s)
	dir, e := diagnosticfs.OpenDir(d)
	if e != nil {
		t.Fatal(e)
	}
	defer dir.Close()
	if _, e = DiagnoseExisting(context.Background(), dir); !errors.Is(e, ErrDiagnosticBusy) {
		t.Fatal("opened live authority", e)
	}
	s.Close()
	before, _ := os.ReadFile(filepath.Join(d, "registry.sqlite"))
	info, _ := os.Stat(filepath.Join(d, "registry.sqlite"))
	entries, _ := os.ReadDir(d)
	got, e := DiagnoseExisting(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	if got.Sessions != 1 || got.LastEvent.IsZero() {
		t.Fatalf("missing durable observation: %+v", got)
	}
	after, _ := os.ReadFile(filepath.Join(d, "registry.sqlite"))
	now, _ := os.Stat(filepath.Join(d, "registry.sqlite"))
	next, _ := os.ReadDir(d)
	if !bytes.Equal(before, after) || !info.ModTime().Equal(now.ModTime()) || info.Mode() != now.Mode() || len(entries) != len(next) {
		t.Fatal("diagnostics changed registry")
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		p := filepath.Join(d, "registry.sqlite"+suffix)
		os.WriteFile(p, nil, 0600)
		if _, e := DiagnoseExisting(context.Background(), dir); e == nil {
			t.Fatal("accepted sidecar", suffix)
		}
		os.Remove(p)
	}
	os.Chmod(filepath.Join(d, "registry.sqlite.lock"), 0644)
	if _, e := DiagnoseExisting(context.Background(), dir); e == nil {
		t.Fatal("accepted public lock")
	}
	st, _ := os.Stat(filepath.Join(d, "registry.sqlite.lock"))
	if st.Mode().Perm() != 0644 {
		t.Fatal("repaired lock permissions")
	}
}

func TestDiagnoseCorruptRegistryNeverRepairs(t *testing.T) {
	for _, kind := range []string{"missing_lock", "corrupt", "corrupt_locked", "future_schema", "lost_channels", "linked_db", "linked_lock"} {
		t.Run(kind, func(t *testing.T) {
			d := filepath.Join(t.TempDir(), "state")
			path := filepath.Join(d, "registry.sqlite")
			s, e := Open(path)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "future_schema" {
				s.db.Exec("PRAGMA user_version=99")
			}
			if kind == "lost_channels" {
				s.db.Exec("DROP TABLE channel_bindings")
			}
			s.Close()
			switch kind {
			case "missing_lock":
				os.Remove(path + ".lock")
			case "corrupt", "corrupt_locked":
				os.WriteFile(path, []byte("private-corrupt-token"), 0600)
			case "linked_db":
				os.Rename(path, path+".real")
				os.Symlink(path+".real", path)
			case "linked_lock":
				os.Rename(path+".lock", path+".lock-real")
				os.Symlink(path+".lock-real", path+".lock")
			}
			dir, e := diagnosticfs.OpenDir(d)
			if e != nil {
				t.Fatal(e)
			}
			defer dir.Close()
			if kind == "corrupt_locked" {
				lock, e := dir.Open("registry.sqlite.lock", 0600, 1<<20)
				if e != nil {
					t.Fatal(e)
				}
				defer lock.Close()
				if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
					t.Fatal(e)
				}
				defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			}
			before, _ := os.ReadFile(path)
			entries, _ := os.ReadDir(d)
			if _, e = DiagnoseExisting(context.Background(), dir); e == nil {
				t.Fatal("accepted damaged state")
			}
			if kind == "corrupt_locked" && !errors.Is(e, ErrDiagnosticBusy) {
				t.Fatal("inspected a locked corrupt registry", e)
			}
			after, _ := os.ReadFile(path)
			next, _ := os.ReadDir(d)
			if !bytes.Equal(before, after) || len(entries) != len(next) {
				t.Fatal("repaired state")
			}
		})
	}
}

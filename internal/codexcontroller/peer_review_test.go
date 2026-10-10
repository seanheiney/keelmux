package codexcontroller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestJournalRejectsNoncanonicalJSON(t *testing.T) {
	for name, data := range map[string]string{
		"duplicate":         `{"Version":1,"Version":2,"Entries":{}}`,
		"case_alias":        `{"version":2,"Entries":{}}`,
		"unknown":           `{"Version":2,"Entries":{},"Unknown":true}`,
		"duplicate_entries": `{"Version":2,"Entries":{"lost":{}},"Entries":null,"Entries":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			f.j.Close()
			if err := os.WriteFile(filepath.Join(f.j.dir, "primaries.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			j, err := OpenJournal(f.j.dir)
			if j != nil {
				j.Close()
			}
			if !errors.Is(err, ErrStorage) {
				t.Fatalf("accepted ambiguous journal: %v", err)
			}
		})
	}
}

func TestJournalPrivateDescriptors(t *testing.T) {
	for _, name := range []string{"controller.lock", "primaries.json"} {
		for _, kind := range []string{"symlink", "fifo", "hardlink", "public_file"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				f := setup(t)
				f.j.Close()
				path := filepath.Join(f.j.dir, name)
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				target := filepath.Join(f.r.Workspace, "target")
				if err := os.WriteFile(target, []byte(`{"Version":2,"Entries":{}}`), 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "symlink":
					err = os.Symlink(target, path)
				case "fifo":
					err = unix.Mkfifo(path, 0600)
				case "hardlink":
					err = os.Link(target, path)
				case "public_file":
					err = os.WriteFile(path, nil, 0644)
				}
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					j, err := OpenJournal(f.j.dir)
					if j != nil {
						j.Close()
					}
					done <- err
				}()
				select {
				case err := <-done:
					if !errors.Is(err, ErrStorage) {
						t.Fatalf("unsafe descriptor accepted: %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("special file blocked journal open")
				}
			})
		}
	}
}

func TestReplacedJournalDirectoryRefusesEffects(t *testing.T) {
	f := setup(t)
	if err := os.Rename(f.j.dir, f.j.dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.j.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Start(context.Background(), f.r); !errors.Is(err, ErrStorage) || f.s.launches != 0 {
		t.Fatalf("replaced journal directory accepted: %v", err)
	}
}

func TestReplacedJournalLockRefusesEffects(t *testing.T) {
	f := setup(t)
	path := filepath.Join(f.j.dir, "controller.lock")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Start(context.Background(), f.r); !errors.Is(err, ErrStorage) || f.s.launches != 0 {
		t.Fatalf("replaced journal lock accepted: %v", err)
	}
}

func TestJournalFormattingDoesNotExportCapability(t *testing.T) {
	f := setup(t)
	f.start(t)
	capability := f.j.entries[f.r.Target.ExecutionID].Capability
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, f.j), capability) {
			t.Fatalf("journal formatting leaked capability with %s", format)
		}
	}
}

func TestNativeHomeAliasCannotBypassLiveThreadFence(t *testing.T) {
	f := setup(t)
	f.start(t)
	alias := filepath.Join(f.r.Workspace, "home-alias")
	if err := os.Symlink(f.o.CodexHome, alias); err != nil {
		t.Fatal(err)
	}
	opts := f.o
	opts.RealmRevision = "realm-2"
	opts.CodexHome = alias
	c, err := New(f.j, f.s, f.u, f.g, opts)
	if err != nil {
		t.Fatal(err)
	}
	r := f.r
	r.OperationID = "other-start"
	r.Target.SessionID = "other-session"
	r.Target.ExecutionID = "other-execution"
	r.RealmRevision = opts.RealmRevision
	if _, err := c.Start(context.Background(), r); !errors.Is(err, ErrConflict) || f.s.launches != 1 {
		t.Fatalf("home alias bypassed live-thread fence: err=%v launches=%d", err, f.s.launches)
	}
}

func TestReplacedNativeHomeRefusesEffects(t *testing.T) {
	f := setup(t)
	home := filepath.Join(f.r.Workspace, "native-home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	opts := f.o
	opts.CodexHome = home
	c, err := New(f.j, f.s, f.u, f.g, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(home, home+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(context.Background(), f.r); !errors.Is(err, ErrConflict) || f.s.launches != 0 {
		t.Fatalf("replaced native home accepted: %v", err)
	}
}

func TestExecutableFIFORefusesWithoutBlocking(t *testing.T) {
	f := setup(t)
	if err := os.Remove(f.o.Binary); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(f.o.Binary, 0700); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := f.c.Start(context.Background(), f.r); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnavailable) || f.s.launches != 0 {
			t.Fatalf("executable FIFO accepted: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		// Unblock the old reader before failing so this negative regression
		// never leaves a goroutine holding the journal/common gate in cleanup.
		fd, err := unix.Open(f.o.Binary, unix.O_WRONLY|unix.O_NONBLOCK, 0)
		if err == nil {
			unix.Close(fd)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("could not release blocked executable FIFO")
		}
		t.Fatal("executable FIFO blocked while holding lifecycle gate")
	}
}

package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
)

const readOnlyHost domain.ID = "host_0123456789abcdef0123456789abcdef"

func hostReadDirectory(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func putReadOnlyHost(t *testing.T, dir string, raw []byte) string {
	t.Helper()
	path := filepath.Join(dir, "host-id")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadHostIDAbsentDoesNotCreateStateOrIdentity(t *testing.T) {
	parent := hostReadDirectory(t)
	absent := filepath.Join(parent, "absent")
	if _, err := ReadHostID(absent); !os.IsNotExist(err) {
		t.Fatalf("%v", err)
	}
	if _, err := os.Lstat(absent); !os.IsNotExist(err) {
		t.Fatal("absent state created", err)
	}
	if _, err := ReadHostID(parent); !os.IsNotExist(err) {
		t.Fatalf("%v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("read created state", err)
	}
}

func TestReadHostIDExistingIdentityIsUnchanged(t *testing.T) {
	dir := hostReadDirectory(t)
	path := putReadOnlyHost(t, dir, []byte(readOnlyHost))
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeDir, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ReadHostID(dir)
	if err != nil || id != readOnlyHost {
		t.Fatalf("%s %v", id, err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	afterDir, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, []byte(readOnlyHost)) || !os.SameFile(before, after) || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) || !os.SameFile(beforeDir, afterDir) || !beforeDir.ModTime().Equal(afterDir.ModTime()) {
		t.Fatal("read changed existing private identity", err)
	}
}

func TestReadHostIDMalformedOrOversizedStaysUntouched(t *testing.T) {
	values := [][]byte{nil, []byte("host_"), []byte(strings.ToUpper(string(readOnlyHost))), []byte(string(readOnlyHost) + "\n"), bytes.Repeat([]byte("x"), 257)}
	for i, raw := range values {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			dir := hostReadDirectory(t)
			path := putReadOnlyHost(t, dir, raw)
			if id, err := ReadHostID(dir); err == nil || id != "" {
				t.Fatalf("malformed identity accepted: %q %v", id, err)
			}
			got, err := os.ReadFile(path)
			info, statErr := os.Lstat(path)
			if err != nil || statErr != nil || !bytes.Equal(raw, got) || info.Mode().Perm() != 0600 {
				t.Fatal("invalid identity repaired", err, statErr)
			}
		})
	}
}

func TestReadHostIDRefusesUnsafeFilesAndDirectories(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"public file", func(t *testing.T, dir string) {
			path := putReadOnlyHost(t, dir, []byte(readOnlyHost))
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{"public directory", func(t *testing.T, dir string) {
			putReadOnlyHost(t, dir, []byte(readOnlyHost))
			if err := os.Chmod(dir, 0755); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink file", func(t *testing.T, dir string) {
			source := putReadOnlyHost(t, hostReadDirectory(t), []byte(readOnlyHost))
			if err := os.Symlink(source, filepath.Join(dir, "host-id")); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink file", func(t *testing.T, dir string) {
			source := putReadOnlyHost(t, hostReadDirectory(t), []byte(readOnlyHost))
			if err := os.Link(source, filepath.Join(dir, "host-id")); err != nil {
				t.Fatal(err)
			}
		}},
		{"FIFO", func(t *testing.T, dir string) {
			if err := syscall.Mkfifo(filepath.Join(dir, "host-id"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := hostReadDirectory(t)
			c.setup(t, dir)
			beforeDir, err := os.Lstat(dir)
			if err != nil {
				t.Fatal(err)
			}
			beforeFile, err := os.Lstat(filepath.Join(dir, "host-id"))
			if err != nil {
				t.Fatal(err)
			}
			if id, err := ReadHostID(dir); err == nil || id != "" {
				t.Fatalf("unsafe identity accepted: %q %v", id, err)
			}
			afterDir, _ := os.Lstat(dir)
			afterFile, _ := os.Lstat(filepath.Join(dir, "host-id"))
			if !os.SameFile(beforeDir, afterDir) || beforeDir.Mode() != afterDir.Mode() || !os.SameFile(beforeFile, afterFile) || beforeFile.Mode() != afterFile.Mode() {
				t.Fatal("unsafe state silently changed")
			}
		})
	}
}

func TestReadHostIDRefusesStateSymlink(t *testing.T) {
	dir := hostReadDirectory(t)
	putReadOnlyHost(t, dir, []byte(readOnlyHost))
	parent := hostReadDirectory(t)
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if id, err := ReadHostID(alias); err == nil || id != "" {
		t.Fatalf("state alias accepted: %s %v", id, err)
	}
	if id, err := ReadHostID(dir); err != nil || id != readOnlyHost {
		t.Fatalf("alias refusal changed real identity: %s %v", id, err)
	}
}

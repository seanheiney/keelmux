package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDaemonLogCannotFollowLinksOrUnsafeFiles(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(root, "public")
	if err := os.WriteFile(public, []byte("preserve"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0644); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, public, fifo, root} {
		if f, err := OpenPrivateLog(path); err == nil {
			_ = f.Close()
			t.Fatalf("unsafe daemon log accepted: %s", path)
		}
	}
	data, err := os.ReadFile(victim)
	if err != nil || string(data) != "preserve" {
		t.Fatal("log refusal changed symlink target")
	}
	path := filepath.Join(root, "owned")
	f, err := OpenPrivateLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("first\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	f, err = OpenPrivateLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "first\nsecond\n" {
		t.Fatal("owned daemon log lost existing content")
	}
	i, err := os.Stat(path)
	if err != nil || i.Mode().Perm() != 0600 {
		t.Fatal("daemon log was not private")
	}
}

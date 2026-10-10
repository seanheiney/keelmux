package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPrivateCredentialReference(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "token")
	if err := os.WriteFile(secret, []byte("owned-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b, err := ReadPrivateSecret(secret)
	if err != nil || string(b) != "owned-secret\n" {
		t.Fatalf("read = %q, %v", b, err)
	}
	if err := os.Chmod(secret, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateSecret(secret); err != nil {
		t.Fatalf("private read-only file: %v", err)
	}
	if err := os.Chmod(secret, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateSecret(secret); err == nil {
		t.Fatal("public credential accepted")
	}
	if info, _ := os.Stat(secret); info.Mode().Perm() != 0644 {
		t.Fatal("reader changed foreign permissions")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(dir, "large")
	if err := os.WriteFile(large, []byte(strings.Repeat("x", 8193)), 0600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"relative", dir, link, fifo, large, empty, filepath.Join(dir, "missing")} {
		if _, err := ReadPrivateSecret(path); err == nil {
			t.Fatalf("unsafe reference accepted: %s", path)
		}
	}
}

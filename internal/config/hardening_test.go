package config

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEveryCreatedDirectorySyncsItsParent(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "one", "two", "three")
	var parents []uint64
	sync := func(fd int) error {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return err
		}
		parents = append(parents, uint64(stat.Ino))
		return unix.Fsync(fd)
	}
	if err := ensurePrivateDirectory(path, sync); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{base, filepath.Join(base, "one"), filepath.Join(base, "one", "two")} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		inode := uint64(info.Sys().(*syscall.Stat_t).Ino)
		found := false
		for _, synced := range parents {
			if inode == synced {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("new child namespace parent never synced: %s", dir)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("new state not private", err)
	}
}
func TestDirectoryRetryConfirmsInterruptedParentSync(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "one", "two")
	failed := false
	sync := func(fd int) error {
		if !failed {
			if _, err := os.Stat(filepath.Join(base, "one")); err == nil {
				failed = true
				return errors.New("interrupted parent sync")
			}
		}
		return unix.Fsync(fd)
	}
	if err := ensurePrivateDirectory(path, sync); err == nil || !failed {
		t.Fatal("creation did not stop on sync failure")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created deeper state after parent sync failed", err)
	}
	if err := EnsurePrivateDirectory(path); err != nil {
		t.Fatal("failed mkdir boundary could not be retried", err)
	}
}
func TestPrivateDirectoryRejectsLeafSymlinkAndPublicPermissions(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDirectory(alias); err == nil {
		t.Fatal("private leaf symlink accepted")
	}
	if err := os.Chmod(real, 0755); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDirectory(real); err == nil {
		t.Fatal("public leaf accepted")
	}
	info, _ := os.Stat(real)
	if info.Mode().Perm() != 0755 {
		t.Fatal("rejected leaf was silently changed")
	}
}
func TestHostIdentityAndLifetimeLockRejectSymlinks(t *testing.T) {
	base := t.TempDir()
	p := Paths{Dir: base}
	other := filepath.Join(base, "other")
	if err := os.WriteFile(other, []byte("host_other"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"host-id", "daemon.lock"} {
		path := filepath.Join(base, name)
		if err := os.Symlink(other, path); err != nil {
			t.Fatal(err)
		}
		if name == "host-id" {
			if _, err := p.HostID(); err == nil {
				t.Fatal("symlink host identity accepted")
			}
		} else {
			if f, err := p.Lock(); err == nil {
				_ = f.Close()
				t.Fatal("symlink authority lock accepted")
			}
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if data, err := os.ReadFile(other); err != nil || string(data) != "host_other" {
		t.Fatal("unowned target changed", err)
	}
}
func TestLifetimeLockExecHelper(t *testing.T) {
	if os.Getenv("BP_CONFIG_LOCK_CHILD") != "1" {
		return
	}
	p := Paths{Dir: os.Getenv("BP_CONFIG_LOCK_DIR")}
	f, err := p.Lock()
	if err != nil {
		os.Exit(72)
	}
	binary, err := os.Executable()
	if err != nil {
		os.Exit(73)
	}
	// Exec keeps this process alive while discarding the CLOEXEC lock descriptor.
	_ = f
	err = syscall.Exec(binary, []string{binary, "-test.run=^TestLifetimeLockExecReplacement$"}, append(os.Environ(), "BP_CONFIG_LOCK_REPLACEMENT=1"))
	runtime.KeepAlive(f)
	if err != nil {
		os.Exit(74)
	}
}
func TestLifetimeLockExecReplacement(t *testing.T) {
	if os.Getenv("BP_CONFIG_LOCK_REPLACEMENT") != "1" {
		return
	}
	p := Paths{Dir: os.Getenv("BP_CONFIG_LOCK_DIR")}
	f, err := p.Lock()
	if err != nil {
		t.Fatal("lifetime lock inherited into replacement process", err)
	}
	_ = f.Close()
}
func TestLifetimeLockDoesNotSurviveExec(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cmd := exec.Command(binary, "-test.run=^TestLifetimeLockExecHelper$")
	cmd.Env = append(os.Environ(), "BP_CONFIG_LOCK_CHILD=1", "BP_CONFIG_LOCK_DIR="+dir)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("exec replacement retained lock: %v %s", err, data)
	}
}

func TestMalformedHostIdentityRequiresRepairWithoutReplacement(t *testing.T) {
	values := []string{"", "host_", "host_0123", "host_" + strings.Repeat("a", 33), "host_" + strings.Repeat("A", 32), "host_" + strings.Repeat("g", 32), "legacy_host", "host_" + strings.Repeat("a", 31) + "\n", "host_" + strings.Repeat("a", 31) + "\x00"}
	for i, value := range values {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "host-id")
			if err := os.WriteFile(path, []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
			p := Paths{Dir: dir}
			id, err := p.HostID()
			if err == nil || !strings.Contains(err.Error(), "repair required") || id != "" {
				t.Fatal("malformed identity accepted or silently replaced", id, err)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(data, []byte(value)) {
				t.Fatal("malformed legacy identity changed", readErr)
			}
		})
	}
}
func TestHostIdentityPartialWriteCrashHelper(t *testing.T) {
	if os.Getenv("BP_HOST_ID_PARTIAL_CHILD") != "1" {
		return
	}
	path := filepath.Join(os.Getenv("BP_HOST_ID_PARTIAL_DIR"), "host-id")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(74)
	}
	if _, err = f.WriteString("host_0123456789abcdef"); err != nil {
		os.Exit(75)
	}
	// Abrupt exit before the remaining identity bytes, sync or close.
	os.Exit(73)
}
func TestAbruptPartialHostIdentityWriteFailsClosed(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cmd := exec.Command(binary, "-test.run=^TestHostIdentityPartialWriteCrashHelper$")
	cmd.Env = append(os.Environ(), "BP_HOST_ID_PARTIAL_CHILD=1", "BP_HOST_ID_PARTIAL_DIR="+dir)
	data, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("partial-write crash boundary missing: %v %s", err, data)
	}
	p := Paths{Dir: dir}
	if id, err := p.HostID(); err == nil || id != "" || !strings.Contains(err.Error(), "repair required") {
		t.Fatal("truncated authority was accepted after process exit", id, err)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "host-id"))
	if err != nil || string(saved) != "host_0123456789abcdef" {
		t.Fatal("partial identity was overwritten", err)
	}
}

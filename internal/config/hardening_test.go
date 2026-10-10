package config

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

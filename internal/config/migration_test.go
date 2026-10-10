package config

import (
	"os"
	"path/filepath"
	"testing"
)

func isolatedHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "km-state-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("KEELMUX_STATE_DIR", "")
	t.Setenv("BP_STATE_DIR", "")
	return home
}

func TestRenameKeepsLegacyHostAndWriterAuthority(t *testing.T) {
	home := isolatedHome(t)
	legacy := filepath.Join(home, ".local", "state", "agent-backplane")
	p, err := Resolve(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	host, err := p.HostID()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := p.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	renamed, err := Resolve("")
	if err != nil || renamed.Dir != legacy {
		t.Fatalf("rename changed authority: %+v %v", renamed, err)
	}
	if err := renamed.Ensure(); err != nil {
		t.Fatal(err)
	}
	if next, err := renamed.HostID(); err != nil || next != host {
		t.Fatal("rename changed stable host", err)
	}
	if other, err := renamed.Lock(); err == nil {
		other.Close()
		t.Fatal("rename bypassed the live prior daemon's lock")
	}
	if _, err := os.Lstat(filepath.Join(home, ".local", "state", "keelmux")); !os.IsNotExist(err) {
		t.Fatal("rename created a second state directory")
	}
}

func TestNewStateAndExplicitEnvironmentPriority(t *testing.T) {
	home := isolatedHome(t)
	p, err := Resolve("")
	if err != nil || p.Dir != filepath.Join(home, ".local", "state", "keelmux") {
		t.Fatalf("fresh state: %+v %v", p, err)
	}
	t.Setenv("BP_STATE_DIR", filepath.Join(home, "old-env"))
	p, err = Resolve("")
	if err != nil || p.Dir != filepath.Join(home, "old-env") {
		t.Fatal("legacy environment was not retained", err)
	}
	t.Setenv("KEELMUX_STATE_DIR", filepath.Join(home, "new-env"))
	p, err = Resolve("")
	if err != nil || p.Dir != filepath.Join(home, "new-env") {
		t.Fatal("canonical environment did not take precedence", err)
	}
	p, err = Resolve(filepath.Join(home, "explicit"))
	if err != nil || p.Dir != filepath.Join(home, "explicit") {
		t.Fatal("explicit state did not take precedence", err)
	}
}

func TestInvalidLegacyStateCannotCreateReplacementAuthority(t *testing.T) {
	for _, kind := range []string{"symlink", "public", "regular", "corrupt-host"} {
		t.Run(kind, func(t *testing.T) {
			home := isolatedHome(t)
			parent := filepath.Join(home, ".local", "state")
			if err := os.MkdirAll(parent, 0700); err != nil {
				t.Fatal(err)
			}
			legacy := filepath.Join(parent, "agent-backplane")
			switch kind {
			case "symlink":
				if err := os.Symlink(home, legacy); err != nil {
					t.Fatal(err)
				}
			case "regular":
				if err := os.WriteFile(legacy, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Mkdir(legacy, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "public" {
					if err := os.Chmod(legacy, 0755); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(filepath.Join(legacy, "host-id"), []byte("incomplete"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			p, err := Resolve("")
			if err != nil || p.Dir != legacy {
				t.Fatal("invalid legacy state was skipped", err)
			}
			if kind == "corrupt-host" {
				if _, err := p.HostID(); err == nil {
					t.Fatal("corrupt prior host identity accepted")
				}
			} else if err := p.Ensure(); err == nil {
				t.Fatal("unsafe legacy state accepted")
			}
			if _, err := os.Lstat(filepath.Join(parent, "keelmux")); !os.IsNotExist(err) {
				t.Fatal("unsafe old state caused a replacement authority")
			}
		})
	}
}

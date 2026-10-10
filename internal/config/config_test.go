package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateStableHostAndSingleOwner(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "bp-config-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	p, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Ensure(); err != nil {
		t.Fatal(err)
	}
	a, err := p.HostID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.HostID()
	if err != nil || a != b {
		t.Fatal("host identity changed", err)
	}
	lock, err := p.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err = p.Lock(); err == nil {
		t.Fatal("second daemon permitted")
	}
	i, _ := os.Stat(filepath.Join(p.Dir, "host-id"))
	if i.Mode().Perm() != 0600 {
		t.Fatal("host identity not private")
	}
}

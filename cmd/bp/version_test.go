package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseInformationDoesNotCreateStateOrStartDaemon(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "km-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	prior := os.Args
	t.Cleanup(func() { os.Args = prior })
	state := filepath.Join(root, "must-stay-absent")
	for _, args := range [][]string{{"--version"}, {"version"}, {"version", "--json"}, {"--help"}, {"help"}, {"run", "--help"}, {"stop", "--help"}} {
		os.Args = append([]string{"keelmux", "--state", state}, args...)
		if err := run(); err != nil {
			t.Fatalf("informational command %v: %v", args, err)
		}
		if _, err := os.Lstat(state); !os.IsNotExist(err) {
			t.Fatal("informational command created state")
		}
	}
	for _, args := range [][]string{{"version", "extra"}, {"--version", "list"}} {
		os.Args = append([]string{"keelmux", "--state", state}, args...)
		if err := run(); err == nil {
			t.Fatal("unsupported metadata arguments accepted")
		}
	}
}

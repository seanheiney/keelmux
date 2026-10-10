package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntryConfigurationIsPrivateAndDoesNotFollowSymlinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entry.json")
	data := []byte(`{"principal":"local","peers":[{"host_id":"home","socket":"/tmp/owned/control.sock"}],"grants":[{"host_id":"home","sessions":["session_a"]}]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	conf, err := readConfiguration(path)
	if err != nil || conf.Principal != "local" || len(conf.Grants) != 1 {
		t.Fatalf("private explicit grant config rejected: %v", err)
	}
	link := filepath.Join(t.TempDir(), "redirect.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfiguration(link); err == nil {
		t.Fatal("followed symlink into another configuration")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfiguration(path); err == nil {
		t.Fatal("accepted public entry authorization file")
	}
	if err := os.WriteFile(path, []byte(`{"SENSITIVE_PRIVATE_FIELD":"secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0600)
	if _, err := readConfiguration(path); err == nil || strings.Contains(err.Error(), "SENSITIVE") || strings.Contains(err.Error(), "secret") {
		t.Fatal("invalid authorization config accepted or disclosed")
	}
}

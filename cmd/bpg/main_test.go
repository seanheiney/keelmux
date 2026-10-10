package main

import (
	"encoding/json"
	"github.com/seanheiney/keelmux/internal/domain"
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

func TestEntryConfigurationRequiresOneExplicitInventoryRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entry.json")
	host := string(domain.NewID("host"))
	for _, peer := range []map[string]string{
		{"host_id": host},
		{"host_id": host, "socket": "/tmp/control.sock", "inventory_store": "/tmp/peers"},
		{"host_id": host, "inventory_store": "relative"},
		{"host_id": "host_bad", "inventory_store": "/tmp/peers"},
	} {
		data, _ := json.Marshal(map[string]any{"principal": "owner", "peers": []any{peer}})
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readConfiguration(path); err == nil {
			t.Fatal("ambiguous/invalid inventory route accepted", peer)
		}
	}
	data, _ := json.Marshal(map[string]any{"principal": "owner", "peers": []any{map[string]string{"host_id": host, "inventory_store": "/tmp/peers"}}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if conf, err := readConfiguration(path); err != nil || conf.Peers[0].InventoryStore != "/tmp/peers" {
		t.Fatal("explicit enrolled inventory route rejected", err)
	}
}

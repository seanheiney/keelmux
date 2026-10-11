package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/federation"
	"github.com/seanheiney/keelmux/internal/peers"
)

func TestEntryReadBridgeRequiresExplicitEnrolledPolicySelector(t *testing.T) {
	conf, _, _ := entryEnrollment(t)
	conf.Peers[0].TerminalRead = &peers.ReadSelection{PolicyPath: "/private/home/export.json", ScopeID: domain.NewID("scope")}
	sources, closeAll, err := entrySources(conf)
	if err != nil || len(sources) != 1 {
		t.Fatal("explicit observer selector rejected", err)
	}
	defer closeAll()
	if _, ok := sources[0].Source.(federation.StreamSource); !ok {
		t.Fatal("explicit read route lacked scoped stream source")
	}
	// Construction reads the enrolled record, and cannot contact a missing fake
	// known_hosts, launch SSH, or fetch native state. Trust is checked at use.
	if err = sources[0].Source.(*peers.ReadSource).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEntryReadBridgeDoesNotCreateMissingEnrollmentLock(t *testing.T) {
	conf, dir, _ := entryEnrollment(t)
	conf.Peers[0].TerminalRead = &peers.ReadSelection{PolicyPath: "/private/home/export.json", ScopeID: domain.NewID("scope")}
	lock := filepath.Join(dir, "peers.lock")
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if _, closeAll, err := entrySources(conf); err == nil {
		closeAll()
		t.Fatal("read entry recovered missing enrollment authority")
	}
	if _, err := os.Lstat(lock); !os.IsNotExist(err) {
		t.Fatal("read entry created lock", err)
	}
}

func TestEntryReadBridgeConfigurationRefusesAliasAndRouteWidening(t *testing.T) {
	host, scope := domain.NewID("host"), domain.NewID("scope")
	valid := map[string]any{"principal": "owner", "peers": []any{map[string]any{"host_id": host, "inventory_store": "/private/peers", "terminal_read": map[string]any{"policy_path": "/private/export.json", "scope_id": scope}}}}
	path := filepath.Join(t.TempDir(), "entry.json")
	raw, _ := json.Marshal(valid)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfiguration(path); err != nil {
		t.Fatal("valid opt-in refused", err)
	}
	cases := []string{
		`{"principal":"owner","principal":"different","peers":[]}`,
		`{"Principal":"owner","peers":[]}`,
		`{"principal":"owner","peers":[{"host_id":"` + string(host) + `","socket":"/private/control.sock","terminal_read":{"policy_path":"/private/export.json","scope_id":"` + string(scope) + `"}}]}`,
		`{"principal":"owner","peers":[{"host_id":"` + string(host) + `","inventory_store":"/private/peers","terminal_read":{"policy_path":"relative","scope_id":"` + string(scope) + `"}}]}`,
		`{"principal":"owner","peers":[{"host_id":"` + string(host) + `","inventory_store":"/private/peers","terminal_read":{"policy_path":"/private/export.json","scope_id":"scope_invalid"}}]}`,
		`{"principal":"owner","peers":[{"host_id":"` + string(host) + `","inventory_store":"/private/peers","terminal_read":{"policy_path":"/private/export.json","scope_id":"` + string(scope) + `","socket":"/other/home.sock"}}]}`,
	}
	for _, bad := range cases {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readConfiguration(path); err == nil {
			t.Fatal("ambiguous/expanded read authority accepted", bad)
		}
	}
}

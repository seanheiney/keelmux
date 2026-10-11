package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateJSONRejectsAmbiguousAuthorityBeforePublishing(t *testing.T) {
	type route struct {
		Principal string `json:"principal"`
		Scope     struct {
			Sessions []string `json:"sessions"`
		} `json:"scope"`
	}
	path := filepath.Join(t.TempDir(), "private.json")
	cases := []string{
		`{"principal":"owner","principal":"attacker"}`,
		`{"principal":"owner","Principal":"attacker"}`,
		`{"principal":"owner","scope":{"sessions":["a"],"\u0073essions":["b"]}}`,
		`{"principal":"owner","scope":{"seſsions":["a"]}}`,
		`{"principal":"owner","unknown":true}`,
		`{"principal":"owner"} {"principal":"other"}`,
		`{"principal":"owner"}` + strings.Repeat(" ", 2048),
		string([]byte{'{', '"', 'p', 0xff, '"', ':', '1', '}'}),
	}
	for _, raw := range cases {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		var out route
		if err := ReadPrivateJSON(path, 1024, &out); err == nil {
			t.Fatalf("ambiguous private scope accepted: %q", raw)
		}
	}
	good := `{"principal":"owner","scope":{"sessions":["a"]}}`
	if err := os.WriteFile(path, []byte(good), 0600); err != nil {
		t.Fatal(err)
	}
	var out route
	if err := ReadPrivateJSON(path, 1024, &out); err != nil || out.Principal != "owner" || len(out.Scope.Sessions) != 1 || out.Scope.Sessions[0] != "a" {
		t.Fatal("explicit private scope refused", err, out)
	}
	link := filepath.Join(t.TempDir(), "redirect.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := ReadPrivateJSON(link, 1024, &out); err == nil {
		t.Fatal("private authorization followed symlink")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := ReadPrivateJSON(path, 1024, &out); err == nil {
		t.Fatal("world-readable authority accepted")
	}
	if err := ReadPrivateJSON(filepath.Dir(path), 1024, &out); err == nil {
		t.Fatal("nonregular authority accepted")
	}
}

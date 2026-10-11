package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/peerbridge"
)

func policyCLIFixture(t *testing.T) (string, string, peerbridge.ReadPolicy) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := peerbridge.ReadPolicy{Version: peerbridge.Version, ScopeID: domain.NewID("scope"), HostID: domain.NewID("host"), Principal: "owner", Socket: "/private/home/control.sock", Revision: 1, Enabled: true, Sessions: []peerbridge.SessionActions{}}
	file, authority := filepath.Join(dir, "desired.json"), filepath.Join(dir, "authority.json")
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return file, authority, p
}

func TestPeerPolicyExplicitProvisionUpdateAndPolicyOnlyReadback(t *testing.T) {
	file, authority, p := policyCLIFixture(t)
	var out bytes.Buffer
	ctx := context.Background()
	if err := peerPolicyCommand(ctx, []string{"provision", "--file", file, authority}, &out); err != nil {
		t.Fatal(err)
	}
	var actual peerbridge.ReadPolicy
	if err := json.Unmarshal(out.Bytes(), &actual); err != nil || actual.ScopeID != p.ScopeID || actual.Revision != 1 {
		t.Fatal("provision readback identity", err)
	}
	if strings.Contains(out.String(), "operations") || strings.Contains(out.String(), "lease") {
		t.Fatal("private journal exposed")
	}
	out.Reset()
	p.Enabled, p.Revision = false, 2
	raw, _ := json.Marshal(p)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := peerPolicyCommand(ctx, []string{"update", "--file", file, "--expect-revision", "1", authority}, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &actual); err != nil || actual.Revision != 2 || actual.Enabled {
		t.Fatal("exact update not read back", err)
	}
	before, err := os.ReadFile(authority)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := peerPolicyCommand(ctx, []string{"update", "--file", file, "--expect-revision", "1", authority}, &out); err == nil || out.Len() != 0 {
		t.Fatal("stale revision overwrote authority")
	}
	after, err := os.ReadFile(authority)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused mutation changed retained bytes", err)
	}
}

func TestPeerPolicyMalformedInputCannotProvisionOrPrintPrivateValue(t *testing.T) {
	for _, raw := range []string{
		`{"version":"private-secret","version":"duplicate"}`,
		`{"Version":"private-secret"}`,
		`{"version":"private-secret","unknown":true}`,
		`{"version":"private-secret"} {}`,
	} {
		t.Run(raw, func(t *testing.T) {
			file, authority, _ := policyCLIFixture(t)
			if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := peerPolicyCommand(context.Background(), []string{"provision", "--file", file, authority}, &out)
			if err == nil || strings.Contains(err.Error(), "private-secret") || out.Len() != 0 {
				t.Fatal("ambiguous data adopted or echoed", err)
			}
			if _, err := os.Lstat(authority); !os.IsNotExist(err) {
				t.Fatal("invalid input created home authority", err)
			}
		})
	}
}

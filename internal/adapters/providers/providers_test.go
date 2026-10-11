package providers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func executable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoverParsesOnlySafeStatusFieldsAndQualifiedCoverage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	executable(t, dir, "codex", `if [ "$1" = --version ]; then printf '%s\n' 'codex-cli 0.159.0'; else printf '%s\n' 'Logged in using an API key - sk-SENSITIVE_SHOULD_NOT_ESCAPE' >&2; fi`)
	executable(t, dir, "claude", `if [ "$1" = --version ]; then printf '%s\n' '2.1.296 (Claude Code)'; else printf '%s\n' '{"loggedIn":true,"authMethod":"claude.ai","email":"private@example.com","access_token":"SENSITIVE_SHOULD_NOT_ESCAPE","subscriptionType":"max"}'; fi`)
	records, err := Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 5 || records[0].Version.Value != "0.159.0" || records[1].Version.Value != "2.1.296" {
		t.Fatalf("wrong versions: %+v", records)
	}
	if records[0].Auth.Method.Value != "api_key" || records[1].Auth.Method.Value != "claude.ai" || records[1].Auth.State.Value != "credentials_present" {
		t.Fatal("safe documented status not parsed")
	}
	for _, r := range records {
		if r.Auth.Principal.Value != "unknown" || r.SemanticPrompt || !strings.Contains(r.Auth.Coverage, "subscription eligibility") {
			t.Fatal("discovery overclaimed identity or eligibility")
		}
	}
	b, _ := json.Marshal(records)
	if strings.Contains(string(b), "SENSITIVE") || strings.Contains(string(b), "private@example") || strings.Contains(string(b), "subscriptionType") {
		t.Fatal("raw native status leaked into records")
	}
	if records[2].Installed || records[2].Auth.State.Value != "unknown" {
		t.Fatal("missing Grok fabricated installed or authenticated state")
	}
}

func TestUnavailableMalformedAndUnknownAuthRemainDistinct(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	executable(t, dir, "codex", `if [ "$1" = --version ]; then printf '%s\n' 'codex-cli 0.159.0'; else printf '%s\n' 'Not logged in' >&2; exit 1; fi`)
	executable(t, dir, "claude", `if [ "$1" = --version ]; then printf '%s\n' '2.1.296 (Claude Code)'; else printf '%s\n' '{"loggedIn":false,"authMethod":"none"}'; exit 1; fi`)
	r, err := Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r[0].Auth.State.Value != "unavailable" || r[1].Auth.State.Value != "unavailable" {
		t.Fatal("known unavailable native status lost")
	}
	executable(t, dir, "claude", `if [ "$1" = --version ]; then printf '%s\n' '2.1.296 (Claude Code)'; else printf '%s\n' '{"loggedIn":true,"authMethod":"secret-payload-not-a-method"}'; fi`)
	r, _ = Discover(context.Background())
	if r[1].Auth.State.Value != "credentials_present" || r[1].Auth.Method.Value != "unknown" {
		t.Fatal("unknown method string trusted")
	}
	executable(t, dir, "claude", `if [ "$1" = --version ]; then printf '%s\n' '2.1.296 (Claude Code)'; else printf '%s\n' 'provider failed SENSITIVE'; exit 3; fi`)
	r, _ = Discover(context.Background())
	if r[1].Auth.State.Value != "unknown" {
		t.Fatal("failed status treated as logged out")
	}
}

func TestSupportedLaunchPreservesArgvAndRejectsImplicitAuthChanges(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	path := executable(t, dir, "codex", `printf '%s\n' 'codex-cli 0.159.0'`)
	args := []string{"--model", "example", "quoted $value `literal`"}
	argv, version, err := SupportedLaunch(context.Background(), "codex", args)
	if err != nil {
		t.Fatal(err)
	}
	if version != "0.159.0" || !reflect.DeepEqual(argv, append([]string{path}, args...)) {
		t.Fatal("native args transformed or interpreted as shell")
	}
	for _, bad := range [][]string{{"login"}, {"logout"}, {"auth", "login"}, {"update"}, {"--profile", "example", "logout"}, {"--config", "k=v", "login"}, {"--profile=example", "logout"}, {"-ck=v", "login"}, {"bad\x00argv"}} {
		if _, _, err := SupportedLaunch(context.Background(), "codex", bad); err == nil {
			t.Fatalf("unsafe native workflow accepted: %v", bad)
		}
	}
	if _, _, err := SupportedLaunch(context.Background(), "grok", nil); err == nil {
		t.Fatal("missing Grok executable claimed support")
	}
	executable(t, dir, "codex", `printf '%s\n' 'SENSITIVE not a version'`)
	if _, _, err := SupportedLaunch(context.Background(), "codex", nil); err == nil || strings.Contains(err.Error(), "SENSITIVE") {
		t.Fatal("invalid version leaked or accepted")
	}
}

func TestNativeWorkflowGuardHandlesOptionsAndLiteralPrompts(t *testing.T) {
	for _, bad := range [][]string{{"setup-token"}, {"--model", "example", "auth", "logout"}, {"--model=example", "setup-token"}, {"--unknown-future-option", "value", "auth"}, {"--dangerously-skip-permissions", "daemon", "stop"}, {"--", "setup-token"}, {"-p", "prompt"}, {"-cp", "prompt"}, {"--bg"}} {
		if err := validateNativeArgs("claude", bad); err == nil {
			t.Fatalf("nonterminal or management workflow accepted: %v", bad)
		}
	}
	for _, good := range [][]string{{"--profile", "login"}, {"--model=logout", "ordinary prompt"}, {"--", "please discuss login"}, {"resume", "saved-native-id"}, {"please explain logout"}} {
		if err := validateNativeArgs("codex", good); err != nil {
			t.Fatalf("literal native argv rejected: %v: %v", good, err)
		}
	}
}

func TestNativeDiscoveryReceipt(t *testing.T) {
	if os.Getenv("BP_NATIVE_DISCOVERY") != "1" {
		t.Skip("explicit read-only native CLI receipt")
	}
	r, err := Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"codex": "0.159.0", "claude": "2.1.296"}
	for _, record := range r {
		if version, ok := want[record.Name]; ok {
			if !record.Installed || record.Version.Value != version {
				t.Fatalf("native %s version does not match declared receipt", record.Name)
			}
		}
		// Record only whitelisted fields; principal/eligibility remain unknown.
		t.Logf("native %s installed=%t version=%s auth=%s method=%s source=%s principal=%s semantic=%t", record.Name, record.Installed, record.Version.Value, record.Auth.State.Value, record.Auth.Method.Value, record.Auth.State.Source, record.Auth.Principal.Value, record.SemanticPrompt)
	}
}

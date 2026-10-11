package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/seanheiney/keelmux/internal/library"
	"github.com/seanheiney/keelmux/internal/library/native"
)

func toolsCLI(t *testing.T, state string, args ...string) []byte {
	t.Helper()
	var out, diagnostic bytes.Buffer
	if err := toolsCommand(context.Background(), state, "", args, &diagnostic, &out); err != nil {
		t.Fatalf("tools %v: %v %s", args, err, diagnostic.String())
	}
	return out.Bytes()
}

func TestDirectToolSwitchesPreserveIndependentPinsAndPackageGate(t *testing.T) {
	ctx := context.Background()
	state, bin := libraryCLIDir(t), libraryCLIDir(t)
	marker := filepath.Join(bin, "invoked")
	if err := os.WriteFile(filepath.Join(bin, "headroom"), []byte("#!/bin/sh\n: > '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	var pack library.Package
	if err := json.Unmarshal(libraryCLI(t, state, "plugins", "import", "--builtin", "headroom"), &pack); err != nil {
		t.Fatal(err)
	}
	libraryCLI(t, state, "plugins", "on", "headroom")
	libraryCLI(t, state, "profiles", "create", "--name", "Review", "review")
	libraryCLI(t, state, "plugins", "on", "--profile", "review", "headroom")
	reg, err := library.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	toolsCLI(t, state, "off", "--profile", "review", "headroom/mcp")
	defaultProfile, err := reg.Resolve(ctx, "default")
	if err != nil || len(defaultProfile.Tools) != 1 {
		t.Fatal("switch changed a different profile", defaultProfile, err)
	}
	review, err := reg.Resolve(ctx, "review")
	if err != nil || len(review.Tools) != 0 {
		t.Fatal("off did not gate the selected tool", review, err)
	}
	toolsCLI(t, state, "on", "--profile", "review", "headroom/mcp")
	review, err = reg.Resolve(ctx, "review")
	if err != nil || len(review.Tools) != 1 || review.Tools[0].PackageDigest != pack.Digest {
		t.Fatal("re-enable changed or lost the pin", review, err)
	}
	libraryCLI(t, state, "plugins", "off", "--profile", "review", "headroom")
	toolsCLI(t, state, "off", "--profile", "review", "headroom/mcp")
	toolsCLI(t, state, "on", "--profile", "review", "headroom/mcp")
	catalog, err := reg.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := libraryProfile(catalog, "review")
	preference, overridden := selected.Packages["headroom"].Components["tool/mcp"]
	if err != nil || selected.Packages["headroom"].Enabled || selected.Packages["headroom"].Digest != pack.Digest || overridden && !preference {
		t.Fatal("component switch enabled a disabled pack or lost its preference", selected, err)
	}
	review, err = reg.Resolve(ctx, "review")
	if err != nil || len(review.Tools) != 0 {
		t.Fatal("package gate was bypassed", review, err)
	}
	before, err := reg.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	toolsCLI(t, state, "on", "--profile", "review", "headroom/mcp")
	after, err := reg.Read(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("same-value switch changed catalog or revisions", err)
	}
	var out bytes.Buffer
	if err := toolsCommand(ctx, state, "", []string{"off", "headroom/missing"}, &out, &out); err == nil {
		t.Fatal("unknown tool accepted")
	}
	after, err = reg.Read(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("invalid switch changed the catalog", err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("tool switch ran the executable", err)
	}
}

func TestDirectToolSwitchRefusalsCreateNoState(t *testing.T) {
	state := filepath.Join(libraryCLIDir(t), "absent")
	for _, args := range [][]string{
		{"on", "headroom/mcp"}, {"off", "headroom/mcp"},
		{"on", "headroom"}, {"off", "headroom/mcp/extra"},
		{"on", "--health", "headroom/mcp"}, {"off", "--workspace", "/tmp", "headroom/mcp"},
		{"on", "--profile", "missing", "headroom/mcp"},
	} {
		var out bytes.Buffer
		if err := toolsCommand(context.Background(), state, "", args, &out, &out); err == nil {
			t.Fatal("invalid/unselected switch accepted", args)
		}
		if _, err := os.Lstat(state); !os.IsNotExist(err) {
			t.Fatal("refusal created state", args, err)
		}
	}
	var out bytes.Buffer
	if err := toolsCommand(context.Background(), state, "/private/entry", []string{"on", "headroom/mcp"}, &out, &out); err == nil {
		t.Fatal("remote entry changed local selections")
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatal("entry refusal created state", err)
	}
}

func TestPinnedGeminiToolSelectorRefusalPreservesAppliedBytes(t *testing.T) {
	ctx := context.Background()
	state, workspace := libraryCLIDir(t), libraryCLIDir(t)
	libraryCLI(t, state, "plugins", "import", "--builtin", "headroom")
	libraryCLI(t, state, "plugins", "on", "headroom")
	libraryCLI(t, state, "plugins", "apply", "--harness", "gemini", "--workspace", workspace)
	target := native.Target{Harness: "gemini", Workspace: workspace, ApplicationID: "work"}
	before, err := native.Status(ctx, target)
	if err != nil || !before.Verified {
		t.Fatal(before, err)
	}
	for _, args := range [][]string{{"--allowed-mcp-server-names", "other"}, {"--allowed-mcp-server-names=other"}} {
		if _, _, err := prepareLibraryLaunch(ctx, state, "gemini", workspace, "default", "work", true, args); err == nil {
			t.Fatal("profile tool selector override accepted", args)
		}
		after, err := native.Status(ctx, target)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("selector refusal changed applied files or receipt", err)
		}
	}
	args := []string{"--model", "native-example", "--resume=previous"}
	got, snapshot, err := prepareLibraryLaunch(ctx, state, "gemini", workspace, "default", "work", true, args)
	if err != nil || snapshot == nil || !reflect.DeepEqual(got, args) {
		t.Fatal("ordinary model/resume flags were changed", got, snapshot, err)
	}
	// No selected library means this native selector is left to the separate
	// reviewed provider contract. No executable is probed by preparation.
	emptyState := filepath.Join(libraryCLIDir(t), "absent")
	args = []string{"--allowed-mcp-server-names", "other"}
	got, snapshot, err = prepareLibraryLaunch(ctx, emptyState, "gemini", workspace, "default", "work", false, args)
	if err != nil || snapshot != nil || !reflect.DeepEqual(got, args) {
		t.Fatal("unprofiled selector was changed", got, snapshot, err)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/library"
	"github.com/seanheiney/keelmux/internal/library/native"
)

func libraryCLIDir(t *testing.T) string {
	t.Helper()
	created, err := os.MkdirTemp("/tmp", "kl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(created) })
	d, err := filepath.EvalSymlinks(created)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	return d
}

func libraryCLI(t *testing.T, state, command string, args ...string) []byte {
	t.Helper()
	var out, diagnostic bytes.Buffer
	if err := manageLibrary(context.Background(), state, "", command, args, &diagnostic, &out); err != nil {
		t.Fatalf("%s %v: %v %s", command, args, err, diagnostic.String())
	}
	return out.Bytes()
}

func TestLibraryCLIReadsAndHelpHaveNoEffects(t *testing.T) {
	parent := libraryCLIDir(t)
	state := filepath.Join(parent, "absent")
	libraryCLI(t, state, "plugins", "list")
	libraryCLI(t, state, "profiles", "show")
	libraryCLI(t, state, "plugins", "--help")
	var out bytes.Buffer
	if err := toolsCommand(context.Background(), state, "", []string{"list"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatal("read created state", err)
	}
	if err := manageLibrary(context.Background(), state, "/private/entry", "plugins", []string{"import", "--builtin", "headroom"}, &out, &out); err == nil {
		t.Fatal("entry mutated local library")
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
		t.Fatal("unexpected effect", entries, err)
	}
}

func TestLibraryCLIActualPinnedSelectionsAndNoImplicitToolExecution(t *testing.T) {
	state := libraryCLIDir(t)
	bin := libraryCLIDir(t)
	marker := filepath.Join(bin, "invoked")
	if err := os.WriteFile(filepath.Join(bin, "headroom"), []byte("#!/bin/sh\n: > '"+marker+"'\nexit 9\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	var pack library.Package
	if err := json.Unmarshal(libraryCLI(t, state, "plugins", "import", "--builtin", "headroom"), &pack); err != nil {
		t.Fatal(err)
	}
	libraryCLI(t, state, "plugins", "on", "headroom")
	var first library.Resolution
	json.Unmarshal(libraryCLI(t, state, "profiles", "show"), &first)
	if first.Loaded != "unknown" || len(first.Tools) != 1 || first.Tools[0].PackageDigest != pack.Digest {
		t.Fatal(first)
	}
	var out bytes.Buffer
	if err := toolsCommand(context.Background(), state, "", []string{"check", "headroom/mcp"}, &out, &out); err != nil {
		t.Fatal(err, out.String())
	}
	if !strings.Contains(out.String(), `"started": false`) || !strings.Contains(out.String(), `"invoked": false`) {
		t.Fatal(out.String())
	}
	libraryCLI(t, state, "profiles", "create", "--name", "Review", "review")
	libraryCLI(t, state, "plugins", "on", "--profile", "review", "headroom")
	libraryCLI(t, state, "plugins", "off", "--component", "tool/mcp", "headroom")
	var disabled, other library.Resolution
	json.Unmarshal(libraryCLI(t, state, "profiles", "show"), &disabled)
	json.Unmarshal(libraryCLI(t, state, "profiles", "show", "review"), &other)
	if len(disabled.Tools) != 0 || disabled.Digest == first.Digest || len(other.Tools) != 1 {
		t.Fatal(disabled, other)
	}
	out.Reset()
	if err := toolsCommand(context.Background(), state, "", []string{"check", "headroom/mcp"}, &out, &out); err == nil {
		t.Fatal("disabled tool selected")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("declaration/toggle/check executed program", err)
	}
	libraryCLI(t, state, "plugins", "off", "headroom")
	var c library.Catalog
	json.Unmarshal(libraryCLI(t, state, "plugins", "list"), &c)
	for _, p := range c.Profiles {
		if p.ID == "default" && (p.Packages["headroom"].Digest != pack.Digest || p.Packages["headroom"].Enabled) {
			t.Fatal("off lost pin", p)
		}
	}
}

func TestLibraryCLIApplyLaunchSnapshotToggleAndOwnedClear(t *testing.T) {
	ctx := context.Background()
	state, workspace := libraryCLIDir(t), libraryCLIDir(t)
	libraryCLI(t, state, "plugins", "import", "--builtin", "headroom")
	libraryCLI(t, state, "plugins", "on", "headroom")
	common := []string{"--harness", "claude", "--workspace", workspace}
	var plan native.Plan
	json.Unmarshal(libraryCLI(t, state, "plugins", append([]string{"preview"}, common...)...), &plan)
	if _, err := os.Lstat(filepath.Join(workspace, ".keelmux-native")); !os.IsNotExist(err) {
		t.Fatal("preview wrote workspace", err)
	}
	if _, _, err := prepareLibraryLaunch(ctx, state, "claude", workspace, "default", "work", true, nil); err == nil {
		t.Fatal("unapplied profile launched")
	}
	var receipt native.Receipt
	json.Unmarshal(libraryCLI(t, state, "plugins", append(append([]string{"apply"}, common...), "--expect-plan", plan.Digest)...), &receipt)
	args, snapshot, err := prepareLibraryLaunch(ctx, state, "claude", workspace, "default", "work", true, []string{"--model", "native-example"})
	if err != nil || snapshot == nil || snapshot.ApplicationDigest != receipt.Digest || snapshot.Loaded != "unknown" || len(args) != 4 || args[0] != "--mcp-config" {
		t.Fatal(args, snapshot, err)
	}
	if err = snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareLibraryLaunch(ctx, state, "claude", workspace, "default", "work", true, []string{"--mcp-config", "other.json"}); err == nil {
		t.Fatal("native override changed pinned application")
	}
	libraryCLI(t, state, "plugins", "off", "--component", "tool/mcp", "headroom")
	if _, _, err := prepareLibraryLaunch(ctx, state, "claude", workspace, "default", "work", true, nil); err == nil {
		t.Fatal("stale application launched")
	}
	oldPath := receipt.Files[0].Path
	libraryCLI(t, state, "plugins", append([]string{"apply"}, common...)...)
	if _, err := os.Lstat(filepath.Join(workspace, oldPath)); !os.IsNotExist(err) {
		t.Fatal("off failed to remove owned config", err)
	}
	if _, s, err := prepareLibraryLaunch(ctx, state, "claude", workspace, "default", "work", true, nil); err != nil || s == nil {
		t.Fatal("empty applied selection unavailable", s, err)
	}
	libraryCLI(t, state, "plugins", append([]string{"clear"}, common...)...)
}

func TestPinnedProfileNativeSelectorsAreRefused(t *testing.T) {
	for _, tc := range []struct {
		provider string
		args     []string
	}{
		{"codex", []string{"--cd=/other"}}, {"codex", []string{"-cfeatures.x=true"}}, {"codex", []string{"--profile", "different"}},
		{"claude", []string{"--settings=other.json"}}, {"claude", []string{"--strict-mcp-config"}}, {"grok", []string{"--rules", "other"}},
	} {
		if err := profileArgsCompatible(tc.provider, tc.args); err == nil {
			t.Fatal("accepted override", tc)
		}
	}
}

func TestBuiltinPluginPackMatchesPublishedSourceBundle(t *testing.T) {
	for _, name := range []string{"README.md", "keelmux-plugin.json"} {
		embedded, err := pluginPresets.ReadFile("presets/headroom/" + name)
		if err != nil {
			t.Fatal(err)
		}
		source, err := os.ReadFile(filepath.Join("../../plugins/headroom", name))
		if err != nil || !bytes.Equal(embedded, source) {
			t.Fatal("embedded builtin differs from reviewed bundle", name, err)
		}
	}
}

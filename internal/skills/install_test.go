package skills

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFixture(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
}
func preview(t *testing.T, root string, providers ...string) Preview {
	t.Helper()
	p, err := Plan(root, providers)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func apply(t *testing.T, p Preview) []Result {
	t.Helper()
	r, err := Apply(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func action(t *testing.T, r []Result, provider string) string {
	t.Helper()
	for _, result := range r {
		if result.Provider == provider {
			return result.Action
		}
	}
	t.Fatalf("missing %s result", provider)
	return ""
}

func TestPreviewIsDeterministicAndDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	first := preview(t, root, "codex", "claude", "codex", "grok")
	second := preview(t, root, "grok", "claude", "codex")
	if !reflect.DeepEqual(first, second) {
		t.Fatal("preview depends on provider order or duplicate selection")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("preview wrote files")
	}
	if first.Mappings[2].Action != "unsupported" {
		t.Fatal("unverified Grok skill mapping supported")
	}
	for _, m := range first.Mappings {
		if m.Loaded != "unknown" || m.Discoverable != "unknown" {
			t.Fatal("preview fabricated native loading evidence")
		}
	}
}

func TestInstallIdempotentRemovePreservesNativeConfigAndCustomSkills(t *testing.T) {
	root := t.TempDir()
	custom := filepath.Join(root, ".agents", "skills", "custom", "SKILL.md")
	config := filepath.Join(root, ".claude", "settings.json")
	writeFixture(t, custom, []byte("custom skill"))
	writeFixture(t, config, []byte(`{"custom":"retain"}`))
	p := preview(t, root, "codex", "claude")
	r := apply(t, p)
	for _, name := range []string{"codex", "claude"} {
		if action(t, r, name) != "installed" {
			t.Fatalf("%s did not install: %+v", name, r)
		}
	}
	for _, m := range p.Mappings {
		for _, f := range m.Files {
			path := filepath.Join(root, m.Directory, f.Path)
			b, err := os.ReadFile(path)
			if err != nil || digest(b) != f.Digest {
				t.Fatalf("wrong owned digest %s", f.Path)
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0600 {
				t.Fatal("skill file is not private")
			}
		}
	}
	before, _ := os.ReadFile(filepath.Join(root, ledgerPath))
	r = apply(t, p)
	after, _ := os.ReadFile(filepath.Join(root, ledgerPath))
	if action(t, r, "codex") != "unchanged" || action(t, r, "claude") != "unchanged" || string(before) != string(after) {
		t.Fatal("idempotent install changed ownership")
	}
	r, err := Remove(root, []string{"codex", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "claude"} {
		if action(t, r, name) != "removed" {
			t.Fatalf("%s removal failed: %+v", name, r)
		}
		if _, err := os.Stat(filepath.Join(root, directory(name))); !os.IsNotExist(err) {
			t.Fatal("owned directory not removed")
		}
	}
	if b, _ := os.ReadFile(custom); string(b) != "custom skill" {
		t.Fatal("unrelated custom skill changed")
	}
	if b, _ := os.ReadFile(config); string(b) != `{"custom":"retain"}` {
		t.Fatal("native config changed")
	}
}

func TestUnownedCollisionAndStalePreviewNeverAdoptOrOverwrite(t *testing.T) {
	root := t.TempDir()
	p := preview(t, root, "codex", "claude")
	path := filepath.Join(root, directory("codex"), "SKILL.md")
	writeFixture(t, path, catalogue("codex")["SKILL.md"])
	r := apply(t, p)
	if action(t, r, "codex") != "collision" || action(t, r, "claude") != "installed" {
		t.Fatalf("provider collision did not remain independent: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(root, directory("codex"), marker)); !os.IsNotExist(err) {
		t.Fatal("identical unowned file was adopted")
	}
	r, err := Remove(root, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if action(t, r, "codex") != "not_owned" {
		t.Fatal("unowned collision removed")
	}
	if b, _ := os.ReadFile(path); !reflect.DeepEqual(b, catalogue("codex")["SKILL.md"]) {
		t.Fatal("unowned bytes changed")
	}
}

func TestUserEditedOrAdditionalContentPreservesWholeBundle(t *testing.T) {
	for _, change := range []string{"edited", "additional"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			apply(t, preview(t, root, "codex"))
			path := filepath.Join(root, directory("codex"), "SKILL.md")
			if change == "edited" {
				writeFixture(t, path, []byte("user revised skill"))
			} else {
				writeFixture(t, filepath.Join(root, directory("codex"), "user-notes.md"), []byte("custom"))
			}
			p := preview(t, root, "codex")
			if p.Mappings[0].Action != "preserve_edited" {
				t.Fatal("drift not shown in preview")
			}
			if action(t, apply(t, p), "codex") != "preserve_edited" {
				t.Fatal("apply erased edits")
			}
			r, err := Remove(root, []string{"codex"})
			if err != nil {
				t.Fatal(err)
			}
			if action(t, r, "codex") != "preserve_edited" {
				t.Fatal("remove erased edited bundle")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("owned skill was removed despite user changes")
			}
		})
	}
}

func TestSymlinkPathsNeverWriteOrRemoveTheTarget(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	writeFixture(t, sentinel, []byte("untouched"))
	if err := os.Symlink(outside, filepath.Join(root, ".agents")); err != nil {
		t.Fatal(err)
	}
	p := preview(t, root, "codex", "claude")
	if p.Mappings[1].Action != "unsafe_path" {
		t.Fatal("symlink provider path was accepted")
	}
	r := apply(t, p)
	if action(t, r, "codex") != "unsafe_path" || action(t, r, "claude") != "installed" {
		t.Fatal("unsafe provider blocked unrelated safe provider or wrote target")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Fatal("symlink target mutated")
	}
	rootLink := filepath.Join(t.TempDir(), "root")
	os.Symlink(root, rootLink)
	if _, err := Plan(rootLink, []string{"codex"}); err == nil {
		t.Fatal("symlink root accepted")
	}
	metadataRoot := t.TempDir()
	os.Symlink(outside, filepath.Join(metadataRoot, ".backplane"))
	if _, err := Plan(metadataRoot, []string{"codex"}); err == nil {
		t.Fatal("symlink ownership ledger directory accepted")
	}
	owned := t.TempDir()
	apply(t, preview(t, owned, "codex"))
	script := filepath.Join(owned, directory("codex"), "scripts", "link.sh")
	os.Remove(script)
	os.Symlink(sentinel, script)
	r, err := Remove(owned, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if action(t, r, "codex") != "preserve_edited" {
		t.Fatal("symlink managed file deleted")
	}
	if b, _ := os.ReadFile(sentinel); string(b) != "untouched" {
		t.Fatal("external symlink target changed")
	}
}

func TestPublishedInstallLostAcknowledgementReconcilesWithoutDuplicate(t *testing.T) {
	root := t.TempDir()
	apply(t, preview(t, root, "codex"))
	tr, err := openTree(root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	l, err := readLedger(tr)
	if err != nil {
		t.Fatal(err)
	}
	e := l.Entries[key(UserScope, "codex")]
	e.State = "installing"
	l.Entries[key(UserScope, "codex")] = e
	if err := writeLedger(tr, l); err != nil {
		t.Fatal(err)
	}
	if got := action(t, apply(t, preview(t, root, "codex")), "codex"); got != "unchanged" {
		t.Fatalf("published bundle duplicated: %s", got)
	}
	l, _ = readLedger(tr)
	if l.Entries[key(UserScope, "codex")].InstallationID != e.InstallationID || l.Entries[key(UserScope, "codex")].State != "installed" {
		t.Fatal("publication reconciliation changed ownership identity")
	}
	e.State = "installing"
	l.Entries[key(UserScope, "codex")] = e
	writeLedger(tr, l)
	r, err := Remove(root, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if action(t, r, "codex") != "removed" {
		t.Fatal("lost publication ack left published skill behind on remove")
	}
}

func TestPartialStageAndPartialRemovalResumeRecordedOwnedContent(t *testing.T) {
	root := t.TempDir()
	tr, err := openTree(root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	m := mapping("codex", UserScope)
	id := "install_fixture"
	e := entry{Provider: "codex", Scope: UserScope, Directory: m.Directory, Source: Source, Revision: Revision, InstallationID: id, Stage: filepath.Join(".backplane", id), State: "installing", Files: append(append([]File{}, m.Files...), File{Path: marker, Digest: digest([]byte(id + "\n"))})}
	l := ledger{Version: 1, Owner: "agent-backplane", Entries: map[string]entry{key(UserScope, "codex"): e}}
	if err := writeLedger(tr, l); err != nil {
		t.Fatal(err)
	}
	tr.createDir(e.Stage)
	tr.write(filepath.Join(e.Stage, marker), []byte(id+"\n"), false)
	tr.write(filepath.Join(e.Stage, "SKILL.md"), catalogue("codex")["SKILL.md"], false)
	p := preview(t, root, "codex")
	if p.Mappings[0].Action != "recover_install" {
		t.Fatal("partial stage not recorded")
	}
	if got := action(t, apply(t, p), "codex"); got != "installed" {
		t.Fatalf("partial stage did not recover: %s", got)
	}
	l, _ = readLedger(tr)
	e = l.Entries[key(UserScope, "codex")]
	e.State = "removing"
	l.Entries[key(UserScope, "codex")] = e
	writeLedger(tr, l)
	os.Remove(filepath.Join(root, e.Directory, "scripts", "link.sh"))
	if got := preview(t, root, "codex").Mappings[0].Action; got != "pending_removal" {
		t.Fatal("new install could replace interrupted removal")
	}
	r, err := Remove(root, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if action(t, r, "codex") != "removed" {
		t.Fatal("partial exact removal did not finish")
	}
}

func TestPreviewTamperingAndConcurrentInstallerAreRefused(t *testing.T) {
	root := t.TempDir()
	p := preview(t, root, "codex")
	p.Mappings[0].Files[0].Digest = strings.Repeat("0", 64)
	if _, err := Apply(p); err == nil {
		t.Fatal("altered preview applied")
	}
	tr, err := openTree(root)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.close()
	lock, err := lockLedger(tr)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := Apply(preview(t, root, "codex")); err == nil {
		t.Fatal("concurrent installer could overwrite ledger")
	}
}

func TestProjectScopeAndExplicitInvocationMetadata(t *testing.T) {
	root := t.TempDir()
	p, err := PlanScope(root, ProjectScope, []string{"codex", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	r := apply(t, p)
	if action(t, r, "codex") != "installed" {
		t.Fatal("owned project installation failed")
	}
	b, _ := os.ReadFile(filepath.Join(root, directory("codex"), "agents", "openai.yaml"))
	if !strings.Contains(string(b), "allow_implicit_invocation: false") {
		t.Fatal("Codex skill can invoke implicitly")
	}
	b, _ = os.ReadFile(filepath.Join(root, directory("claude"), "SKILL.md"))
	if !strings.Contains(string(b), "disable-model-invocation: true") {
		t.Fatal("Claude skill can invoke implicitly")
	}
	r, err = Remove(root, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if action(t, r, "codex") != "not_owned" {
		t.Fatal("user-scope removal erased project-scope ownership")
	}
	r, err = RemoveScope(root, ProjectScope, []string{"codex", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if action(t, r, "codex") != "removed" {
		t.Fatal("project-scope removal failed")
	}
}

func TestConnectionHelperAllowlistExactInheritedContextAndNoNonceOutput(t *testing.T) {
	root := t.TempDir()
	apply(t, preview(t, root, "codex"))
	script := filepath.Join(root, directory("codex"), "scripts", "link.sh")
	helper := filepath.Join(root, "owned helper")
	writeFixture(t, helper, []byte("#!/bin/sh\nfor arg do printf '%s\\n' \"$arg\"; done\n"))
	os.Chmod(helper, 0700)
	base := []string{"BP_SESSION_ID=session_1", "BP_EXECUTION_ID=execution_1", "BP_OPERATION_ID=launch_1", "BP_LINK_TOKEN=PRIVATE_NONCE", "BP_STATE_DIR=/owned/state with space", "BP_HELPER_PATH=" + helper}
	for _, args := range [][]string{nil, {"status"}, {"off"}} {
		cmd := exec.Command("/bin/sh", append([]string{script}, args...)...)
		cmd.Env = base
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("allowlisted invocation failed: %s", out)
		}
		want := "--state\n/owned/state with space\nlink\n" + strings.Join(args, "\n")
		if len(args) > 0 {
			want += "\n"
		}
		if string(out) != want || strings.Contains(string(out), "PRIVATE_NONCE") {
			t.Fatal("helper interpreted arguments or printed private nonce")
		}
	}
	for _, args := range [][]string{{"status; echo PRIVATE_NONCE"}, {"stop"}, {"status", "off"}} {
		cmd := exec.Command("/bin/sh", append([]string{script}, args...)...)
		cmd.Env = base
		out, err := cmd.CombinedOutput()
		if err == nil || strings.Contains(string(out), "PRIVATE_NONCE") {
			t.Fatal("invalid helper grammar ran or echoed untrusted input")
		}
	}
	cmd := exec.Command("/bin/sh", script)
	cmd.Env = base[:3]
	out, err := cmd.CombinedOutput()
	if err == nil || strings.Contains(string(out), "PRIVATE_NONCE") {
		t.Fatal("missing inherited nonce trusted")
	}
	cmd = exec.Command("/bin/sh", script)
	cmd.Env = append(base[:5], "BP_HELPER_PATH=bp")
	if err := cmd.Run(); err == nil {
		t.Fatal("relative PATH helper accepted")
	}
}

// Only a native catalogue scan: no thread/start, turn/start, copied credentials,
// or model invocation. Runtime context loading remains unknown.
func TestNativeCodexSkillCatalogueReceipt(t *testing.T) {
	if os.Getenv("BP_NATIVE_SKILL_DISCOVERY") != "1" {
		t.Skip("explicit native catalogue proof")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	version, err := exec.Command(binary, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "codex-cli 0.159.0" {
		t.Fatal("native catalogue receipt requires the declared Codex 0.159.0 version")
	}
	root := t.TempDir()
	p, err := PlanScope(root, ProjectScope, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, p)
	ownedCodexRoot := filepath.Join(root, "native-config")
	os.Mkdir(ownedCodexRoot, 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "app-server", "--stdio")
	cmd.Dir = root
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "CODEX_HOME=") || strings.HasPrefix(env, "HOME=") || strings.HasPrefix(env, "CODEX_ACCESS_TOKEN=") || strings.HasPrefix(env, "OPENAI_API_KEY=") {
			continue
		}
		cmd.Env = append(cmd.Env, env)
	}
	cmd.Env = append(cmd.Env, "CODEX_HOME="+ownedCodexRoot, "HOME="+root)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); cancel(); cmd.Wait() }()
	enc := json.NewEncoder(in)
	dec := json.NewDecoder(out)
	enc.Encode(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "agent_backplane_catalogue_fixture", "version": "0.1.0"}}})
	var response map[string]json.RawMessage
	for {
		if err := dec.Decode(&response); err != nil {
			t.Fatal("native initialize response unavailable")
		}
		if string(response["id"]) == "1" {
			break
		}
	}
	enc.Encode(map[string]any{"method": "initialized", "params": map[string]any{}})
	enc.Encode(map[string]any{"id": 2, "method": "skills/list", "params": map[string]any{"cwds": []string{root}, "forceReload": true}})
	for {
		if err := dec.Decode(&response); err != nil {
			t.Fatal("native catalogue response unavailable")
		}
		if string(response["id"]) == "2" {
			break
		}
	}
	if _, ok := response["error"]; ok {
		t.Fatal("native skills/list refused catalogue scan")
	}
	var result struct {
		Data []struct {
			Skills []struct {
				Name    string `json:"name"`
				Path    string `json:"path"`
				Enabled bool   `json:"enabled"`
			} `json:"skills"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response["result"], &result); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, data := range result.Data {
		for _, skill := range data.Skills {
			if skill.Name == "backplane" && skill.Enabled && skill.Path == filepath.Join(p.Root, directory("codex"), "SKILL.md") {
				found = true
				t.Log("native Codex 0.159.0 skills/list discovered enabled project backplane skill; loaded context remains unknown; no model turn")
			}
		}
	}
	if !found {
		t.Fatal("owned project skill not discovered by actual Codex catalogue")
	}
}

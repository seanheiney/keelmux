package library

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestStrictManifestAndFileInventoryRefusedBeforeStateCreation(t *testing.T) {
	cases := []string{"duplicate", "case-alias", "unknown", "nested-unknown", "future", "null-files", "traversal", "absolute", "backslash", "manifest-self", "bad-hash", "wrong-hash", "undeclared", "missing", "empty-directory", "symlink-directory", "symlink-file", "fifo", "hardlink", "public-writable-file", "public-writable-root", "invalid-skill", "invalid-rule", "duplicate-component", "env-value", "env-duplicate", "shell-transport", "credential-url", "query-url", "insecure-url", "control-id", "utf8", "trailing-json", "oversized-file", "file-count", "total-bytes", "depth"}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			r, state := fixtureRegistry(t)
			source, m := fixturePack(t, "pack", "1")
			raw, _ := json.Marshal(m)
			target := filepath.Join(source, "rules/careful.md")
			switch mode {
			case "duplicate":
				raw = []byte(strings.Replace(string(raw), `"schema":1`, `"schema":1,"schema":1`, 1))
			case "case-alias":
				raw = []byte(strings.Replace(string(raw), `"schema":1`, `"SCHEMA":1`, 1))
			case "unknown":
				raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"unknown":true}`)
			case "nested-unknown":
				raw = []byte(strings.Replace(string(raw), `"transport":"stdio"`, `"transport":"stdio","password":"secret"`, 1))
			case "future":
				m.Schema = 2
			case "null-files":
				m.Files = nil
			case "traversal":
				m.Files["../outside.md"] = strings.Repeat("a", 64)
			case "absolute":
				m.Files["/outside.md"] = strings.Repeat("a", 64)
			case "backslash":
				m.Files[`rules\outside.md`] = strings.Repeat("a", 64)
			case "manifest-self":
				m.Files[ManifestFilename] = strings.Repeat("a", 64)
			case "bad-hash":
				m.Files["rules/careful.md"] = strings.Repeat("A", 64)
			case "wrong-hash":
				m.Files["rules/careful.md"] = strings.Repeat("0", 64)
			case "undeclared":
				mustWrite(t, filepath.Join(source, "extra"), []byte("extra"))
			case "missing":
				os.Remove(target)
			case "empty-directory":
				os.Mkdir(filepath.Join(source, "empty"), 0700)
			case "symlink-directory":
				os.Symlink("rules", filepath.Join(source, "alias"))
			case "symlink-file":
				os.Remove(target)
				os.Symlink("/dev/null", target)
			case "fifo":
				os.Remove(target)
				if err := unix.Mkfifo(target, 0600); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(target, filepath.Join(source, "alias")); err != nil {
					t.Fatal(err)
				}
			case "public-writable-file":
				os.Chmod(target, 0666)
			case "public-writable-root":
				os.Chmod(source, 0777)
			case "invalid-skill":
				m.Skills[0].Path = "rules"
			case "invalid-rule":
				m.Rules[0].Path = "bin/tool.sh"
			case "duplicate-component":
				m.Skills = append(m.Skills, m.Skills[0])
			case "env-value":
				m.Tools[0].EnvRefs = []string{"API_KEY=secret"}
			case "env-duplicate":
				m.Tools[0].EnvRefs = []string{"API_KEY", "API_KEY"}
			case "shell-transport":
				m.Tools[0].Transport = "shell"
			case "credential-url":
				m.Tools[1].URL = "https://user:password@example.invalid/mcp"
			case "query-url":
				m.Tools[1].URL = "https://example.invalid/mcp?api_key=secret"
			case "insecure-url":
				m.Tools[1].URL = "http://example.invalid/mcp"
			case "control-id":
				m.ID = "bad\x1b[31m"
			case "utf8":
				raw = append(raw[:len(raw)-1], 0xff, '}')
			case "trailing-json":
				raw = append(raw, []byte(" {}")...)
			case "oversized-file":
				bytes := []byte(strings.Repeat("x", fileLimit+1))
				mustWrite(t, target, bytes)
				m.Files["rules/careful.md"] = hashBytes(bytes)
			case "file-count":
				for i := 0; i < maxFiles; i++ {
					name := "extra/" + strings.Repeat("x", 1) + string(rune(0x100+i))
					bytes := []byte("x")
					mustWrite(t, filepath.Join(source, name), bytes)
					m.Files[name] = hashBytes(bytes)
				}
			case "total-bytes":
				for i := 0; i < 17; i++ {
					name := "extra/" + string(rune('a'+i))
					bytes := []byte(strings.Repeat("x", fileLimit))
					mustWrite(t, filepath.Join(source, name), bytes)
					m.Files[name] = hashBytes(bytes)
				}
			case "depth":
				name := strings.Repeat("a/", 17) + "tail.md"
				mustWrite(t, filepath.Join(source, name), []byte("x"))
				m.Files[name] = hashBytes([]byte("x"))
			}
			switch mode {
			case "duplicate", "case-alias", "unknown", "nested-unknown", "utf8", "trailing-json":
				mustWrite(t, filepath.Join(source, ManifestFilename), raw)
			default:
				saveManifest(t, source, m)
			}
			before := snapshot(t, source)
			if _, err := r.Import(bg, source); err == nil {
				t.Fatal("invalid package accepted", mode)
			}
			if _, err := os.Lstat(state); !os.IsNotExist(err) {
				t.Fatal("invalid import created state", err)
			}
			if !reflect.DeepEqual(before, snapshot(t, source)) {
				t.Fatal("refused import edited source")
			}
		})
	}
}
func TestManifestCanonicalComponentOrderAndNoProgramOrNativeEffects(t *testing.T) {
	r, state := fixtureRegistry(t)
	source, m := fixturePack(t, "pack", "1")
	marker := filepath.Join(canonicalTemp(t), "tool-ran")
	m.Tools[0].Command = []string{"/bin/sh", "-c", "touch " + marker}
	m.Tools[0].EnvRefs = []string{"Z_KEY", "A_KEY"}
	saveManifest(t, source, m)
	before := snapshot(t, source)
	p, err := r.Import(bg, source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, source)) {
		t.Fatal("import edited source bytes or modes")
	}
	m.Tools[0], m.Tools[1] = m.Tools[1], m.Tools[0]
	m.Tools[1].EnvRefs = []string{"A_KEY", "Z_KEY"}
	saveManifest(t, source, m)
	again, err := r.Import(bg, source)
	if err != nil || p.Digest != again.Digest {
		t.Fatal("declaration order changed immutable identity", err)
	}
	r.SelectPackage(bg, "default", "pack", p.Digest, true)
	if _, err = r.Resolve(bg, "default"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("declaration executed")
	}
	root, _ := r.PackageRoot(bg, "pack", p.Digest)
	if info, err := os.Stat(filepath.Join(root, "bin/tool.sh")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("import retained execute permission", err)
	}
	if len(before) != len(snapshot(t, source)) {
		t.Fatal("import changed source inventory")
	}
	encoded, _ := json.Marshal(snapshot(t, state))
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("credential value stored")
	}
}
func TestUnsafeStorageAndAmbiguousCatalogPreserved(t *testing.T) {
	for _, mode := range []string{"state-public", "state-symlink", "ancestor-symlink", "root-public", "catalog-public", "catalog-symlink", "catalog-fifo", "catalog-hardlink", "lock-public", "lock-symlink", "lock-missing", "archive-missing", "catalog-future", "catalog-duplicate", "catalog-case-alias", "catalog-unknown", "catalog-component", "pending-public", "pending-unknown"} {
		t.Run(mode, func(t *testing.T) {
			r, state := fixtureRegistry(t)
			p := imported(t, r, "pack", "1")
			r.SelectPackage(bg, "default", "pack", p.Digest, true)
			catalog := filepath.Join(state, "library/catalog.json")
			lock := filepath.Join(state, "library/library.lock")
			openState := state
			switch mode {
			case "state-public":
				os.Chmod(state, 0755)
			case "state-symlink":
				openState = filepath.Join(filepath.Dir(state), "alias")
				os.Symlink(state, openState)
			case "ancestor-symlink":
				parent := filepath.Dir(state)
				alias := parent + "-alias"
				os.Symlink(parent, alias)
				defer os.Remove(alias)
				openState = filepath.Join(alias, "state")
			case "root-public":
				os.Chmod(filepath.Join(state, "library"), 0755)
			case "catalog-public":
				os.Chmod(catalog, 0644)
			case "catalog-symlink":
				os.Rename(catalog, catalog+".original")
				os.Symlink(catalog+".original", catalog)
			case "catalog-fifo":
				os.Remove(catalog)
				unix.Mkfifo(catalog, 0600)
			case "catalog-hardlink":
				os.Link(catalog, catalog+".alias")
			case "lock-public":
				os.Chmod(lock, 0644)
			case "lock-symlink":
				os.Rename(lock, lock+".original")
				os.Symlink(lock+".original", lock)
			case "lock-missing":
				os.Remove(lock)
			case "archive-missing":
				os.Remove(filepath.Join(state, "library/archive"))
			case "catalog-future", "catalog-duplicate", "catalog-case-alias", "catalog-unknown", "catalog-component":
				raw, _ := os.ReadFile(catalog)
				switch mode {
				case "catalog-future":
					raw = []byte(strings.Replace(string(raw), `"schema":1`, `"schema":2`, 1))
				case "catalog-duplicate":
					raw = []byte(strings.Replace(string(raw), `"schema":1`, `"schema":1,"schema":1`, 1))
				case "catalog-case-alias":
					raw = []byte(strings.Replace(string(raw), `"schema":1`, `"Schema":1`, 1))
				case "catalog-unknown":
					raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"extra":true}`)
				case "catalog-component":
					var rs records
					json.Unmarshal(raw, &rs)
					pr := rs.Profiles["default"]
					sel := pr.Packages["pack"]
					sel.Components = map[string]bool{"tool/missing": false}
					pr.Packages["pack"] = sel
					rs.Profiles["default"] = pr
					raw, _ = json.Marshal(rs)
				}
				mustWrite(t, catalog, raw)
			case "pending-public":
				mustWrite(t, filepath.Join(state, "library/pending.json"), []byte("{}"))
				os.Chmod(filepath.Join(state, "library/pending.json"), 0644)
			case "pending-unknown":
				mustWrite(t, filepath.Join(state, "library/pending.json"), []byte(`{"schema":1,"unknown":true}`))
			}
			before := snapshot(t, state)
			if _, err := Open(openState); err == nil {
				t.Fatal("unsafe/corrupt store opened")
			}
			r.stateDir = openState
			if _, err := r.Read(bg); err == nil {
				t.Fatal("unsafe/corrupt store read")
			}
			if err := r.Remove(bg, "pack", p.Digest); err == nil {
				t.Fatal("unsafe/corrupt store mutated")
			}
			if !reflect.DeepEqual(before, snapshot(t, state)) {
				t.Fatal("refusal edited or repaired content")
			}
		})
	}
}

type foreignInfo struct {
	os.FileInfo
	uid uint32
}

func (f foreignInfo) Sys() any {
	copy := *f.FileInfo.Sys().(*syscall.Stat_t)
	copy.Uid = f.uid
	return &copy
}
func TestForeignOwnershipAndStrictSelectionIDs(t *testing.T) {
	root := canonicalTemp(t)
	file := filepath.Join(root, "owned")
	mustWrite(t, file, []byte("x"))
	info, _ := os.Stat(file)
	foreign := foreignInfo{info, uint32(os.Geteuid() + 1)}
	if private(foreign, false) || ownedSource(foreign, false) {
		t.Fatal("foreign inode owner accepted")
	}
	r, state := fixtureRegistry(t)
	for _, id := range []string{"", "../bad", "Bad", "a/b", strings.Repeat("a", 65), "bad\nname"} {
		if _, err := r.CreateProfile(bg, id, "Name"); err == nil {
			t.Fatal("invalid profile id", id)
		}
	}
	if _, err := r.CreateProfile(bg, "good", "bad\x1bname"); err == nil {
		t.Fatal("control name accepted")
	}
	if _, err := r.SelectPackage(bg, "default", "pack", "../bad", true); err == nil {
		t.Fatal("invalid pin accepted")
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatal("invalid arguments created state")
	}
}

func TestPendingPointerFieldsRejectCaseAliases(t *testing.T) {
	r, state := fixtureRegistry(t)
	p := imported(t, r, "pack", "1")
	r.fault = func(at string) error {
		if at == "intent" {
			return ErrPending
		}
		return nil
	}
	if err := r.Remove(bg, "pack", p.Digest); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	pending := filepath.Join(state, "library/pending.json")
	raw, _ := os.ReadFile(pending)
	raw = []byte(strings.Replace(string(raw), `"from":`, `"From":`, 1))
	mustWrite(t, pending, raw)
	before := snapshot(t, state)
	r.fault = nil
	if err := r.Remove(bg, "pack", p.Digest); err == nil {
		t.Fatal("pointer JSON case alias accepted")
	}
	if !reflect.DeepEqual(before, snapshot(t, state)) {
		t.Fatal("ambiguous intent was overwritten")
	}
}

package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

var bg = context.Background()

func canonicalTemp(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func mustWrite(t *testing.T, name string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func fixturePack(t *testing.T, id, version string) (string, Manifest) {
	t.Helper()
	dir := canonicalTemp(t)
	files := map[string][]byte{"skills/review/SKILL.md": []byte("---\nname: review\ndescription: Read-only review\n---\nReview the code.\n"), "rules/careful.md": []byte("Preserve owned files. " + version), "bin/tool.sh": []byte("#!/bin/sh\nexit 97\n")}
	m := Manifest{Schema: 1, ID: id, Version: version, Files: map[string]string{}, Skills: []Asset{{ID: "review", Path: "skills/review"}}, Rules: []Asset{{ID: "careful", Path: "rules/careful.md"}}, Tools: []Tool{{ID: "local", Transport: "stdio", Command: []string{"bin/tool.sh", "literal $(touch nope)"}, EnvRefs: []string{"LOCAL_API_KEY"}}, {ID: "remote", Transport: "https", URL: "https://example.invalid/mcp"}}}
	for name, raw := range files {
		mustWrite(t, filepath.Join(dir, name), raw)
		m.Files[name] = hashBytes(raw)
	}
	saveManifest(t, dir, m)
	return dir, m
}
func saveManifest(t *testing.T, dir string, m Manifest) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, ManifestFilename), raw)
}
func fixtureRegistry(t *testing.T) (*registry, string) {
	t.Helper()
	state := filepath.Join(canonicalTemp(t), "state")
	r, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	return r.(*registry), state
}
func imported(t *testing.T, r Registry, id, version string) Package {
	t.Helper()
	dir, _ := fixturePack(t, id, version)
	p, err := r.Import(bg, dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(name string, d os.DirEntry, err error) error {
		if os.IsNotExist(err) && name == root {
			return nil
		}
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, name)
		info, e := os.Lstat(name)
		if e != nil {
			return e
		}
		value := fmt.Sprintf("%s:%03o", info.Mode().Type(), info.Mode().Perm())
		if info.Mode().IsRegular() {
			raw, e := os.ReadFile(name)
			if e != nil {
				return e
			}
			value += ":" + hashBytes(raw)
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(name)
			if e != nil {
				return e
			}
			value += ":" + target
		}
		out[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAbsentOpenReadResolveHaveNoSideEffects(t *testing.T) {
	r, state := fixtureRegistry(t)
	before := snapshot(t, filepath.Dir(state))
	c, err := r.Read(bg)
	if err != nil || c.Present || c.Revision != 0 || len(c.Packages) != 0 || len(c.Profiles) != 1 || c.Profiles[0].ID != "default" {
		t.Fatal(c, err)
	}
	res, err := r.Resolve(bg, "default")
	if err != nil || res.Loaded != "unknown" || res.ProfileRevision != 1 || len(res.Packages) != 0 || !digestPattern.MatchString(res.Digest) {
		t.Fatal(res, err)
	}
	if _, err = r.PackageRoot(bg, "missing", strings.Repeat("a", 64)); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = r.Resolve(bg, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = Open(state); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, filepath.Dir(state))) {
		t.Fatal("observation created state")
	}
}
func TestImmutableImportsProfilesTogglesAndStableResolution(t *testing.T) {
	r, state := fixtureRegistry(t)
	p := imported(t, r, "review-pack", "1.0.0")
	c, _ := r.Read(bg)
	before := snapshot(t, state)
	source, _ := fixturePack(t, "review-pack", "1.0.0")
	again, err := r.Import(bg, source)
	if err != nil || again.Digest != p.Digest {
		t.Fatal(again, err)
	}
	if !reflect.DeepEqual(before, snapshot(t, state)) {
		t.Fatal("same immutable import changed bytes")
	}
	if _, err = r.SelectPackage(bg, "default", p.Manifest.ID, p.Digest, true); err != nil {
		t.Fatal(err)
	}
	res, err := r.Resolve(bg, "default")
	if err != nil || res.Loaded != "unknown" || len(res.Skills) != 1 || len(res.Rules) != 1 || len(res.Tools) != 2 {
		t.Fatal(res, err)
	}
	if _, err = r.CreateProfile(bg, "work", "Work"); err != nil {
		t.Fatal(err)
	}
	v2 := imported(t, r, "review-pack", "2.0.0")
	after, _ := r.Resolve(bg, "default")
	if after.Digest != res.Digest || after.Packages[0].Digest != p.Digest || after.CatalogRevision <= res.CatalogRevision {
		t.Fatal("unrelated change moved resolution identity", after)
	}
	if _, err = r.SelectComponent(bg, "default", p.Manifest.ID, "tool/local", false); err != nil {
		t.Fatal(err)
	}
	selected, _ := r.Read(bg)
	before = snapshot(t, state)
	if _, err = r.SelectComponent(bg, "default", p.Manifest.ID, "tool/local", false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, state)) {
		t.Fatal("same component value wrote state")
	}
	if _, err = r.SelectPackage(bg, "default", p.Manifest.ID, p.Digest, false); err != nil {
		t.Fatal(err)
	}
	off, _ := r.Resolve(bg, "default")
	if len(off.Packages) != 0 || off.Digest == after.Digest {
		t.Fatal("off did not change desired identity", off)
	}
	if _, err = r.SelectPackage(bg, "default", p.Manifest.ID, p.Digest, true); err != nil {
		t.Fatal(err)
	}
	on, _ := r.Resolve(bg, "default")
	if len(on.Tools) != 1 || on.Tools[0].Tool.ID != "remote" {
		t.Fatal("off lost component preferences", on)
	}
	cat, _ := r.Read(bg)
	if cat.Profiles[0].Revision <= selected.Profiles[0].Revision || c.Revision >= cat.Revision {
		t.Fatal("revision did not progress")
	}
	before = snapshot(t, state)
	if _, err = r.SelectPackage(bg, "default", p.Manifest.ID, p.Digest, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, state)) {
		t.Fatal("same package value wrote state")
	}
	root, err := r.PackageRoot(bg, p.Manifest.ID, p.Digest)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "rules/careful.md"))
	if !strings.Contains(string(raw), "1.0.0") {
		t.Fatal("pin unexpectedly updated")
	}
	if _, err = r.SelectPackage(bg, "work", p.Manifest.ID, v2.Digest, true); err != nil {
		t.Fatal(err)
	}
	work, _ := r.Resolve(bg, "work")
	if work.Packages[0].Digest != v2.Digest {
		t.Fatal(work)
	}
}
func TestCanonicalOrderAndDigestIgnoreUnrelatedRevisions(t *testing.T) {
	r, _ := fixtureRegistry(t)
	a := imported(t, r, "a-pack", "1")
	z := imported(t, r, "z-pack", "1")
	for _, p := range []Package{z, a} {
		if _, err := r.SelectPackage(bg, "default", p.Manifest.ID, p.Digest, true); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.Resolve(bg, "default")
	if err != nil || got.Packages[0].Manifest.ID != "a-pack" || got.Tools[0].PackageID != "a-pack" {
		t.Fatal(got, err)
	}
	r2, _ := fixtureRegistry(t)
	a2 := imported(t, r2, "a-pack", "1")
	z2 := imported(t, r2, "z-pack", "1")
	for _, p := range []Package{a2, z2} {
		r2.SelectPackage(bg, "default", p.Manifest.ID, p.Digest, true)
	}
	other, _ := r2.Resolve(bg, "default")
	if got.Digest != other.Digest || !reflect.DeepEqual(got.Packages, other.Packages) {
		t.Fatal("resolution depends on operation order")
	}
	r.CreateProfile(bg, "unused", "Unused")
	imported(t, r, "unused", "1")
	later, _ := r.Resolve(bg, "default")
	if later.Digest != got.Digest || later.CatalogRevision == got.CatalogRevision {
		t.Fatal("catalog leaked into content digest")
	}
}
func TestRemovalArchivesExactRevisionAndDisablesOnlyMatchingPins(t *testing.T) {
	r, state := fixtureRegistry(t)
	one := imported(t, r, "pack", "1")
	two := imported(t, r, "pack", "2")
	r.SelectPackage(bg, "default", "pack", one.Digest, true)
	r.CreateProfile(bg, "other", "Other")
	r.SelectPackage(bg, "other", "pack", two.Digest, true)
	r.SelectComponent(bg, "default", "pack", "skill/review", false)
	root, _ := r.PackageRoot(bg, "pack", one.Digest)
	before := snapshot(t, root)
	if err := r.Remove(bg, "pack", one.Digest); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(state, "library/archive", one.Digest)
	if !reflect.DeepEqual(before, snapshot(t, archive)) {
		t.Fatal("archive did not preserve exact bytes/modes")
	}
	c, err := r.Read(bg)
	if err != nil || len(c.Packages) != 1 || c.Packages[0].Digest != two.Digest {
		t.Fatal(c, err)
	}
	sel := c.Profiles[0].Packages["pack"]
	if sel.Enabled || sel.Digest != one.Digest || sel.Components["skill/review"] {
		t.Fatal("removed selection lost identity/preferences", sel)
	}
	other, err := r.Resolve(bg, "other")
	if err != nil || len(other.Packages) != 1 || other.Packages[0].Digest != two.Digest {
		t.Fatal(other, err)
	}
	if _, err = r.PackageRoot(bg, "pack", one.Digest); !errors.Is(err, ErrNotFound) {
		t.Fatal("archived pin exposed for application", err)
	}
	before = snapshot(t, state)
	if err = r.Remove(bg, "pack", one.Digest); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, state)) {
		t.Fatal("repeated remove wrote state")
	}
}
func TestCorruptPackRefusesReadResolutionAndRemovalWithoutEffects(t *testing.T) {
	for _, mode := range []string{"edit", "undeclared", "public", "symlink", "fifo", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			r, state := fixtureRegistry(t)
			p := imported(t, r, "pack", "1")
			r.SelectPackage(bg, "default", "pack", p.Digest, true)
			root, _ := r.PackageRoot(bg, "pack", p.Digest)
			target := filepath.Join(root, "rules/careful.md")
			switch mode {
			case "edit":
				mustWrite(t, target, []byte("edited"))
			case "undeclared":
				mustWrite(t, filepath.Join(root, "extra"), []byte("extra"))
			case "public":
				os.Chmod(target, 0644)
			case "symlink":
				os.Remove(target)
				os.Symlink("/dev/null", target)
			case "fifo":
				os.Remove(target)
				if err := unix.Mkfifo(target, 0600); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				os.Link(target, filepath.Join(root, "alias"))
			}
			before := snapshot(t, state)
			if _, err := r.Read(bg); err == nil {
				t.Fatal("corrupt pack read accepted")
			}
			if _, err := r.Resolve(bg, "default"); err == nil {
				t.Fatal("corrupt pack resolved")
			}
			if _, err := r.PackageRoot(bg, "pack", p.Digest); err == nil {
				t.Fatal("corrupt root exposed")
			}
			if err := r.Remove(bg, "pack", p.Digest); err == nil {
				t.Fatal("corrupt pack removed")
			}
			if !reflect.DeepEqual(before, snapshot(t, state)) {
				t.Fatal("refusal mutated content or selection")
			}
		})
	}
}
func TestCrashRecoveryAtRemovalPublicationBoundaries(t *testing.T) {
	for _, point := range []string{"intent", "move", "catalog"} {
		t.Run(point, func(t *testing.T) {
			r, state := fixtureRegistry(t)
			p := imported(t, r, "pack", "1")
			r.SelectPackage(bg, "default", "pack", p.Digest, true)
			root, _ := r.PackageRoot(bg, "pack", p.Digest)
			packBefore := snapshot(t, root)
			r.fault = func(at string) error {
				if at == point {
					return errors.New("injected crash")
				}
				return nil
			}
			if err := r.Remove(bg, "pack", p.Digest); err == nil {
				t.Fatal("crash not injected")
			}
			before := snapshot(t, state)
			reopened, err := Open(state)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = reopened.Read(bg); !errors.Is(err, ErrPending) {
				t.Fatal("unfinished write read as committed", err)
			}
			if !reflect.DeepEqual(before, snapshot(t, state)) {
				t.Fatal("read recovered implicitly")
			}
			if err = reopened.Remove(bg, "pack", p.Digest); err != nil {
				t.Fatal("safe recovery failed", err)
			}
			c, err := reopened.Read(bg)
			if err != nil || len(c.Packages) != 0 || c.Profiles[0].Packages["pack"].Enabled {
				t.Fatal(c, err)
			}
			if !reflect.DeepEqual(packBefore, snapshot(t, filepath.Join(state, "library/archive", p.Digest))) {
				t.Fatal("crash recovery lost package bytes")
			}
			if _, err = os.Lstat(filepath.Join(state, "library/pending.json")); !os.IsNotExist(err) {
				t.Fatal("completed intent survived", err)
			}
		})
	}
}
func TestCrashRecoveryRefusesEditedArchivedPackageOrDivergedCatalog(t *testing.T) {
	for _, mode := range []string{"archive", "catalog"} {
		t.Run(mode, func(t *testing.T) {
			r, state := fixtureRegistry(t)
			p := imported(t, r, "pack", "1")
			r.SelectPackage(bg, "default", "pack", p.Digest, true)
			r.fault = func(at string) error {
				if at == "move" {
					return errors.New("crash")
				}
				return nil
			}
			r.Remove(bg, "pack", p.Digest)
			if mode == "archive" {
				mustWrite(t, filepath.Join(state, "library/archive", p.Digest, "rules/careful.md"), []byte("edited archive"))
			} else {
				catalog := filepath.Join(state, "library/catalog.json")
				raw, _ := os.ReadFile(catalog)
				var rs records
				json.Unmarshal(raw, &rs)
				rs.Revision++
				raw, _ = json.Marshal(rs)
				mustWrite(t, catalog, raw)
			}
			before := snapshot(t, state)
			r.fault = nil
			if err := r.Remove(bg, "pack", p.Digest); err == nil {
				t.Fatal("ambiguous recovery accepted")
			}
			if !reflect.DeepEqual(before, snapshot(t, state)) {
				t.Fatal("failed recovery overwrote evidence")
			}
		})
	}
}
func TestImportCrashRecoveryAndOwnedConcurrentMutations(t *testing.T) {
	r, state := fixtureRegistry(t)
	source, _ := fixturePack(t, "pack", "1")
	r.fault = func(at string) error {
		if at == "move" {
			return errors.New("crash")
		}
		return nil
	}
	if _, err := r.Import(bg, source); err == nil {
		t.Fatal("import crash not injected")
	}
	reopened, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	p, err := reopened.Import(bg, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.PackageRoot(bg, "pack", p.Digest); err != nil {
		t.Fatal(err)
	}
	const count = 12
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			other, e := Open(state)
			if e == nil {
				_, e = other.CreateProfile(bg, fmt.Sprintf("profile-%02d", i), fmt.Sprintf("Profile %d", i))
			}
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	c, err := reopened.Read(bg)
	if err != nil || len(c.Profiles) != count+1 {
		t.Fatal("lost concurrent profile writes", c, err)
	}
}
func TestLockCancellationAndReplacementRefuseMutation(t *testing.T) {
	r, state := fixtureRegistry(t)
	imported(t, r, "pack", "1")
	held, err := r.openStore(bg, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(bg, 25*time.Millisecond)
	defer cancel()
	before := snapshot(t, state)
	if _, err = r.CreateProfile(ctx, "blocked", "Blocked"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot(t, state)) {
		t.Fatal("cancelled mutation wrote catalog")
	}
	path := filepath.Join(state, "library/library.lock")
	os.Rename(path, path+".old")
	mustWrite(t, path, []byte{})
	if held.anchored() {
		t.Fatal("replaced lock inode remained anchored")
	}
	held.close()
}

func TestOpenHasFiniteHeldWriterLockRefusalWithoutEffects(t *testing.T) {
	r, state := fixtureRegistry(t)
	imported(t, r, "pack", "1")
	held, err := r.openStore(bg, true)
	if err != nil {
		t.Fatal(err)
	}
	defer held.close()
	before := snapshot(t, state)
	start := time.Now()
	if _, err = Open(state); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Open did not bound held writer lock", err)
	}
	elapsed := time.Since(start)
	if elapsed < 4*time.Second || elapsed > 7*time.Second {
		t.Fatal("unexpected Open lock bound", elapsed)
	}
	if !reflect.DeepEqual(before, snapshot(t, state)) {
		t.Fatal("timed-out Open created/repaired state")
	}
}

func TestCatalogDriftAfterIntentRefusesArchiveBeforeEffects(t *testing.T) {
	r, state := fixtureRegistry(t)
	p := imported(t, r, "pack", "1")
	r.SelectPackage(bg, "default", "pack", p.Digest, true)
	root, _ := r.PackageRoot(bg, "pack", p.Digest)
	packBefore := snapshot(t, root)
	catalog := filepath.Join(state, "library/catalog.json")
	var edited []byte
	r.fault = func(at string) error {
		if at == "intent" {
			raw, err := os.ReadFile(catalog)
			if err != nil {
				return err
			}
			var rs records
			if err = json.Unmarshal(raw, &rs); err != nil {
				return err
			}
			profile := rs.Profiles["default"]
			profile.Name = "Explicit external edit"
			rs.Profiles["default"] = profile
			edited, err = json.Marshal(rs)
			if err != nil {
				return err
			}
			return os.WriteFile(catalog, edited, 0600)
		}
		return nil
	}
	if err := r.Remove(bg, "pack", p.Digest); !errors.Is(err, ErrCorrupt) {
		t.Fatal("catalog drift accepted", err)
	}
	if !reflect.DeepEqual(packBefore, snapshot(t, root)) {
		t.Fatal("drift still moved/edited package")
	}
	raw, _ := os.ReadFile(catalog)
	if string(raw) != string(edited) {
		t.Fatal("external catalog edit overwritten")
	}
	if _, err := os.Lstat(filepath.Join(state, "library/archive", p.Digest)); !os.IsNotExist(err) {
		t.Fatal("archive side effect despite drift", err)
	}
	if _, err := r.Read(bg); !errors.Is(err, ErrPending) {
		t.Fatal("intent/evidence not preserved", err)
	}
}

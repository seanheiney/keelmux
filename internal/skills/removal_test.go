package skills

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func removalFixture(t *testing.T) (string, *tree, ledger, entry) {
	t.Helper()
	root := t.TempDir()
	apply(t, preview(t, root, "codex"))
	tr, err := openTree(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.close)
	l, err := readLedger(tr)
	if err != nil {
		t.Fatal(err)
	}
	return tr.path, tr, l, l.Entries[key(UserScope, "codex")]
}
func onlyResult(t *testing.T, r []Result) Result {
	t.Helper()
	if len(r) != 1 {
		t.Fatalf("results=%v", r)
	}
	return r[0]
}

func TestSuccessfulRemovalRetainsArchiveAndOpenDescriptorEdits(t *testing.T) {
	root, _, _, _ := removalFixture(t)
	source := filepath.Join(root, directory("codex"), "SKILL.md")
	fd, err := os.OpenFile(source, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	r, err := Remove(root, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	result := onlyResult(t, r)
	if result.Action != "removed" || result.RetainedPath == "" {
		t.Fatalf("missing retained removal: %+v", result)
	}
	if _, err = os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("native source remains: %v", err)
	}
	// A live editor's descriptor still points into the archive. The installer
	// cannot promise no future edit, so it must retain the inode indefinitely.
	if err = fd.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err = fd.WriteAt([]byte("late open-descriptor edit"), 0); err != nil {
		t.Fatal(err)
	}
	if err = fd.Sync(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(result.RetainedPath, "SKILL.md"))
	if err != nil || string(b) != "late open-descriptor edit" {
		t.Fatalf("open-descriptor edit lost: %q %v", b, err)
	}
	r, err = Remove(root, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	again := onlyResult(t, r)
	if again.Action != "not_owned" || again.RetainedPath != result.RetainedPath {
		t.Fatal("retry discarded archive receipt")
	}
	apply(t, preview(t, root, "codex"))
	b, err = os.ReadFile(filepath.Join(result.RetainedPath, "SKILL.md"))
	if err != nil || string(b) != "late open-descriptor edit" {
		t.Fatal("reinstall erased prior archive")
	}
}

func TestPostCheckFilenameReplacementRestoresWholeBundle(t *testing.T) {
	root, tr, l, e := removalFixture(t)
	if err := checkBundle(tr, e.Directory, e.Files, false); err != nil {
		t.Fatal(err)
	}
	r, err := prepareRemoval(tr, &l, e, e.Directory, false)
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic boundary: replacement happens after ownership/digests were
	// checked and the intent was persisted, immediately before the atomic move.
	file := filepath.Join(root, e.Directory, "SKILL.md")
	replacement := file + ".editor"
	writeFixture(t, replacement, []byte("replacement edited by user"))
	if err = os.Rename(replacement, file); err != nil {
		t.Fatal(err)
	}
	if err = tr.publish(r.Source, r.Archive); err != nil {
		t.Fatal(err)
	}
	result := finishRemoval(tr, &l, r.ID, mapping("codex", UserScope))
	if result.Action != "preserve_edited" {
		t.Fatalf("changed bundle not restored: %+v", result)
	}
	b, err := os.ReadFile(file)
	if err != nil || string(b) != "replacement edited by user" {
		t.Fatalf("replacement deleted: %q %v", b, err)
	}
	if exists, err := tr.exists(r.Archive); err != nil || exists {
		t.Fatal("restoration acknowledgement incorrect")
	}
	loaded, err := readLedger(tr)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Removals[r.ID].State != "restored" || loaded.Entries[key(UserScope, "codex")].RemovalID != "" {
		t.Fatal("restoration not durable")
	}
}

func TestChangedArchiveAndOccupiedSourcePreserveBothNamespaces(t *testing.T) {
	root, tr, l, e := removalFixture(t)
	r, err := prepareRemoval(tr, &l, e, e.Directory, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = tr.publish(r.Source, r.Archive); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, r.Archive, "SKILL.md"), []byte("archived user edit"))
	writeFixture(t, filepath.Join(root, r.Source, "SKILL.md"), []byte("fresh independent source"))
	result := finishRemoval(tr, &l, r.ID, mapping("codex", UserScope))
	if result.Action != "preserve_edited_retained" || result.RetainedPath == "" {
		t.Fatalf("namespaces not retained: %+v", result)
	}
	for path, want := range map[string]string{filepath.Join(root, r.Archive, "SKILL.md"): "archived user edit", filepath.Join(root, r.Source, "SKILL.md"): "fresh independent source"} {
		b, err := os.ReadFile(path)
		if err != nil || string(b) != want {
			t.Fatalf("%s lost: %q %v", path, b, err)
		}
	}
	if _, ok := l.Entries[key(UserScope, "codex")]; ok {
		t.Fatal("fresh unowned source inherited old installation authority")
	}
	results, err := Remove(root, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if onlyResult(t, results).Action != "not_owned" {
		t.Fatal("retry adopted fresh source")
	}
}

func TestLostMoveAcknowledgementNeverMovesFreshSource(t *testing.T) {
	root, tr, l, e := removalFixture(t)
	r, err := prepareRemoval(tr, &l, e, e.Directory, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = tr.publish(r.Source, r.Archive); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, r.Source, "SKILL.md"), []byte("fresh namespace after lost ack"))
	loaded, err := readLedger(tr)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Removals[r.ID].State != "moving" {
		t.Fatal("fixture accidentally acknowledged move")
	}
	results, err := Remove(root, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	result := onlyResult(t, results)
	if result.Action != "removed" || result.RetainedPath != filepath.Join(root, r.Archive) {
		t.Fatalf("lost ack not reconciled: %+v", result)
	}
	b, err := os.ReadFile(filepath.Join(root, r.Source, "SKILL.md"))
	if err != nil || string(b) != "fresh namespace after lost ack" {
		t.Fatal("old removal moved a fresh source")
	}
	b, err = os.ReadFile(filepath.Join(root, r.Archive, "SKILL.md"))
	if err != nil || digest(b) != digest(catalogue("codex")["SKILL.md"]) {
		t.Fatal("original archive changed")
	}
}

func TestSourceChangedBeforeUnattemptedMoveCancelsOldIntent(t *testing.T) {
	root, tr, l, e := removalFixture(t)
	r, err := prepareRemoval(tr, &l, e, e.Directory, false)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, e.Directory, "SKILL.md"), []byte("changed before attempt"))
	results, err := Remove(root, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if onlyResult(t, results).Action != "preserve_edited" {
		t.Fatal("changed unattempted source moved")
	}
	if exists, err := tr.exists(r.Archive); err != nil || exists {
		t.Fatal("unattempted intent fabricated an archive")
	}
}

func TestSameRootScopesCannotAcquireDuplicateDirectoryAuthority(t *testing.T) {
	root := t.TempDir()
	p, err := PlanScope(root, ProjectScope, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, p)
	user := preview(t, root, "codex")
	if user.Mappings[0].Action != "scope_conflict" {
		t.Fatal("same root scope conflict hidden")
	}
	if onlyResult(t, apply(t, user)).Action != "scope_conflict" {
		t.Fatal("second scope acquired authority")
	}
	results, err := Remove(root, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if onlyResult(t, results).Action != "not_owned" {
		t.Fatal("wrong scope removed project bundle")
	}
	results, err = RemoveScope(root, ProjectScope, []string{"codex"})
	if err != nil || onlyResult(t, results).Action != "removed" {
		t.Fatal("owning scope removal failed")
	}
	if onlyResult(t, apply(t, user)).Action != "preview_changed" {
		t.Fatal("old collision preview silently added a skill")
	}
	if onlyResult(t, apply(t, preview(t, root, "codex"))).Action != "installed" {
		t.Fatal("fresh reviewed scope could not install")
	}
}

func TestLegacyRemovalWithoutMarkerIsPreservedConservatively(t *testing.T) {
	root, tr, l, e := removalFixture(t)
	e.State = "removing"
	l.Entries[key(UserScope, "codex")] = e
	if err := writeLedger(tr, l); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, e.Directory, marker)); err != nil {
		t.Fatal(err)
	}
	results, err := Remove(root, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	result := onlyResult(t, results)
	if result.Action != "preserve_edited" || result.RecoveryHint == "" {
		t.Fatal("legacy unattributable partial removal adopted")
	}
	if _, err = os.Stat(filepath.Join(root, e.Directory, "SKILL.md")); err != nil {
		t.Fatal("legacy bytes deleted")
	}
}

func TestRetainedArchiveAndMetadataMustStayPrivate(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, ".backplane", "sentinel"), []byte("private"))
	if err := os.Chmod(filepath.Join(root, ".backplane"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(preview(t, root, "codex")); err == nil {
		t.Fatal("public ownership authority accepted")
	}
	root, tr, l, e := removalFixture(t)
	d, err := tr.dir(".backplane/removals", true)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if err := os.Chmod(filepath.Join(root, ".backplane", "removals"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareRemoval(tr, &l, e, e.Directory, false); err == nil {
		t.Fatal("public retention archive accepted")
	}
	if _, err := os.Stat(filepath.Join(root, e.Directory, "SKILL.md")); err != nil {
		t.Fatal("unsafe archive changed source")
	}
}

func TestRemovalCrashHelper(t *testing.T) {
	root := os.Getenv("BP_TEST_REMOVAL_CRASH_ROOT")
	if root == "" {
		return
	}
	tr, err := openTree(root)
	if err != nil {
		panic(err)
	}
	lock, err := lockLedger(tr)
	if err != nil {
		panic(err)
	}
	_ = lock
	l, err := readLedger(tr)
	if err != nil {
		panic(err)
	}
	e := l.Entries[key(UserScope, "codex")]
	r, err := prepareRemoval(tr, &l, e, e.Directory, false)
	if err != nil {
		panic(err)
	}
	mode := os.Getenv("BP_TEST_REMOVAL_CRASH_MODE")
	if mode == "before_move" {
		os.Exit(73)
	}
	if err = tr.publish(r.Source, r.Archive); err != nil {
		panic(err)
	}
	if mode == "after_restore" {
		if err = os.WriteFile(filepath.Join(root, r.Archive, "SKILL.md"), []byte("restored user edit"), 0600); err != nil {
			panic(err)
		}
		r.State = "restoring"
		l.Removals[r.ID] = r
		if err = writeLedger(tr, l); err != nil {
			panic(err)
		}
		if err = tr.publish(r.Archive, r.Source); err != nil {
			panic(err)
		}
	}
	os.Exit(73)
}

func TestAbruptExitReconcilesIntentMoveAndRestoration(t *testing.T) {
	for _, mode := range []string{"before_move", "after_move", "after_restore"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			apply(t, preview(t, root, "codex"))
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "-test.run=^TestRemovalCrashHelper$")
			cmd.Env = append(os.Environ(), "BP_TEST_REMOVAL_CRASH_ROOT="+root, "BP_TEST_REMOVAL_CRASH_MODE="+mode)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 73 {
				t.Fatalf("crash helper %v %s", err, output)
			}
			if mode == "after_move" {
				writeFixture(t, filepath.Join(root, directory("codex"), "SKILL.md"), []byte("fresh source after crash"))
			}
			results, err := Remove(root, []string{"codex"})
			if err != nil {
				t.Fatal(err)
			}
			result := onlyResult(t, results)
			if mode == "after_restore" {
				if result.Action != "preserve_edited" {
					t.Fatalf("restore replay=%+v", result)
				}
				b, err := os.ReadFile(filepath.Join(root, directory("codex"), "SKILL.md"))
				if err != nil || string(b) != "restored user edit" {
					t.Fatal("crashed restoration lost edits")
				}
			} else {
				if result.Action != "removed" || result.RetainedPath == "" {
					t.Fatalf("move replay=%+v", result)
				}
				if mode == "after_move" {
					b, err := os.ReadFile(filepath.Join(root, directory("codex"), "SKILL.md"))
					if err != nil || string(b) != "fresh source after crash" {
						t.Fatal("crashed removal moved replacement")
					}
				}
			}
		})
	}
}

func TestConcurrentApplyAndRemovePreserveSourceOrExactArchive(t *testing.T) {
	for range 8 {
		root, tr, _, _ := removalFixture(t)
		p := preview(t, root, "codex")
		if p.Mappings[0].Action != "unchanged" {
			t.Fatal("fixture not installed")
		}
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, err := Apply(p); errs <- err }()
		go func() { defer wg.Done(); <-start; _, err := Remove(root, []string{"codex"}); errs <- err }()
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil && !strings.Contains(err.Error(), "open in another installer") {
				t.Fatal(err)
			}
		}
		l, err := readLedger(tr)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		if b, err := tr.read(filepath.Join(directory("codex"), "SKILL.md")); err == nil {
			if digest(b) != digest(catalogue("codex")["SKILL.md"]) {
				t.Fatal("concurrent source changed")
			}
			found = true
		}
		for _, r := range l.Removals {
			if b, err := tr.read(filepath.Join(r.Archive, "SKILL.md")); err == nil {
				if digest(b) != digest(catalogue("codex")["SKILL.md"]) {
					t.Fatal("concurrent archive changed")
				}
				found = true
			}
		}
		if !found {
			t.Fatal("concurrent operations lost both source and archive")
		}
	}
}

func TestAlteredRemovalReceiptsCannotRedirectOrWeakenRemoval(t *testing.T) {
	for _, change := range []string{"source", "archive", "snapshot", "allow_missing", "binding"} {
		t.Run(change, func(t *testing.T) {
			root, tr, l, e := removalFixture(t)
			r, err := prepareRemoval(tr, &l, e, e.Directory, false)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "source":
				r.Source = ".claude"
			case "archive":
				r.Archive = ".claude/settings.json"
			case "snapshot":
				r.Installation.Stage = ".backplane/unowned"
			case "allow_missing":
				r.AllowMissing = true
			case "binding":
				active := l.Entries[key(UserScope, "codex")]
				active.RemovalID = "missing"
				l.Entries[key(UserScope, "codex")] = active
			}
			l.Removals[r.ID] = r
			if err = writeLedger(tr, l); err != nil {
				t.Fatal(err)
			}
			if _, err = Remove(root, []string{"codex"}); err == nil {
				t.Fatal("altered receipt accepted")
			}
			b, err := os.ReadFile(filepath.Join(root, e.Directory, "SKILL.md"))
			if err != nil || digest(b) != digest(catalogue("codex")["SKILL.md"]) {
				t.Fatal("invalid receipt changed native source")
			}
		})
	}
}

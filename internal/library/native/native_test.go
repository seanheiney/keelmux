package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/seanheiney/keelmux/internal/library"
	"golang.org/x/sys/unix"
)

type fixtureRegistry struct {
	library.Registry
	r     library.Resolution
	roots map[string]string
}

func (f *fixtureRegistry) Resolve(context.Context, string) (library.Resolution, error) {
	return f.r, nil
}
func (f *fixtureRegistry) PackageRoot(_ context.Context, id, digest string) (string, error) {
	for _, p := range f.r.Packages {
		if p.Manifest.ID == id && p.Digest == digest {
			return f.roots[id], nil
		}
	}
	return "", ErrStale
}
func privateDir(t *testing.T) string {
	t.Helper()
	path, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(path, 0700); e != nil {
		t.Fatal(e)
	}
	return path
}
func put(t *testing.T, root, path string, b []byte) {
	t.Helper()
	full := filepath.Join(root, path)
	if e := os.MkdirAll(filepath.Dir(full), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(full, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T, harness string) (*fixtureRegistry, Target) {
	t.Helper()
	root := privateDir(t)
	files := map[string][]byte{"skills/review/SKILL.md": []byte("---\nname: review\ndescription: Review a bounded change\n---\nRead the source and report findings.\n"), "skills/review/assets/check.txt": []byte("immutable companion"), "rules/review.md": []byte("Preserve exact source identity.\n")}
	for p, b := range files {
		put(t, root, p, b)
	}
	m := library.Manifest{Schema: 1, ID: "pack", Version: "1.0", Files: map[string]string{}, Skills: []library.Asset{{ID: "review", Path: "skills/review"}}, Rules: []library.Asset{{ID: "source", Path: "rules/review.md"}}, Tools: []library.Tool{{ID: "lookup", Transport: "stdio", Command: []string{"fixture-tool"}, EnvRefs: []string{"EXAMPLE_TOKEN"}}, {ID: "remote", Transport: "https", URL: "https://example.invalid/mcp"}}}
	for p, b := range files {
		m.Files[p] = hash(b)
	}
	p := library.Package{Manifest: m, Digest: hash(canonical(m))}
	r := library.Resolution{ProfileID: "default", ProfileRevision: 1, CatalogRevision: 1, Packages: []library.Package{p}, Skills: []library.ResolvedAsset{{PackageID: m.ID, PackageDigest: p.Digest, Asset: m.Skills[0]}}, Rules: []library.ResolvedAsset{{PackageID: m.ID, PackageDigest: p.Digest, Asset: m.Rules[0]}}, Tools: []library.ResolvedTool{{PackageID: m.ID, PackageDigest: p.Digest, Tool: m.Tools[0]}, {PackageID: m.ID, PackageDigest: p.Digest, Tool: m.Tools[1]}}, Loaded: "unknown"}
	r.Digest = hash(canonical(r))
	return &fixtureRegistry{r: r, roots: map[string]string{"pack": root}}, Target{Harness: harness, Workspace: privateDir(t), ApplicationID: "app"}
}
func revise(f *fixtureRegistry) {
	f.r.ProfileRevision++
	f.r.Digest = ""
	f.r.Digest = hash(canonical(f.r))
}
func previewMust(t *testing.T, f *fixtureRegistry, target Target) Plan {
	t.Helper()
	p, e := Preview(context.Background(), f, f.r, target)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func applyMust(t *testing.T, f *fixtureRegistry, target Target) Receipt {
	t.Helper()
	r, e := Apply(context.Background(), f, previewMust(t, f, target))
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func noLedger(t *testing.T, target Target) {
	t.Helper()
	if _, e := os.Lstat(filepath.Join(target.Workspace, ledgerDir)); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("state unexpectedly created: %v", e)
	}
}
func content(p Plan, path string) []byte {
	for _, op := range p.Files {
		if op.Path == path {
			return op.Content
		}
	}
	return nil
}

func TestPreviewAndAbsentStatusNeverCreateState(t *testing.T) {
	f, target := fixture(t, "codex")
	p := previewMust(t, f, target)
	if len(p.Files) != 4 || len(p.Collisions) != 0 || p.Loaded != "unknown" {
		t.Fatalf("%+v", p)
	}
	s, e := Status(context.Background(), target)
	if e != nil || s.Present || s.Verified {
		t.Fatalf("%+v %v", s, e)
	}
	s, e = Recover(context.Background(), target)
	if e != nil || s.Present {
		t.Fatalf("%+v %v", s, e)
	}
	noLedger(t, target)
	target.ApplicationID = "../escape"
	if _, e = Status(context.Background(), target); !errors.Is(e, ErrUnsafe) {
		t.Fatal(e)
	}
}
func TestFiveHarnessMappingsAndNoEnvironmentValues(t *testing.T) {
	t.Setenv("EXAMPLE_TOKEN", "never-copy-this-secret")
	for _, h := range []string{"codex", "claude", "gemini", "grok", "opencode"} {
		t.Run(h, func(t *testing.T) {
			f, target := fixture(t, h)
			p := previewMust(t, f, target)
			raw := canonical(p)
			if bytes.Contains(raw, []byte("never-copy-this-secret")) {
				t.Fatal("environment value copied")
			}
			if p.Loaded != "unknown" || p.Reload != "pending_or_unknown" {
				t.Fatal("invented native state")
			}
			if h == "grok" {
				if len(p.Unmapped) != 2 || len(p.LaunchArgs) != 2 || p.LaunchArgs[0] != "--rules" || p.LaunchArgs[1] != string(content(p, appPath(target, "generated/rules.md"))) || !strings.Contains(p.LaunchArgs[1], "not native skill discovery") {
					t.Fatalf("%+v", p)
				}
			} else {
				if len(p.Unmapped) != 0 {
					t.Fatal(p.Unmapped)
				}
				var path string
				switch h {
				case "codex":
					path = ".codex/config.toml"
					if !bytes.Contains(content(p, path), []byte("args = []")) {
						t.Fatal("invalid TOML empty args")
					}
				case "claude":
					path = appPath(target, "generated/mcp.json")
					if p.LaunchArgs[0] != "--mcp-config" || p.LaunchArgs[1] != filepath.Join(target.Workspace, path) {
						t.Fatal(p.LaunchArgs)
					}
				case "gemini":
					path = ".gemini/settings.json"
				case "opencode":
					path = "opencode.json"
				}
				if h != "codex" {
					var obj map[string]any
					if e := json.Unmarshal(content(p, path), &obj); e != nil {
						t.Fatal(e)
					}
				}
			}
			noLedger(t, target)
		})
	}
}
func TestApplyRestartIdempotencyAndRemovalPreservesUnrelated(t *testing.T) {
	f, target := fixture(t, "codex")
	put(t, target.Workspace, "unrelated.txt", []byte("keep"))
	r := applyMust(t, f, target)
	s, e := Status(context.Background(), target)
	if e != nil || !s.Verified || s.Receipt.Digest != r.Digest {
		t.Fatalf("%+v %v", s, e)
	}
	r2 := applyMust(t, f, target)
	if r2.Digest != r.Digest || r2.Revision != 1 {
		t.Fatal("idempotent retry changed receipt")
	}
	removed, e := Remove(context.Background(), target, r.Digest)
	if e != nil || removed.Applied || len(removed.Files) > 0 || len(removed.LaunchArgs) > 0 {
		t.Fatalf("%+v %v", removed, e)
	}
	s, e = Status(context.Background(), target)
	if e != nil || !s.Verified || s.Receipt.Applied {
		t.Fatalf("%+v %v", s, e)
	}
	b, e := os.ReadFile(filepath.Join(target.Workspace, "unrelated.txt"))
	if e != nil || string(b) != "keep" {
		t.Fatal("foreign file changed")
	}
	if _, e = os.Stat(filepath.Join(target.Workspace, ".agents")); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("owned empty directories not removed: %v", e)
	}
	_ = applyMust(t, f, target)
}
func TestSameApplicationReviewedUpdateAndToggle(t *testing.T) {
	f, target := fixture(t, "gemini")
	r := applyMust(t, f, target)
	skills := f.r.Skills
	f.r.Skills = nil
	revise(f)
	off := applyMust(t, f, target)
	if off.Revision != r.Revision+1 || off.ResolutionDigest == r.ResolutionDigest {
		t.Fatal("no reviewed update")
	}
	if _, e := os.Stat(filepath.Join(target.Workspace, ".gemini/skills/review/SKILL.md")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("skill toggle did not remove owned file")
	}
	f.r.Skills = skills
	revise(f)
	on := applyMust(t, f, target)
	if on.Revision != off.Revision+1 {
		t.Fatal("skill re-enable failed")
	}
	s, e := Status(context.Background(), target)
	if e != nil || !s.Verified {
		t.Fatalf("%+v %v", s, e)
	}
}
func TestOwnedDriftRefusesStatusUpdateAndRemove(t *testing.T) {
	f, target := fixture(t, "claude")
	r := applyMust(t, f, target)
	path := r.Files[0].Path
	put(t, target.Workspace, path, []byte("edited"))
	s, e := Status(context.Background(), target)
	if !errors.Is(e, ErrDrift) || s.Verified {
		t.Fatalf("%+v %v", s, e)
	}
	if _, e = Preview(context.Background(), f, f.r, target); !errors.Is(e, ErrDrift) {
		t.Fatal(e)
	}
	if _, e = Remove(context.Background(), target, r.Digest); !errors.Is(e, ErrDrift) {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(target.Workspace, path))
	if string(b) != "edited" {
		t.Fatal("edit overwritten")
	}
}
func TestNativeEntryAndSharedSkillCollisionsPreserved(t *testing.T) {
	for _, h := range []string{"codex", "gemini", "opencode"} {
		t.Run(h, func(t *testing.T) {
			f, target := fixture(t, h)
			path := map[string]string{"codex": "AGENTS.md", "gemini": "GEMINI.md", "opencode": "opencode.json"}[h]
			put(t, target.Workspace, path, []byte("foreign"))
			p := previewMust(t, f, target)
			if len(p.Collisions) == 0 {
				t.Fatal("missing collision")
			}
			if _, e := Apply(context.Background(), f, p); !errors.Is(e, ErrCollision) {
				t.Fatal(e)
			}
			noLedger(t, target)
			b, _ := os.ReadFile(filepath.Join(target.Workspace, path))
			if string(b) != "foreign" {
				t.Fatal("foreign bytes changed")
			}
		})
	}
	f, target := fixture(t, "gemini")
	put(t, target.Workspace, ".agents/skills/review/SKILL.md", []byte("alias wins"))
	p := previewMust(t, f, target)
	if len(p.Collisions) == 0 {
		t.Fatal("shared skill alias ignored")
	}
	noLedger(t, target)
}
func TestImmutablePackageChangeAndForgedPlanRefusedBeforeEffects(t *testing.T) {
	f, target := fixture(t, "codex")
	p := previewMust(t, f, target)
	p.LaunchArgs = []string{"--unsafe"}
	p.Digest = sealPlan(p)
	if _, e := Apply(context.Background(), f, p); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
	noLedger(t, target)
	p = previewMust(t, f, target)
	put(t, f.roots["pack"], "rules/review.md", []byte("modified"))
	if _, e := Apply(context.Background(), f, p); !errors.Is(e, ErrDrift) {
		t.Fatal(e)
	}
	noLedger(t, target)
}
func TestStaleResolutionAndGuardRefusedBeforeEffects(t *testing.T) {
	f, target := fixture(t, "opencode")
	p := previewMust(t, f, target)
	old := f.r
	revise(f)
	if _, e := Preview(context.Background(), f, old, target); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
	if _, e := Apply(context.Background(), f, p); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
	noLedger(t, target)
	p = previewMust(t, f, target)
	put(t, target.Workspace, "opencode.jsonc", []byte("{}"))
	if _, e := Apply(context.Background(), f, p); !errors.Is(e, ErrStale) {
		t.Fatal(e)
	}
	noLedger(t, target)
}
func TestUnsafeExistingStateAndNativeFilesAreNotTightened(t *testing.T) {
	for _, kind := range []string{"ledger-mode", "lock-mode", "symlink", "fifo", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			f, target := fixture(t, "codex")
			p := previewMust(t, f, target)
			var path string
			switch kind {
			case "ledger-mode":
				path = filepath.Join(target.Workspace, ledgerDir)
				if e := os.Mkdir(path, 0755); e != nil {
					t.Fatal(e)
				}
			case "lock-mode":
				put(t, target.Workspace, filepath.Join(ledgerDir, "lock"), []byte("keep"))
				path = filepath.Join(target.Workspace, ledgerDir, "lock")
				os.Chmod(path, 0644)
			case "symlink":
				path = filepath.Join(target.Workspace, "AGENTS.md")
				if e := os.Symlink(filepath.Join(f.roots["pack"], "rules/review.md"), path); e != nil {
					t.Fatal(e)
				}
			case "fifo":
				path = filepath.Join(target.Workspace, "AGENTS.md")
				if e := unix.Mkfifo(path, 0600); e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				path = filepath.Join(target.Workspace, "AGENTS.md")
				if e := os.Link(filepath.Join(f.roots["pack"], "rules/review.md"), path); e != nil {
					t.Fatal(e)
				}
			}
			before, e := os.Lstat(path)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = Apply(context.Background(), f, p); !errors.Is(e, ErrUnsafe) {
				t.Fatalf("%s: %v", kind, e)
			}
			after, e := os.Lstat(path)
			if e != nil || before.Mode() != after.Mode() {
				t.Fatal("unsafe mode/type mutated")
			}
		})
	}
}
func TestOwnedFilePermissionDriftNotClaimedVerified(t *testing.T) {
	f, target := fixture(t, "codex")
	r := applyMust(t, f, target)
	path := filepath.Join(target.Workspace, r.Files[0].Path)
	if e := os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	s, e := Status(context.Background(), target)
	if !errors.Is(e, ErrUnsafe) || s.Verified {
		t.Fatalf("%+v %v", s, e)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0644 {
		t.Fatal("mode silently tightened")
	}
}
func TestExplicitRecoverAcrossEachCommitBoundary(t *testing.T) {
	for _, stage := range []string{"intent", "file:", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			f, target := fixture(t, "codex")
			p := previewMust(t, f, target)
			crash := errors.New("simulated lost acknowledgement")
			_, e := apply(context.Background(), f, p, func(at string) error {
				if strings.HasPrefix(at, stage) {
					return crash
				}
				return nil
			})
			if !errors.Is(e, crash) {
				t.Fatal(e)
			}
			s, e := Status(context.Background(), target)
			if !errors.Is(e, ErrPending) || !s.Pending || s.Verified {
				t.Fatalf("%+v %v", s, e)
			}
			if _, e = Apply(context.Background(), f, p); !errors.Is(e, ErrPending) {
				t.Fatal(e)
			}
			s, e = Recover(context.Background(), target)
			if e != nil || !s.Verified || s.Pending || !s.Receipt.Applied {
				t.Fatalf("%+v %v", s, e)
			}
			r := applyMust(t, f, target)
			if r.Revision != 1 || r.Digest != s.Receipt.Digest {
				t.Fatal("recover replay changed receipt")
			}
		})
	}
}
func TestRecoveryRevalidatesWholeBatchBeforeContinuing(t *testing.T) {
	f, target := fixture(t, "codex")
	p := previewMust(t, f, target)
	_, e := apply(context.Background(), f, p, func(at string) error {
		if at == "intent" {
			return errors.New("crash")
		}
		return nil
	})
	if e == nil {
		t.Fatal("missing injected failure")
	}
	last := p.Files[len(p.Files)-1]
	put(t, target.Workspace, last.Path, []byte("foreign-after-intent"))
	s, e := Recover(context.Background(), target)
	if !errors.Is(e, ErrDrift) || !s.Pending {
		t.Fatalf("%+v %v", s, e)
	}
	first := p.Files[0]
	if _, e = os.Stat(filepath.Join(target.Workspace, first.Path)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("partial hidden continuation before batch validation")
	}
	b, _ := os.ReadFile(filepath.Join(target.Workspace, last.Path))
	if string(b) != "foreign-after-intent" {
		t.Fatal("foreign replaced")
	}
}
func TestCancellationBeforeAndAfterDurableIntent(t *testing.T) {
	f, target := fixture(t, "codex")
	p := previewMust(t, f, target)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Apply(ctx, f, p); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	noLedger(t, target)
	ctx, cancel = context.WithCancel(context.Background())
	_, e := apply(ctx, f, p, func(at string) error {
		if at == "intent" {
			cancel()
		}
		return nil
	})
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	s, e := Status(context.Background(), target)
	if !errors.Is(e, ErrPending) || s.Verified {
		t.Fatal(e)
	}
	if _, e = Recover(context.Background(), target); e != nil {
		t.Fatal(e)
	}
}
func TestGrokRulesArgumentOverflowHasNoEffects(t *testing.T) {
	f, target := fixture(t, "grok")
	b := bytes.Repeat([]byte("x"), maxRulesArg)
	put(t, f.roots["pack"], "rules/review.md", b)
	f.r.Packages[0].Manifest.Files["rules/review.md"] = hash(b)
	if _, e := Preview(context.Background(), f, f.r, target); !errors.Is(e, ErrUnsupported) {
		t.Fatal(e)
	}
	noLedger(t, target)
}
func TestUnsupportedFieldsRemainExplicitAndNoToolExecution(t *testing.T) {
	f, target := fixture(t, "codex")
	b := []byte("---\nname: review\ndescription: Review\nallowed-tools: Bash\n---\nbody\n")
	put(t, f.roots["pack"], "skills/review/SKILL.md", b)
	f.r.Packages[0].Manifest.Files["skills/review/SKILL.md"] = hash(b)
	f.r.Tools[1].Tool.EnvRefs = []string{"TOKEN"}
	p := previewMust(t, f, target)
	if len(p.Unmapped) != 2 {
		t.Fatal(p.Unmapped)
	}
	for _, op := range p.Files {
		if strings.HasSuffix(op.Path, "SKILL.md") {
			t.Fatal("unknown permission metadata activated")
		}
	}
	r, e := Apply(context.Background(), f, p)
	if e != nil || r.Coverage != "partial_for_declared_resolution" {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestExtraNativeSkillAssetRefusesVerifiedLaunchReceipt(t *testing.T) {
	f, target := fixture(t, "opencode")
	r := applyMust(t, f, target)
	put(t, target.Workspace, ".opencode/skills/review/unowned.md", []byte("foreign"))
	s, e := Status(context.Background(), target)
	if !errors.Is(e, ErrDrift) || s.Verified {
		t.Fatalf("%+v %v", s, e)
	}
	if _, e = Remove(context.Background(), target, r.Digest); !errors.Is(e, ErrDrift) {
		t.Fatal(e)
	}
}
func TestMalformedAndCopiedReceiptNeverVerified(t *testing.T) {
	f, target := fixture(t, "codex")
	r := applyMust(t, f, target)
	path := appPath(target, "receipt.json")
	bad := append(canonical(r), []byte(" {}")...)
	put(t, target.Workspace, path, bad)
	if s, e := Status(context.Background(), target); !errors.Is(e, ErrUnsafe) || s.Verified {
		t.Fatalf("%+v %v", s, e)
	}
	r.Target.Workspace = privateDir(t)
	r.Digest = sealReceipt(r)
	put(t, r.Target.Workspace, appPath(r.Target, "receipt.json"), canonical(r))
	if s, e := Status(context.Background(), r.Target); !errors.Is(e, ErrUnsafe) || s.Verified {
		t.Fatalf("%+v %v", s, e)
	}
}
func TestConcurrentApplyOneDurableEffectThenReviewedRetry(t *testing.T) {
	f, target := fixture(t, "claude")
	p := previewMust(t, f, target)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := Apply(context.Background(), f, p); results <- e }()
	}
	wg.Wait()
	close(results)
	pass := 0
	for e := range results {
		if e == nil {
			pass++
		} else if !errors.Is(e, ErrStale) && !errors.Is(e, ErrPending) && !errors.Is(e, ErrUnsafe) {
			t.Fatal(e)
		}
	}
	if pass != 1 {
		t.Fatalf("successful concurrent effects %d", pass)
	}
	r := applyMust(t, f, target)
	if r.Revision != 1 {
		t.Fatal("repeat effect")
	}
}

func TestProfileIdentityIsNotCollapsedBySameSelectionDigest(t *testing.T) {
	f, target := fixture(t, "codex")
	first := applyMust(t, f, target)
	f.r.ProfileID = "other.profile"
	next := applyMust(t, f, target)
	if next.ProfileID != f.r.ProfileID || next.Revision != first.Revision+1 || next.Digest == first.Digest {
		t.Fatal("different profile reused old authority")
	}
}
func TestRemoveAfterAllComponentsDisabled(t *testing.T) {
	f, target := fixture(t, "gemini")
	_ = applyMust(t, f, target)
	f.r.Skills = nil
	f.r.Rules = nil
	f.r.Tools = nil
	revise(f)
	off := applyMust(t, f, target)
	if len(off.Files) > 0 {
		t.Fatal("disabled components still present")
	}
	removed, e := Remove(context.Background(), target, off.Digest)
	if e != nil || removed.Applied {
		t.Fatalf("%+v %v", removed, e)
	}
	if _, e = os.Stat(filepath.Join(target.Workspace, ".gemini")); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("owned empty directory remains: %v", e)
	}
}
func TestMissingOrReplacedRetainedLockNeverRecreated(t *testing.T) {
	for _, kind := range []string{"missing", "replacement"} {
		t.Run(kind, func(t *testing.T) {
			f, target := fixture(t, "codex")
			r := applyMust(t, f, target)
			path := filepath.Join(target.Workspace, ledgerDir, "lock")
			b, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.Rename(path, path+".held"); e != nil {
				t.Fatal(e)
			}
			if kind == "replacement" {
				if e = os.WriteFile(path, b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			s, e := Status(context.Background(), target)
			if !errors.Is(e, ErrUnsafe) || s.Verified {
				t.Fatalf("%+v %v", s, e)
			}
			if _, e = Remove(context.Background(), target, r.Digest); !errors.Is(e, ErrUnsafe) {
				t.Fatal(e)
			}
			if _, e = Preview(context.Background(), f, f.r, target); !errors.Is(e, ErrUnsafe) {
				t.Fatal(e)
			}
			if kind == "missing" {
				if _, e = os.Stat(path); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("missing lock recreated")
				}
			} else {
				after, _ := os.ReadFile(path)
				if !bytes.Equal(after, b) {
					t.Fatal("replacement lock altered")
				}
			}
		})
	}
}
func TestRetainedLockRetargetingAtPublicationBoundariesIsFenced(t *testing.T) {
	for _, stage := range []string{"intent", "receipt"} {
		t.Run(stage, func(t *testing.T) {
			f, target := fixture(t, "codex")
			p := previewMust(t, f, target)
			path := filepath.Join(target.Workspace, ledgerDir, "lock")
			_, e := apply(context.Background(), f, p, func(at string) error {
				if at == stage {
					return os.Rename(path, path+".held")
				}
				return nil
			})
			if !errors.Is(e, ErrUnsafe) {
				t.Fatal(e)
			}
			if stage == "intent" {
				for _, op := range p.Files {
					if _, e = os.Stat(filepath.Join(target.Workspace, op.Path)); !errors.Is(e, os.ErrNotExist) {
						t.Fatal("effect after lock loss")
					}
				}
			}
			if _, e = Recover(context.Background(), target); !errors.Is(e, ErrUnsafe) {
				t.Fatal("lost lock authorized recovery", e)
			}
			if _, e = os.Stat(path); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("recovery recreated missing lock")
			}
			if e = os.Rename(path+".held", path); e != nil {
				t.Fatal(e)
			}
			s, e := Recover(context.Background(), target)
			if e != nil || !s.Verified {
				t.Fatalf("%+v %v", s, e)
			}
		})
	}
}
func TestDuplicateJSONStateFieldsAreRefused(t *testing.T) {
	f, target := fixture(t, "codex")
	r := applyMust(t, f, target)
	b := canonical(r)
	b = append([]byte(`{"schema":1,`), b[1:]...)
	put(t, target.Workspace, appPath(target, "receipt.json"), b)
	if s, e := Status(context.Background(), target); !errors.Is(e, ErrUnsafe) || s.Verified {
		t.Fatalf("%+v %v", s, e)
	}
}

func TestResealedReceiptCannotGrantArbitraryNativeArguments(t *testing.T) {
	for _, h := range []string{"codex", "claude", "gemini", "grok", "opencode"} {
		t.Run(h, func(t *testing.T) {
			f, target := fixture(t, h)
			r := applyMust(t, f, target)
			r.LaunchArgs = []string{"--dangerous", "arbitrary"}
			r.Digest = sealReceipt(r)
			put(t, target.Workspace, appPath(target, "receipt.json"), canonical(r))
			if s, e := Status(context.Background(), target); !errors.Is(e, ErrUnsafe) || s.Verified {
				t.Fatalf("%+v %v", s, e)
			}
		})
	}
}
func TestReceiptDesiredFilesAndArgumentsCanBeCompared(t *testing.T) {
	f, target := fixture(t, "grok")
	r := applyMust(t, f, target)
	p := previewMust(t, f, target)
	if !equalFiles(&r, p) {
		t.Fatal("current application did not match")
	}
	r.LaunchArgs[1] = "forged"
	if equalFiles(&r, p) {
		t.Fatal("forged rules matched registry")
	}
}

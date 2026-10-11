//go:build library_registry

package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/seanheiney/keelmux/internal/library"
)

// Enabled by the captain after integrating the independently owned Registry
// implementation, or with a read-only Go overlay of that exact source commit.
func TestActualRegistryResolutionApplicationRestartAndToggle(t *testing.T) {
	ctx := context.Background()
	fixture, target := fixture(t, "gemini")
	state := filepath.Join(privateDir(t), "registry-state")
	reg, e := library.Open(state)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(state); !os.IsNotExist(e) {
		t.Fatal("Open created registry state")
	}
	put(t, fixture.roots["pack"], library.ManifestFilename, canonical(fixture.r.Packages[0].Manifest))
	p, e := reg.Import(ctx, fixture.roots["pack"])
	if e != nil {
		t.Fatal(e)
	}
	if _, e = reg.SelectPackage(ctx, "default", p.Manifest.ID, p.Digest, true); e != nil {
		t.Fatal(e)
	}
	r, e := reg.Resolve(ctx, "default")
	if e != nil {
		t.Fatal(e)
	}
	plan, e := Preview(ctx, reg, r, target)
	if e != nil {
		t.Fatal(e)
	}
	applied, e := Apply(ctx, reg, plan)
	if e != nil {
		t.Fatal(e)
	}
	if applied.ProfileID != r.ProfileID || applied.ResolutionDigest != r.Digest || applied.ProfileRevision != r.ProfileRevision {
		t.Fatal("resolution attribution lost")
	}
	reg, e = library.Open(state)
	if e != nil {
		t.Fatal(e)
	}
	s, e := Status(ctx, target)
	if e != nil || !s.Verified || s.Receipt.Digest != applied.Digest {
		t.Fatalf("%+v %v", s, e)
	}
	if _, e = reg.SelectComponent(ctx, "default", "pack", "skill/review", false); e != nil {
		t.Fatal(e)
	}
	r, e = reg.Resolve(ctx, "default")
	if e != nil {
		t.Fatal(e)
	}
	plan, e = Preview(ctx, reg, r, target)
	if e != nil {
		t.Fatal(e)
	}
	off, e := Apply(ctx, reg, plan)
	if e != nil {
		t.Fatal(e)
	}
	if off.Revision != applied.Revision+1 || off.ResolutionDigest == applied.ResolutionDigest {
		t.Fatal("reviewed registry toggle not applied")
	}
	if _, e = os.Stat(filepath.Join(target.Workspace, ".gemini/skills/review/SKILL.md")); !os.IsNotExist(e) {
		t.Fatal("disabled skill remained")
	}
}

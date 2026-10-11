package adoption

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/library"
	"github.com/seanheiney/keelmux/internal/library/native"
)

const testHost domain.ID = "host_0123456789abcdef0123456789abcdef"

func privateDirectory(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func writeFixture(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		value := fmt.Sprintf("%v:%o:%d:%d", info.Mode().Type(), info.Mode().Perm(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + digest(raw)
		}
		out[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func localRequest(state, workspace string) Request {
	s := domain.NewSession(testHost, "local fixture", workspace, "opencode", "1.18.21")
	s.Execution.State = "running"
	s.Library = &domain.LibrarySnapshot{ProfileID: "default", ProfileRevision: 1, ResolutionDigest: strings.Repeat("a", 64), ApplicationID: "acceptance", ApplicationDigest: strings.Repeat("b", 64), Source: "client-declaration", Loaded: Unknown}
	return Request{StateDir: state, LocalHost: testHost, Session: s}
}

type actualFixture struct {
	request Request
	reg     library.Registry
	target  native.Target
	pack    library.Package
	receipt native.Receipt
}

func actualApplication(t *testing.T, harness string) actualFixture {
	t.Helper()
	ctx := context.Background()
	state := filepath.Join(privateDirectory(t), "state")
	workspace, bundle := privateDirectory(t), privateDirectory(t)
	files := map[string][]byte{
		"skills/review/SKILL.md": []byte("---\nname: review\ndescription: Harmless fixture review\n---\nSKILL-MARKER: no actions.\n"),
		"rules/base.md":          []byte("RULE-MARKER: preserve foreign content.\n"),
	}
	manifest := library.Manifest{Schema: 1, ID: "pack", Version: "1.0.0", Files: map[string]string{}, Skills: []library.Asset{{ID: "review", Path: "skills/review"}}, Rules: []library.Asset{{ID: "base", Path: "rules/base.md"}}, Tools: []library.Tool{{ID: "probe", Transport: "stdio", Command: []string{"fixture-mcp-never-run"}}}}
	for path, raw := range files {
		manifest.Files[path] = digest(raw)
		writeFixture(t, filepath.Join(bundle, path), raw)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(bundle, library.ManifestFilename), raw)
	reg, err := library.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := reg.Import(ctx, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reg.SelectPackage(ctx, "default", "pack", pack.Digest, true); err != nil {
		t.Fatal(err)
	}
	if _, err = reg.CreateProfile(ctx, "review", "Review"); err != nil {
		t.Fatal(err)
	}
	if _, err = reg.SelectPackage(ctx, "review", "pack", pack.Digest, true); err != nil {
		t.Fatal(err)
	}
	resolution, err := reg.Resolve(ctx, "review")
	if err != nil {
		t.Fatal(err)
	}
	target := native.Target{Harness: harness, Workspace: workspace, ApplicationID: "acceptance"}
	plan, err := native.Preview(ctx, reg, resolution, target)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := native.Apply(ctx, reg, plan)
	if err != nil {
		t.Fatal(err)
	}
	req := localRequest(state, workspace)
	req.Session.Execution.Provider = harness
	req.Session.Library = &domain.LibrarySnapshot{ProfileID: receipt.ProfileID, ProfileRevision: receipt.ProfileRevision, ResolutionDigest: receipt.ResolutionDigest, ApplicationID: target.ApplicationID, ApplicationDigest: receipt.Digest, Source: "client-declaration", Loaded: Unknown}
	return actualFixture{req, reg, target, pack, receipt}
}

func TestInspectAbsentStateCreatesNothing(t *testing.T) {
	parent, workspace := privateDirectory(t), privateDirectory(t)
	state := filepath.Join(parent, "absent")
	before := treeSnapshot(t, workspace)
	out, err := Inspect(context.Background(), localRequest(state, workspace))
	if err != nil || out.State != Pending || out.Reason != "application_absent" {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatalf("state created: %v", err)
	}
	if !reflect.DeepEqual(before, treeSnapshot(t, workspace)) {
		t.Fatal("workspace changed")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("inspection created an identity/lock", err)
	}
}

func TestPreflightRefusesBeforeAnyStateOrWorkspaceRead(t *testing.T) {
	base := localRequest("/never-open-state", "/never-open-workspace")
	cases := []struct {
		name, reason string
		change       func(*Request)
	}{
		{"unproven", "local_host_unproven", func(r *Request) { r.LocalHost = "" }},
		{"remote", "remote_session", func(r *Request) { r.Session.HostID = "host_ffffffffffffffffffffffffffffffff" }},
		{"observed", "unmanaged_session", func(r *Request) { r.Session.Mode = domain.Observed }},
		{"linked", "unmanaged_session", func(r *Request) { r.Session.Linked = true }},
		{"observer only", "unmanaged_session", func(r *Request) { r.Session.Capabilities.ObserverOnly = true }},
		{"malformed session ID", "session_invalid", func(r *Request) { r.Session.ID = "session_fake" }},
		{"malformed execution ID", "session_invalid", func(r *Request) { r.Session.Execution.ID = "execution_fake" }},
		{"missing snapshot", "launch_missing", func(r *Request) { r.Session.Library = nil }},
		{"invalid snapshot", "launch_invalid", func(r *Request) { r.Session.Library.Loaded = "loaded" }},
		{"provider", "provider_unsupported", func(r *Request) { r.Session.Execution.Provider = "foreign" }},
		{"workspace", "workspace_invalid", func(r *Request) { r.Session.Workspace = "relative" }},
		{"missing application", "application_missing", func(r *Request) { r.Session.Library.ApplicationID = ""; r.Session.Library.ApplicationDigest = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := base
			copy := *base.Session.Library
			req.Session.Library = &copy
			c.change(&req)
			read := readers{open: func(string) (library.Registry, error) { t.Fatal("preflight performed state read"); return nil, nil }}
			out, err := inspect(context.Background(), req, read)
			if err != nil || out.State != Unavailable || out.Reason != c.reason || out.Saved != nil || out.Applied != nil {
				t.Fatalf("%+v %v", out, err)
			}
			if out.NativeLoaded != Unknown || out.Permissions != Unknown {
				t.Fatal("native authority invented")
			}
		})
	}
}

func TestActualAlignedInspectionIsReadOnlyAndDoesNotExposeContent(t *testing.T) {
	f := actualApplication(t, "opencode")
	stateBefore, workspaceBefore := treeSnapshot(t, f.request.StateDir), treeSnapshot(t, f.target.Workspace)
	out, err := Inspect(context.Background(), f.request)
	if err != nil || out.State != Aligned || out.SavedToLaunch != Match || out.SavedToApplied != Match || out.AppliedToLaunch != Match {
		t.Fatalf("%+v %v", out, err)
	}
	if out.NativeLoaded != Unknown || out.Permissions != Unknown || out.Launch == f.request.Session.Library {
		t.Fatal("loading/permissions or aliased launch")
	}
	if !reflect.DeepEqual(stateBefore, treeSnapshot(t, f.request.StateDir)) || !reflect.DeepEqual(workspaceBefore, treeSnapshot(t, f.target.Workspace)) {
		t.Fatal("inspection wrote state")
	}
	raw, _ := json.Marshal(out)
	for _, forbidden := range []string{"RULE-MARKER", "SKILL-MARKER", "fixture-mcp-never-run", "launch_args", "env_refs"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("generated content exposed: %s", forbidden)
		}
	}
}

func TestSavedThenAppliedTogglePreservesDefaultAndOriginalLaunch(t *testing.T) {
	f := actualApplication(t, "opencode")
	ctx := context.Background()
	defaultBefore, err := f.reg.Resolve(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	launch := *f.request.Session.Library
	if _, err = f.reg.SelectComponent(ctx, "review", "pack", "tool/probe", false); err != nil {
		t.Fatal(err)
	}
	out, err := Inspect(ctx, f.request)
	if err != nil || out.State != Pending || out.SavedToLaunch != Different || out.SavedToApplied != Different || out.AppliedToLaunch != Match || out.NextAction != "preview_apply" {
		t.Fatalf("%+v %v", out, err)
	}
	defaultAfter, err := f.reg.Resolve(ctx, "default")
	if err != nil || !sameResolution(defaultBefore, defaultAfter) || len(defaultAfter.Tools) != 1 || defaultAfter.Packages[0].Digest != f.pack.Digest {
		t.Fatal("default or pin changed", err)
	}
	review, err := f.reg.Resolve(ctx, "review")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := native.Preview(ctx, f.reg, review, f.target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = native.Apply(ctx, f.reg, plan); err != nil {
		t.Fatal(err)
	}
	out, err = Inspect(ctx, f.request)
	if err != nil || out.State != Pending || out.SavedToApplied != Match || out.AppliedToLaunch != Different || out.NextAction != "new_session_required" {
		t.Fatalf("%+v %v", out, err)
	}
	if *f.request.Session.Library != launch || *out.Launch != launch {
		t.Fatal("historical launch snapshot changed")
	}
}

func TestDifferentProfileWithSamePinsDoesNotMatchApplication(t *testing.T) {
	f := actualApplication(t, "opencode")
	current, err := f.reg.Resolve(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	f.request.Session.Library.ProfileID = current.ProfileID
	f.request.Session.Library.ProfileRevision = current.ProfileRevision
	f.request.Session.Library.ResolutionDigest = current.Digest
	out, err := Inspect(context.Background(), f.request)
	if err != nil || out.State == Aligned || out.SavedToApplied != Different || out.AppliedToLaunch != Different {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestRemovedApplicationDoesNotBecomeAligned(t *testing.T) {
	f := actualApplication(t, "opencode")
	if _, err := native.Remove(context.Background(), f.target, f.receipt.Digest); err != nil {
		t.Fatal(err)
	}
	out, err := Inspect(context.Background(), f.request)
	if err != nil || out.State != Pending || out.Reason != "application_not_applied" || out.SavedToApplied != Unknown || out.AppliedToLaunch != Unknown {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestStrictNativeRefusals(t *testing.T) {
	cases := []struct {
		name, harness, reason string
		change                func(*testing.T, actualFixture)
	}{
		{"drift", "opencode", "application_drift", func(t *testing.T, f actualFixture) {
			writeFixture(t, filepath.Join(f.target.Workspace, "opencode.json"), []byte("foreign changed content"))
		}},
		{"pending", "opencode", "application_pending", func(t *testing.T, f actualFixture) {
			writeFixture(t, filepath.Join(f.target.Workspace, ".keelmux-native/acceptance/intent.json"), []byte("{}"))
		}},
		{"partial Grok MCP", "grok", "application_partial", func(*testing.T, actualFixture) {}},
		{"precedence", "opencode", "mapping_collision", func(t *testing.T, f actualFixture) {
			writeFixture(t, filepath.Join(f.target.Workspace, ".claude/skills/review/SKILL.md"), []byte("foreign skill"))
		}},
		{"unsafe workspace", "opencode", "application_unsafe", func(t *testing.T, f actualFixture) {
			if err := os.Chmod(f.target.Workspace, 0777); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := actualApplication(t, c.harness)
			c.change(t, f)
			before := treeSnapshot(t, f.target.Workspace)
			out, err := Inspect(context.Background(), f.request)
			if err != nil || out.State != Blocked || out.Reason != c.reason || out.SavedToApplied != Unknown || out.NativeLoaded != Unknown || out.Permissions != Unknown {
				t.Fatalf("%+v %v", out, err)
			}
			if !reflect.DeepEqual(before, treeSnapshot(t, f.target.Workspace)) {
				t.Fatal("refusal changed native files")
			}
		})
	}
}

type resolutionSequence struct {
	library.Registry
	values []library.Resolution
	calls  int
}

func (r *resolutionSequence) Resolve(_ context.Context, _ string) (library.Resolution, error) {
	i := r.calls
	r.calls++
	if i >= len(r.values) {
		i = len(r.values) - 1
	}
	return r.values[i], nil
}
func fakeObservation() (Request, *resolutionSequence, native.Receipt, native.Plan) {
	req := localRequest("/owned-state", "/owned-workspace")
	r := library.Resolution{ProfileID: "default", ProfileRevision: 1, Digest: req.Session.Library.ResolutionDigest, Loaded: Unknown}
	reg := &resolutionSequence{values: []library.Resolution{r, r}}
	target := native.Target{Harness: "opencode", Workspace: req.Session.Workspace, ApplicationID: "acceptance"}
	raw := []byte("fixture")
	receipt := native.Receipt{Target: target, ProfileID: "default", ProfileRevision: 1, ResolutionDigest: r.Digest, Digest: req.Session.Library.ApplicationDigest, Applied: true, Coverage: "complete_for_declared_resolution", Files: []native.OwnedFile{{Path: "opencode.json", SHA256: digest(raw), Bytes: len(raw)}}}
	plan := native.Plan{Target: target, ProfileID: r.ProfileID, ProfileRevision: r.ProfileRevision, ResolutionDigest: r.Digest, ExpectedReceiptDigest: receipt.Digest, Digest: "irrelevant-plan-digest", Files: []native.FileOperation{{Path: "opencode.json", After: native.FileIdentity{Present: true, SHA256: digest(raw)}, Content: raw}}}
	return req, reg, receipt, plan
}

func TestObservationFencesAndDesiredFileEquivalence(t *testing.T) {
	cases := []struct {
		name, state, reason string
		change              func(*resolutionSequence, *native.Receipt, *native.Plan)
	}{
		{"profile repin", Blocked, "observation_changed", func(r *resolutionSequence, _ *native.Receipt, _ *native.Plan) {
			r.values[1].ProfileRevision++
			r.values[1].Digest = strings.Repeat("c", 64)
		}},
		{"receipt replaced", Blocked, "observation_changed", func(_ *resolutionSequence, r *native.Receipt, _ *native.Plan) { r.Digest = strings.Repeat("c", 64) }},
		{"desired bytes differ with same PlanDigest", Pending, "application_changed", func(_ *resolutionSequence, _ *native.Receipt, p *native.Plan) {
			p.Files[0].After.SHA256 = strings.Repeat("d", 64)
		}},
		{"desired argv differs with same PlanDigest", Pending, "application_changed", func(_ *resolutionSequence, _ *native.Receipt, p *native.Plan) {
			p.LaunchArgs = []string{"--rules", "changed literal"}
		}},
		{"unrelated catalog change", Aligned, "aligned", func(r *resolutionSequence, _ *native.Receipt, _ *native.Plan) { r.values[1].CatalogRevision++ }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, reg, first, plan := fakeObservation()
			last := first
			c.change(reg, &last, &plan)
			statusCalls := 0
			read := readers{open: func(string) (library.Registry, error) { return reg, nil }, status: func(context.Context, native.Target) (native.ApplicationStatus, error) {
				statusCalls++
				r := first
				if statusCalls > 1 {
					r = last
				}
				return native.ApplicationStatus{Present: true, Verified: true, Receipt: &r}, nil
			}, preview: func(context.Context, library.Registry, library.Resolution, native.Target) (native.Plan, error) {
				return plan, nil
			}}
			out, err := inspect(context.Background(), req, read)
			if err != nil || out.State != c.state || out.Reason != c.reason {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
}

func TestContextCancellationPropagatesWithoutRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := inspect(ctx, localRequest("/not-read", "/not-read"), readers{open: func(string) (library.Registry, error) { t.Fatal("canceled read"); return nil, nil }})
	if !errors.Is(err, context.Canceled) || out.State == Aligned {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestMatchesSessionFencesIdentityAndCopiesLaunch(t *testing.T) {
	s := localRequest("/owned-state", "/owned-workspace").Session
	out := reportFor(s)
	if !MatchesSession(out, s) || out.Launch == s.Library {
		t.Fatal("initial tuple or snapshot copy")
	}
	changes := []func(*domain.Session){
		func(s *domain.Session) { s.HostID = "changed" }, func(s *domain.Session) { s.ID = "changed" },
		func(s *domain.Session) { s.Execution.ID = "changed" }, func(s *domain.Session) { s.Execution.Generation++ },
		func(s *domain.Session) { s.Execution.Provider = "grok" }, func(s *domain.Session) { s.Workspace += "/other" },
		func(s *domain.Session) { s.Execution.State = "stopped" }, func(s *domain.Session) { s.Library.ProfileID = "review" },
		func(s *domain.Session) { s.Library.ProfileRevision++ }, func(s *domain.Session) { s.Library.ApplicationDigest = strings.Repeat("c", 64) },
		func(s *domain.Session) { s.Library = nil },
	}
	for i, change := range changes {
		current := s
		copy := *s.Library
		current.Library = &copy
		change(&current)
		if MatchesSession(out, current) {
			t.Fatalf("accepted retarget %d", i)
		}
	}
	s.Mode, s.Linked = domain.Observed, true
	if !MatchesSession(out, s) {
		t.Fatal("pure tuple check prevented unchanged unavailable report; caller fences eligibility")
	}
	s.Library.ProfileID = "review"
	if out.Launch.ProfileID != "default" {
		t.Fatal("launch pointer aliased caller")
	}
}

func TestCancellationAfterSuccessfulReadStillPropagates(t *testing.T) {
	for _, stage := range []string{"open", "first_status", "preview", "last_status"} {
		t.Run(stage, func(t *testing.T) {
			req, reg, receipt, plan := fakeObservation()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			statusCalls := 0
			read := readers{open: func(string) (library.Registry, error) {
				if stage == "open" {
					cancel()
				}
				return reg, nil
			}, status: func(context.Context, native.Target) (native.ApplicationStatus, error) {
				statusCalls++
				if stage == "first_status" && statusCalls == 1 || stage == "last_status" && statusCalls == 2 {
					cancel()
				}
				return native.ApplicationStatus{Present: true, Verified: true, Receipt: &receipt}, nil
			}, preview: func(context.Context, library.Registry, library.Resolution, native.Target) (native.Plan, error) {
				if stage == "preview" {
					cancel()
				}
				return plan, nil
			}}
			out, err := inspect(ctx, req, read)
			if !errors.Is(err, context.Canceled) || out.State == Aligned {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
}

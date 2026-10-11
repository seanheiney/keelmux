package native

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/seanheiney/keelmux/internal/library"
)

var nativeContracts = map[string]string{
	"codex":    "codex-cli 0.159.0; documented project .agents/skills, AGENTS.md and .codex/config.toml",
	"claude":   "Claude Code 2.1.296; documented .claude/skills, .claude/rules and supplemental --mcp-config",
	"gemini":   "Gemini CLI 0.52.0; documented .gemini/skills, GEMINI.md and project .gemini/settings.json",
	"grok":     "grok 1.0.50 (c58f321264ba) [stable]; installed --rules literal instruction text only; skill index compatibility",
	"opencode": "OpenCode 1.18.21; documented .opencode/skills and project opencode.json instructions/mcp",
}

func currentReceipt(ctx context.Context, t *tree, target Target) (*Receipt, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	b, present, e := t.read(appPath(target, "receipt.json"), maxStateBytes)
	if e != nil {
		return nil, e
	}
	if !present {
		return nil, nil
	}
	var r Receipt
	if e = decode(b, &r); e != nil {
		return nil, e
	}
	if e = validateReceipt(t, r, target); e != nil {
		return nil, e
	}
	return &r, nil
}
func validateReceipt(t *tree, r Receipt, target Target) error {
	dev, ino, e := readLock(t)
	if e != nil {
		return e
	}
	if r.LockDevice != dev || r.LockInode != ino {
		return ErrUnsafe
	}
	if r.Schema != 1 || r.Target != target || r.WorkspaceDevice != t.dev || r.WorkspaceInode != t.ino || !digestPattern.MatchString(r.Digest) || sealReceipt(r) != r.Digest || r.Revision == 0 || !identifier.MatchString(r.ProfileID) || !digestPattern.MatchString(r.ResolutionDigest) || !digestPattern.MatchString(r.PlanDigest) || r.Loaded != "unknown" || r.Reload != "pending_or_unknown" || len(r.Files) > maxFiles {
		return ErrUnsafe
	}
	seen := map[string]bool{}
	total := 0
	for _, f := range r.Files {
		if !operationAllowed(target, f.Path) || seen[f.Path] || !digestPattern.MatchString(f.SHA256) || f.Bytes < 0 || f.Bytes > maxFileBytes {
			return ErrUnsafe
		}
		seen[f.Path] = true
		total += f.Bytes
	}
	if total > maxTotalBytes || len(r.CreatedDirectories) > maxFiles*8 {
		return ErrUnsafe
	}
	for _, d := range r.CreatedDirectories {
		if !directoryAllowed(target, d) {
			return ErrUnsafe
		}
	}
	if (!r.Applied && (len(r.Files) > 0 || len(r.LaunchArgs) > 0)) || (r.Coverage != "complete_for_declared_resolution" && r.Coverage != "partial_for_declared_resolution") || len(r.LaunchArgs) > 4 || len(canonical(r.LaunchArgs)) > 2*maxRulesArg+8192 {
		return ErrUnsafe
	}
	if e := receiptArgs(r); e != nil {
		return e
	}
	return nil
}
func receiptArgs(r Receipt) error {
	var file *OwnedFile
	path := ""
	switch r.Target.Harness {
	case "claude":
		path = appPath(r.Target, "generated/mcp.json")
	case "grok":
		path = appPath(r.Target, "generated/rules.md")
	}
	for _, f := range r.Files {
		if f.Path == path {
			copy := f
			file = &copy
			break
		}
	}
	if file == nil {
		if len(r.LaunchArgs) > 0 {
			return ErrUnsafe
		}
		return nil
	}
	if len(r.LaunchArgs) != 2 {
		return ErrUnsafe
	}
	if r.Target.Harness == "claude" {
		if r.LaunchArgs[0] != "--mcp-config" || r.LaunchArgs[1] != filepath.Join(r.Target.Workspace, path) {
			return ErrUnsafe
		}
	} else {
		if r.LaunchArgs[0] != "--rules" || !rulesArgumentWithinBudget(r.LaunchArgs[1]) || len(r.LaunchArgs[1]) != file.Bytes || hash([]byte(r.LaunchArgs[1])) != file.SHA256 {
			return ErrUnsafe
		}
	}
	return nil
}

func receiptDrift(ctx context.Context, t *tree, r *Receipt) ([]Collision, error) {
	var drift []Collision
	owned := map[string]string{}
	for _, f := range r.Files {
		owned[f.Path] = f.SHA256
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		got, e := t.owned(f.Path)
		if e != nil {
			return nil, e
		}
		if got != (FileIdentity{true, f.SHA256}) {
			drift = append(drift, Collision{f.Path, "owned applied bytes changed or disappeared"})
		}
	}
	for _, f := range r.Files {
		if strings.HasSuffix(f.Path, "/SKILL.md") {
			if e := checkInventory(t, filepath.Dir(f.Path), owned); e != nil {
				if errors.Is(e, ErrCollision) {
					drift = append(drift, Collision{filepath.Dir(f.Path), "native skill contains an unowned companion file"})
				} else {
					return nil, e
				}
			}
		}
	}
	return drift, nil
}
func Status(ctx context.Context, target Target) (ApplicationStatus, error) {
	var out ApplicationStatus
	if e := targetOK(target); e != nil {
		return out, e
	}
	t, e := openTree(target.Workspace)
	if e != nil {
		return out, e
	}
	defer t.close()
	return status(ctx, t, target)
}
func status(ctx context.Context, t *tree, target Target) (ApplicationStatus, error) {
	var out ApplicationStatus
	if _, _, e := t.read(filepath.Join(ledgerDir, "lock"), maxFileBytes); e != nil {
		return out, e
	}
	_, pending, e := t.read(appPath(target, "intent.json"), maxStateBytes)
	if e != nil {
		return out, e
	}
	if pending {
		out.Pending = true
		return out, ErrPending
	}
	r, e := currentReceipt(ctx, t, target)
	if e != nil {
		return out, e
	}
	if r == nil {
		return out, nil
	}
	out.Present = true
	out.Receipt = r
	out.Drift, e = receiptDrift(ctx, t, r)
	if e != nil {
		return out, e
	}
	if len(out.Drift) > 0 {
		return out, ErrDrift
	}
	// An optimistic read never publishes a receipt replaced during observation.
	r2, e := currentReceipt(ctx, t, target)
	if e != nil {
		return out, e
	}
	_, pending, e = t.read(appPath(target, "intent.json"), maxStateBytes)
	if e != nil {
		return out, e
	}
	if pending {
		out.Pending = true
		return out, ErrPending
	}
	if r2 == nil || r2.Digest != r.Digest {
		return out, ErrStale
	}
	if e = t.bound(); e != nil {
		return out, e
	}
	out.Verified = true
	return out, nil
}

// Preview is read-only, including on absent application state. Caller supplies
// workspace authorization. Global/account configuration is intentionally not
// inspected; native trust, precedence, approval and loaded state remain native.
func Preview(ctx context.Context, reg library.Registry, resolution library.Resolution, target Target) (Plan, error) {
	var p Plan
	if reg == nil {
		return p, ErrUnsafe
	}
	if e := targetOK(target); e != nil {
		return p, e
	}
	if !identifier.MatchString(resolution.ProfileID) || !digestPattern.MatchString(resolution.Digest) {
		return p, ErrUnsafe
	}
	actual, e := reg.Resolve(ctx, resolution.ProfileID)
	if e != nil {
		return p, e
	}
	if !bytes.Equal(canonical(actual), canonical(resolution)) {
		return p, ErrStale
	}
	t, e := openTree(target.Workspace)
	if e != nil {
		return p, e
	}
	defer t.close()
	return preview(ctx, reg, resolution, target, t)
}
func preview(ctx context.Context, reg library.Registry, r library.Resolution, target Target, t *tree) (Plan, error) {
	p := Plan{Schema: 1, Target: target, ProfileID: r.ProfileID, ProfileRevision: r.ProfileRevision, ResolutionDigest: r.Digest, WorkspaceDevice: t.dev, WorkspaceInode: t.ino, Guards: map[string]FileIdentity{}, Loaded: "unknown", Reload: "pending_or_unknown", Scope: "explicit selected workspace; account stores untouched", Precedence: "workspace mapping only; parent, user, managed and environment-selected native configuration can override or combine; not inspected", NativeContract: nativeContracts[target.Harness], Prerequisites: []string{"Launch from this exact workspace using verified receipt argv.", "Native trust, approvals, environment availability and version compatibility remain prerequisites.", "Applying files does not prove loading, tool health or invocation; restart/reload is native and remains pending or unknown."}}
	s, e := status(ctx, t, target)
	if e != nil {
		return p, e
	}
	old := s.Receipt
	owned := map[string]string{}
	if old != nil {
		p.ExpectedReceiptDigest = old.Digest
		for _, f := range old.Files {
			owned[f.Path] = f.SHA256
		}
	}
	m, e := buildMapping(ctx, reg, r, target)
	if e != nil {
		return p, e
	}
	p.LaunchArgs = m.args
	p.Unmapped = m.unmapped
	p.Collisions = m.collisions
	for _, root := range m.roots {
		exists, e := t.inspectEntry(root)
		if e != nil {
			return p, e
		}
		if exists {
			hasOwned := false
			for path := range owned {
				if strings.HasPrefix(path, root+"/") {
					hasOwned = true
					break
				}
			}
			if !hasOwned && old != nil {
				for _, dir := range old.CreatedDirectories {
					if dir == root {
						hasOwned = true
						break
					}
				}
			}
			if !hasOwned {
				p.Collisions = append(p.Collisions, Collision{root, "existing native skill directory is preserved; explicit manual opt-in is required"})
			} else if e = checkInventory(t, root, owned); e != nil {
				return p, e
			}
		}
	}
	for _, path := range m.guards {
		got, e := t.observe(path)
		if e != nil {
			return p, e
		}
		p.Guards[path] = got
		if got.Present {
			// All alias skill sources and alternative project entry files are
			// preserved. Supplemental Claude MCP permits unrelated server names.
			if target.Harness == "claude" && path == ".mcp.json" {
				if e = checkMCPNames(t, path, m.files); e != nil {
					return p, e
				}
			} else {
				p.Collisions = append(p.Collisions, Collision{path, "native precedence source already exists; explicit manual opt-in is required"})
			}
		}
	}
	paths := map[string]bool{}
	for path := range m.files {
		paths[path] = true
	}
	for path := range owned {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	for _, path := range ordered {
		if e = ctx.Err(); e != nil {
			return p, e
		}
		got, e := t.observe(path)
		if e != nil {
			return p, e
		}
		b, want := m.files[path]
		if got.Present && owned[path] == "" {
			p.Collisions = append(p.Collisions, Collision{path, "existing file is preserved; explicit manual opt-in is required"})
		}
		if owned[path] != "" && got != (FileIdentity{true, owned[path]}) {
			return p, ErrDrift
		}
		op := FileOperation{Path: path, Before: got, After: identity(b, want), Content: b}
		p.Files = append(p.Files, op)
	}
	sort.Slice(p.Collisions, func(i, j int) bool { return string(canonical(p.Collisions[i])) < string(canonical(p.Collisions[j])) })
	if len(p.Files) > maxFiles {
		return p, ErrUnsafe
	}
	p.Digest = sealPlan(p)
	return p, nil
}
func checkInventory(t *tree, root string, owned map[string]string) error {
	fd, name, e := t.parent(filepath.Join(root, "entry"), false)
	if e != nil {
		return e
	}
	_ = name
	file := os.NewFile(uintptr(fd), root)
	defer file.Close()
	entries, e := file.ReadDir(maxFiles + 1)
	if e != nil && e != io.EOF {
		return e
	}
	if len(entries) > maxFiles {
		return ErrUnsafe
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			if e = checkInventory(t, path, owned); e != nil {
				return e
			}
		} else {
			if owned[path] == "" {
				return fmt.Errorf("%w: unowned file inside skill %s", ErrCollision, path)
			}
			if _, e = t.owned(path); e != nil {
				return e
			}
		}
	}
	return nil
}
func checkMCPNames(t *tree, path string, generated map[string][]byte) error {
	b, _, e := t.read(path, maxFileBytes)
	if e != nil {
		return e
	}
	var existing struct {
		MCP map[string]any `json:"mcpServers"`
	}
	if e = decode(b, &existing); e != nil {
		return fmt.Errorf("%w: existing project MCP names cannot be safely checked", ErrCollision)
	}
	for path, b := range generated {
		if !strings.HasSuffix(path, "/mcp.json") {
			continue
		}
		var desired struct {
			MCP map[string]any `json:"mcpServers"`
		}
		if e = decode(b, &desired); e != nil {
			return e
		}
		for name := range desired.MCP {
			if _, ok := existing.MCP[name]; ok {
				return fmt.Errorf("%w: existing MCP server %s", ErrCollision, name)
			}
		}
	}
	return nil
}

func missingParents(t *tree, paths []FileOperation) ([]string, error) {
	set := map[string]bool{}
	for _, op := range paths {
		if !op.After.Present {
			continue
		}
		p := filepath.Dir(op.Path)
		for p != "." {
			if !privatePath(p) {
				exists, e := t.inspectEntry(p)
				if e != nil {
					return nil, e
				}
				if !exists {
					set[p] = true
				}
			}
			p = filepath.Dir(p)
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

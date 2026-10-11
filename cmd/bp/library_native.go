package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/library"
	"github.com/seanheiney/keelmux/internal/library/native"
)

func nativeLibraryAction(s string) bool {
	switch s {
	case "preview", "apply", "status", "clear", "recover":
		return true
	}
	return false
}

func manageNativeLibrary(ctx context.Context, state string, args []string, diagnostics, out io.Writer) error {
	fs := flag.NewFlagSet("keelmux plugins "+args[0], flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	profile := fs.String("profile", "default", "exact profile ID")
	harness := fs.String("harness", "", "codex, claude, gemini, grok or opencode")
	workspace := fs.String("workspace", "", "existing owned workspace")
	application := fs.String("application", "work", "workspace application ID")
	expect := ""
	if args[0] == "apply" {
		fs.StringVar(&expect, "expect-plan", "", "optional exact preview digest")
	} else if args[0] == "clear" {
		fs.StringVar(&expect, "expect-receipt", "", "optional exact applied receipt digest")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if len(fs.Args()) != 0 || *harness == "" || *workspace == "" {
		return errors.New("select --harness NAME and --workspace PATH; no positional arguments")
	}
	abs, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}
	target := native.Target{Harness: *harness, Workspace: abs, ApplicationID: *application}
	encode := func(v any) error {
		e := json.NewEncoder(out)
		e.SetIndent("", "  ")
		return e.Encode(v)
	}
	switch args[0] {
	case "status":
		s, e := native.Status(ctx, target)
		if writeErr := encode(s); writeErr != nil {
			return writeErr
		}
		return e
	case "recover":
		s, e := native.Recover(ctx, target)
		if e != nil {
			return e
		}
		return encode(s)
	case "clear":
		s, e := native.Status(ctx, target)
		if e != nil {
			return e
		}
		if !s.Present || !s.Verified || s.Receipt == nil {
			return errors.New("no verified owned application to clear")
		}
		if expect != "" && expect != s.Receipt.Digest {
			return native.ErrStale
		}
		r, e := native.Remove(ctx, target, s.Receipt.Digest)
		if e != nil {
			return e
		}
		return encode(r)
	}
	p, err := config.Resolve(state)
	if err != nil {
		return err
	}
	reg, err := library.Open(p.Dir)
	if err != nil {
		return err
	}
	r, err := reg.Resolve(ctx, *profile)
	if err != nil {
		return err
	}
	plan, err := native.Preview(ctx, reg, r, target)
	if err != nil {
		return err
	}
	if args[0] == "preview" {
		return encode(plan)
	}
	if expect != "" && expect != plan.Digest {
		return native.ErrStale
	}
	receipt, err := native.Apply(ctx, reg, plan)
	if err != nil {
		return err
	}
	return encode(receipt)
}

// A saved profile is desired state. Launch adopts only a currently verified,
// matching workspace application; it never applies files as a side effect.
func prepareLibraryLaunch(ctx context.Context, state, provider, workspace, profile, application string, explicit bool, args []string) ([]string, *domain.LibrarySnapshot, error) {
	reg, err := library.Open(state)
	if err != nil {
		return nil, nil, err
	}
	c, err := reg.Read(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !c.Present && !explicit {
		return args, nil, nil
	}
	r, err := reg.Resolve(ctx, profile)
	if err != nil {
		return nil, nil, err
	}
	if r.CatalogRevision != c.Revision {
		return nil, nil, errors.New("capability selections changed during preparation; retry")
	}
	var selection library.Profile
	for _, p := range c.Profiles {
		if p.ID == profile {
			selection = p
		}
	}
	snapshot := &domain.LibrarySnapshot{ProfileID: r.ProfileID, ProfileRevision: r.ProfileRevision, ResolutionDigest: r.Digest, Source: "client-declaration", Loaded: "unknown"}
	target := native.Target{Harness: provider, Workspace: workspace, ApplicationID: application}
	s, err := native.Status(ctx, target)
	if err != nil {
		return nil, nil, err
	}
	// Disabled selections still require an explicit application refresh to remove
	// earlier owned native files and bind the new desired resolution.
	needsApplication := len(selection.Packages) > 0 || s.Present && s.Receipt != nil && s.Receipt.Applied
	if !needsApplication {
		if !explicit && len(selection.Packages) == 0 {
			return args, nil, nil
		}
		return args, snapshot, nil
	}
	if !s.Present || !s.Verified || s.Receipt == nil || !s.Receipt.Applied || s.Receipt.ProfileID != r.ProfileID || s.Receipt.ProfileRevision != r.ProfileRevision || s.Receipt.ResolutionDigest != r.Digest {
		return nil, nil, fmt.Errorf("profile %s requires a current application: plugins apply --profile %s --harness %s --workspace PATH --application %s", profile, profile, provider, application)
	}
	if s.Receipt.Coverage != "complete_for_declared_resolution" {
		return nil, nil, errors.New("native application has unmapped capabilities; disable them or use a compatible harness before launch")
	}
	if err := profileArgsCompatible(provider, args); err != nil {
		return nil, nil, err
	}
	// Re-preview also observes precedence guards and the exact generated argv;
	// Status alone proves applied bytes, not the absence of newly added aliases.
	plan, err := native.Preview(ctx, reg, r, target)
	if err != nil {
		return nil, nil, err
	}
	if len(plan.Collisions) > 0 || len(plan.Unmapped) > 0 {
		return nil, nil, errors.New("native application has changed precedence or compatibility; inspect plugins preview")
	}
	if !receiptMatchesDesiredPlan(*s.Receipt, plan) {
		return nil, nil, errors.New("native application differs from the pinned desired files or launch arguments; inspect plugins preview")
	}
	snapshot.ApplicationID, snapshot.ApplicationDigest = application, s.Receipt.Digest
	return append(append([]string{}, s.Receipt.LaunchArgs...), args...), snapshot, nil
}

func receiptMatchesDesiredPlan(receipt native.Receipt, plan native.Plan) bool {
	if plan.ExpectedReceiptDigest != receipt.Digest {
		return false
	}
	left, _ := json.Marshal(receipt.LaunchArgs)
	right, _ := json.Marshal(plan.LaunchArgs)
	// Nil and empty argv both mean no native additions.
	if len(receipt.LaunchArgs) != len(plan.LaunchArgs) || len(receipt.LaunchArgs) > 0 && string(left) != string(right) {
		return false
	}
	files := map[string]native.OwnedFile{}
	for _, f := range receipt.Files {
		files[f.Path] = f
	}
	count := 0
	for _, op := range plan.Files {
		if !op.After.Present {
			return false
		}
		f, ok := files[op.Path]
		if !ok || f.SHA256 != op.After.SHA256 || f.Bytes != len(op.Content) {
			return false
		}
		count++
	}
	return count == len(receipt.Files)
}

func profileArgsCompatible(provider string, args []string) error {
	blocked := map[string]bool{}
	switch provider {
	case "codex":
		for _, k := range []string{"-C", "--cd", "-c", "--config", "-p", "--profile", "--enable", "--disable", "--remote", "--remote-auth-token-env"} {
			blocked[k] = true
		}
	case "claude":
		for _, k := range []string{"--mcp-config", "--strict-mcp-config", "--settings", "--setting-sources", "--plugin-dir", "--plugin-url", "--system-prompt", "--append-system-prompt"} {
			blocked[k] = true
		}
	case "grok":
		blocked["--rules"] = true
	}
	for _, a := range args {
		flag, _, _ := strings.Cut(a, "=")
		if blocked[flag] || provider == "codex" && strings.HasPrefix(a, "-C") || provider == "codex" && strings.HasPrefix(a, "-c") || provider == "codex" && strings.HasPrefix(a, "-p") {
			return fmt.Errorf("native %s conflicts with a pinned profile application", flag)
		}
	}
	return nil
}

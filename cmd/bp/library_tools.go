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
	"time"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/library"
	librarytools "github.com/seanheiney/keelmux/internal/library/tools"
)

const toolsHelp = `Usage: keelmux [--state DIR] tools COMMAND

tools list [--profile ID]                 List enabled pinned declarations
tools on [--profile ID] PACK/TOOL         Save one tool's enabled preference
tools off [--profile ID] PACK/TOOL        Save one tool's disabled preference
tools check [--profile ID] PACK/TOOL       Inspect availability; runs nothing
tools check --health --workspace PATH [--timeout 5s] PACK/TOOL
                                        Explicit stdio MCP initialize/tools-list

Flags precede PACK/TOOL. On/off preserve the selected pack pin and do not
enable a disabled pack. Select the pack with plugins on before editing tools.
Changes require explicit native apply and supported reload or a new session.
Check requires an enabled tool in the selected profile.
Health starts only that declared executable in a fresh private home, using
listed environment references. It never invokes a tool or makes a model turn.
HTTPS health is unqualified and performs no request. Running native sessions
and their tool permissions remain separate. Use plugins on/off --component
tool/ID PACK to change desired selections, then explicitly apply/reload.
`

func toolsCommand(ctx context.Context, state, entry string, args []string, diagnostics, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(out, toolsHelp)
		return err
	}
	if entry != "" {
		return errors.New("tool checks belong to the local execution home")
	}
	if args[0] != "list" && args[0] != "check" && args[0] != "on" && args[0] != "off" {
		return errors.New("unknown tools command; use tools --help")
	}
	fs := flag.NewFlagSet("keelmux tools "+args[0], flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	profile := fs.String("profile", "default", "exact profile ID")
	health, workspace, timeout := false, "", 5*time.Second
	if args[0] == "check" {
		fs.BoolVar(&health, "health", false, "explicitly start this stdio server")
		fs.StringVar(&workspace, "workspace", "", "existing working directory selected for explicit health")
		fs.DurationVar(&timeout, "timeout", 5*time.Second, "bounded health timeout (maximum 15s)")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "list" && len(fs.Args()) != 0 || args[0] != "list" && len(fs.Args()) != 1 {
		return errors.New("list takes no argument; on, off and check take PACK/TOOL")
	}
	if args[0] == "on" || args[0] == "off" {
		parts := strings.Split(fs.Args()[0], "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return errors.New("select one pinned PACK/TOOL")
		}
		// Reuse the revision-and-pin guarded selection boundary. This never
		// resolves an enabled-only list, which would make an off tool impossible
		// to turn back on, and never selects a different package revision.
		return manageLibrary(ctx, state, entry, "plugins", []string{
			args[0], "--profile", *profile, "--component", "tool/" + parts[1], parts[0],
		}, diagnostics, out)
	}
	if health && workspace == "" || !health && workspace != "" || timeout <= 0 || timeout > 15*time.Second {
		return errors.New("health requires --workspace PATH and a timeout above zero and at most 15s")
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
	encode := func(v any) error {
		e := json.NewEncoder(out)
		e.SetIndent("", "  ")
		return e.Encode(v)
	}
	if args[0] == "list" {
		return encode(r.Tools)
	}
	parts := strings.Split(fs.Args()[0], "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return errors.New("select one enabled PACK/TOOL")
	}
	for _, tool := range r.Tools {
		if tool.PackageID != parts[0] || tool.Tool.ID != parts[1] {
			continue
		}
		if _, err := reg.PackageRoot(ctx, tool.PackageID, tool.PackageDigest); err != nil {
			return err
		}
		if workspace != "" {
			workspace, err = filepath.Abs(workspace)
			if err != nil {
				return err
			}
		}
		receipt := librarytools.Check(ctx, tool, librarytools.Options{Health: health, WorkingDirectory: workspace, Timeout: timeout})
		if err := encode(receipt); err != nil {
			return err
		}
		if receipt.Availability != "available" || health && receipt.Health != "healthy" {
			return fmt.Errorf("tool check: %s", receipt.Code)
		}
		return nil
	}
	return errors.New("tool is not enabled in this profile; inspect tools list")
}

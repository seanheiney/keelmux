package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/library"
)

const libraryHelp = `Usage: keelmux [--state DIR] plugins COMMAND

plugins list                             Inspect immutable packs and profiles
plugins import DIRECTORY                 Import pinned local bytes; runs nothing
plugins import --builtin headroom         Import the bundled optional MCP pack
plugins on [--profile ID] [--digest SHA] PACK
plugins off [--profile ID] PACK
plugins on --component tool/ID PACK       Enable one component in a profile
plugins off --component skill/ID PACK     Disable one component in a profile
plugins remove --digest SHA PACK          Archive one exact unchanged revision
plugins preview --harness NAME --workspace PATH [--profile ID]
plugins apply --harness NAME --workspace PATH [--expect-plan SHA]
plugins status --harness NAME --workspace PATH
plugins clear --harness NAME --workspace PATH [--expect-receipt SHA]
plugins recover --harness NAME --workspace PATH

Native commands accept --profile ID and --application ID (default work).
Preview creates nothing. Apply writes only verified owned workspace files;
clear removes unchanged owned files. Neither starts a native harness or tool.

profiles list                            Inspect saved selections
profiles create --name NAME ID           Create an independent profile
profiles show [ID]                        Resolve exact pins; loaded stays unknown

Flags precede the final argument. The default profile is global to this local
library; choose another profile per session with run --profile ID. Importing
updates does not change selections. If multiple unselected revisions exist,
on requires --digest. Off preserves the pin and component preferences.
Selections affect the next explicit native apply/launch. Existing sessions
need native reload/restart; saving a toggle does not prove live revocation.
`

func libraryCommand(state, entry, command string, args []string) error {
	return manageLibrary(context.Background(), state, entry, command, args, os.Stderr, os.Stdout)
}

// The CLI boundary stays independent of a daemon and native provider login.
func manageLibrary(ctx context.Context, state, entry, command string, args []string, diagnostics, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(out, libraryHelp)
		return err
	}
	if entry != "" {
		return errors.New("capability selections belong to the local execution home")
	}
	sub := args[0]
	if command == "plugins" && nativeLibraryAction(sub) {
		return manageNativeLibrary(ctx, state, args, diagnostics, out)
	}
	fs := flag.NewFlagSet("keelmux "+command+" "+sub, flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	profile, digest, component, name := "default", "", "", ""
	builtin := false
	if command == "plugins" {
		switch sub {
		case "on", "off":
			fs.StringVar(&profile, "profile", "default", "exact profile ID")
			fs.StringVar(&component, "component", "", "skill/ID, rule/ID or tool/ID")
			if sub == "on" {
				fs.StringVar(&digest, "digest", "", "exact immutable revision")
			}
		case "remove":
			fs.StringVar(&digest, "digest", "", "exact immutable revision")
		case "import":
			fs.BoolVar(&builtin, "builtin", false, "import a bundled declarative pack")
		case "list":
		default:
			return errors.New("unknown plugins command; use plugins --help")
		}
	} else if command == "profiles" {
		switch sub {
		case "create":
			fs.StringVar(&name, "name", "", "profile display name")
		case "list", "show":
		default:
			return errors.New("unknown profiles command; use profiles --help")
		}
	} else {
		return errors.New("unknown library command")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	pos := fs.Args()
	if sub == "list" && len(pos) != 0 || sub == "show" && len(pos) > 1 || sub != "list" && sub != "show" && len(pos) != 1 {
		return errors.New("select one exact argument; flags precede it")
	}
	p, err := config.Resolve(state)
	if err != nil {
		return err
	}
	r, err := library.Open(p.Dir)
	if err != nil {
		return err
	}
	encode := func(v any) error {
		e := json.NewEncoder(out)
		e.SetIndent("", "  ")
		return e.Encode(v)
	}
	if command == "profiles" {
		switch sub {
		case "list":
			c, err := r.Read(ctx)
			if err != nil {
				return err
			}
			return encode(c.Profiles)
		case "create":
			v, err := r.CreateProfile(ctx, pos[0], name)
			if err != nil {
				return err
			}
			return encode(v)
		case "show":
			if len(pos) == 1 {
				profile = pos[0]
			}
			v, err := r.Resolve(ctx, profile)
			if err != nil {
				return err
			}
			return encode(v)
		}
	}
	switch sub {
	case "list":
		v, err := r.Read(ctx)
		if err != nil {
			return err
		}
		return encode(v)
	case "import":
		if builtin {
			v, err := importPreset(ctx, r, pos[0])
			if err != nil {
				return err
			}
			return encode(v)
		}
		source, err := filepath.Abs(pos[0])
		if err != nil {
			return err
		}
		v, err := r.Import(ctx, source)
		if err != nil {
			return err
		}
		return encode(v)
	case "remove":
		if digest == "" {
			return errors.New("remove requires --digest for one exact immutable revision")
		}
		if err := r.Remove(ctx, pos[0], digest); err != nil {
			return err
		}
		return encode(struct {
			Package string `json:"package"`
			Digest  string `json:"digest"`
			Result  string `json:"result"`
		}{pos[0], digest, "archived; matching selections disabled; native sessions unchanged"})
	case "on", "off":
		if component != "" {
			if digest != "" {
				return errors.New("component changes preserve the selected pin; omit --digest")
			}
			c, err := r.Read(ctx)
			if err != nil {
				return err
			}
			p, err := libraryProfile(c, profile)
			if err != nil {
				return err
			}
			sel, ok := p.Packages[pos[0]]
			if !ok {
				return errors.New("select a pack pin before editing its components")
			}
			guarded, ok := r.(library.GuardedSelections)
			if !ok {
				return library.ErrInvalid
			}
			v, err := guarded.SelectComponentAt(ctx, profile, pos[0], component, sub == "on", p.Revision, sel.Digest)
			if err != nil {
				return err
			}
			return encode(v)
		}
		var observedRevision uint64
		if digest == "" {
			c, err := r.Read(ctx)
			if err != nil {
				return err
			}
			digest, err = selectedLibraryPin(c, profile, pos[0])
			if err != nil {
				return err
			}
			p, err := libraryProfile(c, profile)
			if err != nil {
				return err
			}
			observedRevision = p.Revision
		}
		var v library.Profile
		if observedRevision != 0 {
			guarded, ok := r.(library.GuardedSelections)
			if !ok {
				return library.ErrInvalid
			}
			v, err = guarded.SelectPackageAt(ctx, profile, pos[0], digest, sub == "on", observedRevision)
		} else {
			v, err = r.SelectPackage(ctx, profile, pos[0], digest, sub == "on")
		}
		if err != nil {
			return err
		}
		return encode(v)
	}
	return errors.New("unsupported library action")
}

func libraryProfile(c library.Catalog, id string) (library.Profile, error) {
	for _, p := range c.Profiles {
		if p.ID == id {
			return p, nil
		}
	}
	return library.Profile{}, library.ErrNotFound
}

func selectedLibraryPin(c library.Catalog, profile, id string) (string, error) {
	for _, p := range c.Profiles {
		if p.ID == profile {
			if s, ok := p.Packages[id]; ok {
				return s.Digest, nil
			}
			break
		}
	}
	var pin string
	for _, p := range c.Packages {
		if p.Manifest.ID == id {
			if pin != "" {
				return "", errors.New("multiple immutable revisions; select --digest explicitly")
			}
			pin = p.Digest
		}
	}
	if pin == "" {
		return "", errors.New("pack not imported; use plugins import DIRECTORY")
	}
	return pin, nil
}

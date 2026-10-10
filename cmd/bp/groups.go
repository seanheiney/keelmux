package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/clientstate"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
)

const groupHelp = `Usage: keelmux [--state DIR] groups COMMAND

groups list                           List local company/product collections
groups create --name NAME [--parent GROUP]
groups rename --name NAME GROUP       Rename a collection, retaining its place
groups parent [--parent GROUP] GROUP  Move under a parent or make a flat group
groups add --kind KIND --id ID GROUP  Add a stable work/session/thread reference
groups remove --kind KIND --id ID GROUP
groups add --kind workspace --workspace PATH [--host HOST] GROUP
groups remove --kind workspace --workspace PATH [--host HOST] GROUP
groups select GROUP                   Remember this collection for the switcher
groups select all                     Return to All work
groups delete GROUP                   Remove navigation; work and drafts remain

Flags precede the final GROUP. Groups are optional local navigation, not
permissions or execution placement. Commands never start or contact a daemon.
Editing refuses while this client place is open in a switcher.
`

type groupList struct {
	Active   domain.ID           `json:"active_group,omitempty"`
	Previous domain.ID           `json:"previous_group,omitempty"`
	Groups   []clientstate.Group `json:"groups"`
}

func manageGroups(stateDir string, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" ||
		(len(args) > 1 && (args[len(args)-1] == "--help" || args[len(args)-1] == "-h")) {
		_, err := fmt.Fprint(out, groupHelp)
		return err
	}
	command := args[0]
	fs := flag.NewFlagSet("keelmux groups "+command, flag.ContinueOnError)
	fs.SetOutput(out)
	name, parent, kind, id, workspace, host := "", "", "", "", "", ""
	switch command {
	case "create", "rename":
		fs.StringVar(&name, "name", "", "collection name")
		if command == "create" {
			fs.StringVar(&parent, "parent", "", "optional parent collection")
		}
	case "parent":
		fs.StringVar(&parent, "parent", "", "parent collection; empty makes a flat group")
	case "add", "remove":
		fs.StringVar(&kind, "kind", "", "work, session, thread or workspace")
		fs.StringVar(&id, "id", "", "stable reference ID")
		fs.StringVar(&workspace, "workspace", "", "clean absolute workspace")
		fs.StringVar(&host, "host", "", "optional exact workspace host")
	case "list", "select", "delete":
	default:
		return errors.New("unknown groups command; use groups --help")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	pos := fs.Args()
	group := domain.ID("")
	if command == "create" || command == "list" {
		if len(pos) != 0 {
			return errors.New("groups " + command + " accepts no positional arguments")
		}
	} else {
		if len(pos) != 1 {
			return errors.New("select one exact group ID; flags precede it")
		}
		group = domain.ID(pos[0])
		if command == "select" && group == "all" {
			group = ""
		} else if !domain.ValidObjectID(group, "group") {
			return errors.New("group needs its exact stable group ID")
		}
	}
	if parent != "" && !domain.ValidObjectID(domain.ID(parent), "group") {
		return errors.New("parent needs its exact stable group ID")
	}
	if command == "create" || command == "rename" {
		if err := clientstate.ValidateGroupName(name); err != nil {
			return err
		}
	}
	ref := clientstate.Reference{Kind: kind, ID: domain.ID(id), Workspace: workspace, HostID: domain.ID(host)}
	if command == "add" || command == "remove" {
		switch kind {
		case "workspace":
			if id != "" || !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace || !utf8.ValidString(workspace) || strings.IndexFunc(workspace, unicode.IsControl) >= 0 || len(workspace) > 4096 ||
				(host != "" && !domain.ValidObjectID(domain.ID(host), "host")) {
				return errors.New("workspace membership needs a clean absolute path and optional exact host; no ID")
			}
		case "work", "session", "thread":
			if workspace != "" || host != "" || !domain.ValidObjectID(domain.ID(id), kind) {
				return errors.New("stable membership needs an exact work/session/thread ID; no workspace or host")
			}
		default:
			return errors.New("membership kind must be work, session, thread or workspace")
		}
	}
	paths, err := config.Resolve(stateDir)
	if err != nil {
		return err
	}
	if command == "list" {
		if info, err := os.Lstat(paths.Dir); err == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Geteuid()) {
				return errors.New("client state must be an owned private directory, without a symlink")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	} else {
		if err := paths.Ensure(); err != nil {
			return err
		}
		lock, err := clientstate.AcquireLock(paths.Place)
		if err != nil {
			return err
		}
		defer lock.Close()
	}
	state, err := clientstate.Load(paths.Place)
	if err != nil {
		return err
	}
	if state.Groups == nil {
		state.Groups = []clientstate.Group{}
	}
	result := any(groupList{Active: state.ActiveGroup, Previous: state.PreviousGroup, Groups: state.Groups})
	switch command {
	case "create":
		group, err = state.AddGroup(name, domain.ID(parent))
	case "rename":
		err = state.RenameGroup(group, name)
	case "parent":
		err = state.ReparentGroup(group, domain.ID(parent))
	case "add":
		err = state.AddReference(group, ref)
	case "remove":
		err = state.RemoveReference(group, ref)
	case "select":
		err = state.SwitchGroup(group)
	case "delete":
		err = state.DeleteGroup(group)
	}
	if err != nil {
		return err
	}
	if command != "list" {
		if err := clientstate.Save(paths.Place, state); err != nil {
			return err
		}
		if command == "delete" || command == "select" {
			result = groupList{Active: state.ActiveGroup, Previous: state.PreviousGroup, Groups: state.Groups}
		} else {
			result, _ = state.Group(group)
		}
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(result)
}

func groupsCommand(stateDir, entry string, args []string) error {
	if entry != "" {
		return errors.New("groups edit the local client place; select its --state without --entry")
	}
	return manageGroups(stateDir, args, os.Stdout)
}

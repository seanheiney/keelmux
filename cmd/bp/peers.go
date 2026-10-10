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
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/federation"
	"github.com/seanheiney/keelmux/internal/peers"
)

const peersHelp = `Usage: keelmux [--state DIR] peers COMMAND

peers enroll --config PRIVATE_JSON    Verify an existing SSH home and enroll it
peers list --principal OWNER [--refresh]
peers inspect --principal OWNER HOST  Read the currently permitted cache
peers refresh --principal OWNER HOST  Fetch one pinned home, without starting it
peers grant --principal OWNER --sessions ID,ID HOST
peers revoke --principal OWNER HOST   Revoke inventory and discard cached records

Flags precede HOST. Use exact host/session IDs. An empty --sessions value
revokes that host's inventory scope. Enrollment needs a reviewed private config,
existing native SSH login and a running Keelmux home. Inventory is read-only:
it grants no remote terminal, model, stop, move or failover capability.
`

func managePeers(ctx context.Context, stateDir string, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(out, peersHelp)
		return err
	}
	command := args[0]
	fs := flag.NewFlagSet("keelmux peers "+command, flag.ContinueOnError)
	fs.SetOutput(out)
	var file, principal, sessions string
	var refresh bool
	switch command {
	case "enroll":
		fs.StringVar(&file, "config", "", "owned private enrollment JSON")
	case "list", "inspect", "refresh", "revoke", "grant":
		fs.StringVar(&principal, "principal", "", "exact configured local owner")
		if command == "list" {
			fs.BoolVar(&refresh, "refresh", false, "fetch pinned homes within a shared deadline")
		}
		if command == "grant" {
			fs.StringVar(&sessions, "sessions", "", "comma-separated exact permitted session IDs")
		}
	default:
		return errors.New("unknown peers command; use peers --help")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var selected domain.ID
	var cfg peers.Config
	if command == "enroll" {
		if file == "" || len(fs.Args()) != 0 {
			return errors.New("enroll needs one --config private JSON file")
		}
		var err error
		cfg, err = peers.ReadConfig(file)
		if err != nil {
			return err
		}
	} else {
		if principal == "" || len(principal) > 128 || !utf8.ValidString(principal) || strings.TrimSpace(principal) != principal || strings.ContainsFunc(principal, unicode.IsControl) {
			return errors.New("select the exact configured --principal")
		}
		if command == "list" {
			if len(fs.Args()) != 0 {
				return errors.New("peers list accepts no host argument")
			}
		} else {
			if len(fs.Args()) != 1 || !domain.ValidObjectID(domain.ID(fs.Args()[0]), "host") {
				return errors.New("select one exact host ID; flags precede it")
			}
			selected = domain.ID(fs.Args()[0])
		}
	}
	grant := federation.Grant{Principal: principal, HostID: selected, Sessions: []domain.ID{}}
	if command == "grant" {
		specified := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "sessions" {
				specified = true
			}
		})
		if !specified {
			return errors.New("grant needs an explicit --sessions list; an empty value removes scope")
		}
		seen := map[domain.ID]bool{}
		if sessions != "" {
			for _, s := range strings.Split(sessions, ",") {
				id := domain.ID(s)
				if !domain.ValidObjectID(id, "session") || seen[id] || len(grant.Sessions) >= peers.MaxSessions {
					return errors.New("grant needs distinct exact session IDs within the 256-session limit")
				}
				seen[id] = true
				grant.Sessions = append(grant.Sessions, id)
			}
		}
	}
	paths, err := config.Resolve(stateDir)
	if err != nil {
		return err
	}
	dir := filepath.Join(paths.Dir, "peers")
	if command == "enroll" {
		if err := paths.Ensure(); err != nil {
			return err
		}
	} else if info, err := os.Lstat(paths.Dir); err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || st.Uid != uint32(os.Geteuid()) {
			return errors.New("peer client state must be an owned private directory, without a symlink")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Lstat(dir); os.IsNotExist(err) && command != "enroll" {
		if command != "list" {
			return errors.New("no enrolled peers; enroll an explicitly configured home first")
		}
		return json.NewEncoder(out).Encode(peers.Directory{Version: peers.Version, Sessions: []domain.Session{}, Workspaces: []peers.Workspace{}, Places: []peers.Place{}})
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	r, err := peers.Open(dir, nil)
	if err != nil {
		return err
	}
	defer r.Close()
	var result any
	switch command {
	case "enroll":
		result, err = r.Enroll(ctx, cfg)
	case "list":
		result, err = r.Directory(ctx, principal, refresh)
	case "inspect":
		result, err = r.View(selected, principal)
	case "refresh":
		result, err = r.Refresh(ctx, selected, principal)
	case "grant":
		if err = r.SetGrant(grant); err == nil {
			result, err = r.View(selected, principal)
		}
	case "revoke":
		if err = r.Revoke(selected, principal); err == nil {
			result, err = r.View(selected, principal)
		}
	}
	if err != nil {
		return err
	}
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(result)
}

func peersCommand(stateDir, entry string, args []string) error {
	if entry != "" {
		return errors.New("peer enrollment belongs to this local inventory store; select --state without --entry")
	}
	return managePeers(context.Background(), stateDir, args, os.Stdout)
}

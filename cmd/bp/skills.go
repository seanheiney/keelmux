package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"

	"github.com/seanheiney/agent-backplane/internal/skills"
)

func skillFlags(name string, args []string, installing bool) (string, string, []string, bool, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	root := fs.String("root", "", "user home or project directory")
	scope := fs.String("scope", skills.UserScope, "user or project")
	selected := fs.String("providers", "codex,claude", "comma-separated provider selection")
	var apply bool
	if installing {
		fs.BoolVar(&apply, "apply", false, "apply this pinned installation plan")
	}
	if err := fs.Parse(args); err != nil {
		return "", "", nil, false, err
	}
	if len(fs.Args()) != 0 {
		return "", "", nil, false, errors.New("unexpected skill command arguments")
	}
	if *root == "" {
		if *scope != skills.UserScope {
			return "", "", nil, false, errors.New("project scope requires --root")
		}
		var err error
		*root, err = os.UserHomeDir()
		if err != nil {
			return "", "", nil, false, err
		}
	}
	absolute, err := filepath.Abs(*root)
	if err != nil {
		return "", "", nil, false, err
	}
	var names []string
	for _, name := range strings.Split(*selected, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", "", nil, false, errors.New("provider selection cannot be empty")
		}
		names = append(names, name)
	}
	return absolute, *scope, names, apply, nil
}

func setupSkills(args []string) error {
	root, scope, selected, apply, err := skillFlags("bp setup", args, true)
	if err != nil {
		return err
	}
	preview, err := skills.PlanScope(root, scope, selected)
	if err != nil {
		return err
	}
	if !apply {
		return printJSON(preview)
	}
	results, err := skills.Apply(preview)
	if err != nil {
		return err
	}
	return printJSON(results)
}

func manageSkills(args []string) error {
	if len(args) == 0 || args[0] != "remove" {
		return errors.New("skills accepts remove; use bp setup for an installation preview")
	}
	root, scope, selected, _, err := skillFlags("bp skills remove", args[1:], false)
	if err != nil {
		return err
	}
	results, err := skills.RemoveScope(root, scope, selected)
	if err != nil {
		return err
	}
	return printJSON(results)
}

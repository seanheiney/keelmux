package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

const projectHelp = `Keelmux projects

  keelmux projects create --name "Project name" --workspace PATH [--project PROJECT_ID]
  keelmux projects list
  keelmux projects show PROJECT_ID

A project has a stable ID, name and absolute workspace at its execution home.
Creation prints its retry ID and performs no runtime or network activity.
Keep the same ID and configuration if a response is lost. Bind a thread with
keelmux ircd bind --project PROJECT_ID --session SESSION_ID --allow-user ACCOUNT.
Project channels contain one distinct bot per stable thread. Membership is
explicit and stays fixed across execution replacement and daemon restart.
`

func manageProjects(ctx context.Context, c *protocol.Client, args []string) error {
	if len(args) == 0 {
		return errors.New("projects needs an action; use keelmux projects --help")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("projects list takes no arguments")
		}
		out, err := c.Projects(ctx)
		if err != nil {
			return err
		}
		return printJSON(out)
	case "show":
		if len(args) != 2 || !domain.ValidObjectID(domain.ID(args[1]), "project") {
			return errors.New("projects show needs an exact project ID")
		}
		out, err := c.Project(ctx, domain.ID(args[1]))
		if err != nil {
			return err
		}
		return printJSON(out)
	case "create":
		fs := flag.NewFlagSet("keelmux projects create", flag.ContinueOnError)
		id, name, workspace := fs.String("project", "", "reuse the original immutable project ID"), fs.String("name", "", "project display name"), fs.String("workspace", "", "explicit project workspace")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 || *workspace == "" || *name == "" || *id != "" && !domain.ValidObjectID(domain.ID(*id), "project") {
			return errors.New("projects create needs name, workspace and a valid optional project ID")
		}
		path, err := filepath.Abs(*workspace)
		if err != nil {
			return err
		}
		if *id == "" {
			*id = string(domain.NewID("project"))
		}
		fmt.Fprintln(os.Stderr, "Project intent:", *id)
		out, err := c.CreateProject(ctx, protocol.ProjectCreateRequest{ID: domain.ID(*id), Name: *name, Workspace: path})
		if err != nil {
			return fmt.Errorf("project %s: %w; preserve the same --project and configuration after a lost response", *id, err)
		}
		return printJSON(out)
	default:
		return errors.New("unknown projects action; use keelmux projects --help")
	}
}

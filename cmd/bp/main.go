package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/seanheiney/agent-backplane/internal/config"
	"github.com/seanheiney/agent-backplane/internal/domain"
	"github.com/seanheiney/agent-backplane/internal/protocol"
	"github.com/seanheiney/agent-backplane/internal/tui"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bp:", err)
		os.Exit(1)
	}
}
func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func run() error {
	fs := flag.NewFlagSet("bp", flag.ContinueOnError)
	state := fs.String("state", "", "private state directory")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	args := fs.Args()
	if len(args) > 0 && args[0] == "version" {
		fmt.Println("bp development (protocol", protocol.Version+")")
		return nil
	}
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help") {
		fmt.Print(help)
		return nil
	}
	p, err := config.Resolve(*state)
	if err != nil {
		return err
	}
	if err = p.Ensure(); err != nil {
		return err
	}
	c := protocol.NewClient(p.Control)
	ctx := context.Background()
	if err = ensureDaemon(ctx, c, p); err != nil {
		return err
	}
	if len(args) == 0 {
		return tui.Run(ctx, c, p.Place)
	}
	switch args[0] {
	case "list":
		s, err := c.List(ctx)
		if err != nil {
			return err
		}
		return printJSON(s)
	case "run":
		rfs := flag.NewFlagSet("bp run", flag.ContinueOnError)
		name := rfs.String("name", "", "session name")
		workspace := rfs.String("workspace", "", "exact workspace path")
		operation := rfs.String("operation", "", "retry the same launch operation ID")
		if err = rfs.Parse(args[1:]); err != nil {
			return err
		}
		rest := rfs.Args()
		if len(rest) == 0 {
			return errors.New("run needs codex or claude; flags precede provider")
		}
		provider := rest[0]
		if provider != "codex" && provider != "claude" {
			return errors.New("supported native providers: codex, claude")
		}
		binary, err := exec.LookPath(provider)
		if err != nil {
			return fmt.Errorf("install %s through native instructions: %w", provider, err)
		}
		if *workspace == "" {
			*workspace, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		*workspace, err = filepath.Abs(*workspace)
		if err != nil {
			return err
		}
		if *name == "" {
			*name = provider + " · " + filepath.Base(*workspace)
		}
		if *operation == "" {
			*operation = string(domain.NewID("op"))
		}
		versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		v, _ := exec.CommandContext(versionCtx, binary, "--version").Output()
		argv := append([]string{binary}, rest[1:]...)
		r, err := c.Launch(ctx, protocol.LaunchRequest{OperationID: domain.ID(*operation), Name: *name, Workspace: *workspace, Provider: provider, ProviderVersion: strings.TrimSpace(string(v)), Argv: argv})
		if err != nil {
			return fmt.Errorf("launch operation %s: %w; retry only with this same --operation", *operation, err)
		}
		return printJSON(r)
	case "link":
		if len(args) > 1 && args[1] == "status" {
			s, err := c.List(ctx)
			if err != nil {
				return err
			}
			for _, v := range s {
				if string(v.ID) == os.Getenv("BP_SESSION_ID") {
					return printJSON(v)
				}
			}
			return errors.New("no exact managed context; attach intended terminal first")
		}
		r := protocol.LinkRequest{SessionID: domain.ID(os.Getenv("BP_SESSION_ID")), ExecutionID: domain.ID(os.Getenv("BP_EXECUTION_ID")), OperationID: domain.ID(os.Getenv("BP_OPERATION_ID")), Off: len(args) > 1 && args[1] == "off"}
		if r.SessionID == "" || r.ExecutionID == "" || r.OperationID == "" {
			return errors.New("link requires verified managed context; use bp run, then invoke bp link inside that session")
		}
		s, err := c.Link(ctx, r)
		if err != nil {
			return err
		}
		return printJSON(s)
	case "attach", "stop", "inspect":
		if len(args) < 2 {
			return fmt.Errorf("%s needs exact session ID", args[0])
		}
		sessions, err := c.List(ctx)
		if err != nil {
			return err
		}
		var s domain.Session
		for _, v := range sessions {
			if string(v.ID) == args[1] {
				s = v
				break
			}
		}
		if s.ID == "" {
			return errors.New("session not found")
		}
		if args[0] == "inspect" {
			return printJSON(s)
		}
		if args[0] == "stop" {
			return c.Stop(ctx, protocol.SessionRequest{SessionID: s.ID, ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, OperationID: domain.NewID("op")})
		}
		ro := len(args) > 2 && args[2] == "--read-only"
		req := protocol.LeaseRequest{SessionID: s.ID, ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, OperationID: domain.NewID("attach"), ClientID: string(domain.NewID("client")), ReadOnly: ro}
		a, err := c.Attach(ctx, req)
		if err != nil {
			return err
		}
		req.Token = a.Lease.Token
		defer c.Release(ctx, req)
		if len(a.Argv) == 0 {
			return errors.New("backend returned no native attach path")
		}
		cmd := exec.Command(a.Argv[0], a.Argv[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	case "reconcile":
		return c.Reconcile(ctx)
	case "doctor":
		_, err := c.List(ctx)
		if err != nil {
			return err
		}
		for _, binary := range []string{"tmux", "codex", "claude"} {
			path, e := exec.LookPath(binary)
			if e != nil {
				fmt.Println(binary + ": unavailable")
			} else {
				fmt.Println(binary + ": " + path)
			}
		}
		fmt.Println("Private daemon reachable. Account/model, skill loading and semantic controls need separate evidence.")
		return nil
	default:
		return fmt.Errorf("unknown command %q; bp help", args[0])
	}
}
func ensureDaemon(ctx context.Context, c *protocol.Client, p config.Paths) error {
	if _, err := c.List(ctx); err == nil {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	binary := filepath.Join(filepath.Dir(self), "bpd")
	if _, err = os.Stat(binary); err != nil {
		return errors.New("bpd unavailable beside bp; run make build or start bpd --state explicitly")
	}
	log, err := os.OpenFile(filepath.Join(p.Dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(binary, "--state", p.Dir)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = c.List(ctx); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not become ready; inspect %s", filepath.Join(p.Dir, "daemon.log"))
}

const help = `Agent Backplane — native terminal switcher
bp [--state DIR]                 Open remembered work
bp run [--name NAME] [--workspace PATH] [--operation ID] codex|claude [native args]
bp list                          Inspect registered work as JSON
bp attach SESSION [--read-only]   Enter native terminal; Ctrl-G d returns
bp link [status|off]              Link exact inherited managed context
bp inspect SESSION               Inspect capabilities and unknown evidence
bp stop SESSION                  Stop selected current execution
bp reconcile                     Recheck surviving executions
bp doctor                        Check local binaries and daemon
`

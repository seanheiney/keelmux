package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/seanheiney/keelmux/internal/adapters/providers"
	"github.com/seanheiney/keelmux/internal/buildinfo"
	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/peers"
	"github.com/seanheiney/keelmux/internal/protocol"
	"github.com/seanheiney/keelmux/internal/tui"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, filepath.Base(os.Args[0])+":", err)
		os.Exit(1)
	}
}
func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func run() error {
	fs := flag.NewFlagSet("keelmux", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), help) }
	state := fs.String("state", "", "private state directory")
	entry := fs.String("entry", "", "private entry socket; do not start a local daemon")
	version := fs.Bool("version", false, "print exact executable version")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	args := fs.Args()
	if len(args) > 0 && (args[0] == "peer-read-bridge" || args[0] == "peer-policy") {
		if fs.NFlag() != 0 {
			return errors.New("peer bridge commands accept no global state or entry options")
		}
		if args[0] == "peer-read-bridge" {
			return servePeerBridge(args[1:])
		}
		return peerPolicyCommand(context.Background(), args[1:], os.Stdout)
	}
	if len(args) > 0 && args[0] == "peer-export" {
		if len(args) != 1 || fs.NFlag() != 0 {
			return errors.New("peer-export accepts only its bounded stdin request, without flags or arguments")
		}
		return peers.Export(context.Background(), os.Stdin, os.Stdout)
	}
	if len(args) > 0 && (args[0] == "channels" || args[0] == "ircd" || args[0] == "projects") {
		scopedHelp := channelHelp
		if args[0] == "ircd" {
			scopedHelp = ircdHelp
		} else if args[0] == "projects" {
			scopedHelp = projectHelp
		}
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h" || args[1] == "help") {
			fmt.Print(scopedHelp)
			return nil
		}
		if len(args) > 2 && (args[len(args)-1] == "--help" || args[len(args)-1] == "-h") {
			fmt.Print(scopedHelp)
			return nil
		}
		if *entry != "" {
			return errors.New(args[0] + " configuration and messages belong to the local execution home")
		}
	}
	if *version {
		if len(args) != 0 {
			return errors.New("--version accepts no arguments; use version --json for metadata")
		}
		fmt.Println(buildinfo.String(protocol.Version))
		return nil
	}
	if len(args) > 0 && args[0] == "version" {
		if len(args) == 2 && args[1] == "--json" {
			return printJSON(buildinfo.Current(protocol.Version))
		}
		if len(args) != 1 {
			return errors.New("version accepts only --json")
		}
		fmt.Println(buildinfo.String(protocol.Version))
		return nil
	}
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help") {
		fmt.Print(help)
		return nil
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		switch args[0] {
		case "run", "attach", "stop", "inspect", "list", "directory", "link", "reconcile", "doctor", "providers", "daemon":
			fmt.Print(help)
			return nil
		}
	}
	if len(args) > 0 && args[0] == "setup" {
		return setupSkills(args[1:])
	}
	if len(args) > 0 && args[0] == "skills" {
		return manageSkills(args[1:])
	}
	if len(args) > 0 && (args[0] == "plugins" || args[0] == "profiles") {
		return libraryCommand(*state, *entry, args[0], args[1:])
	}
	if len(args) > 0 && args[0] == "tools" {
		return toolsCommand(context.Background(), *state, *entry, args[1:], os.Stderr, os.Stdout)
	}
	if len(args) > 0 && args[0] == "providers" {
		records, err := providers.Discover(context.Background())
		if err != nil {
			return err
		}
		return printJSON(records)
	}
	if len(args) > 0 && args[0] == "drive" {
		return driveNative(*entry, args[1:])
	}
	if len(args) > 0 && args[0] == "groups" {
		return groupsCommand(*state, *entry, args[1:])
	}
	if len(args) > 0 && args[0] == "peers" {
		return peersCommand(*state, *entry, args[1:])
	}
	if len(args) > 0 && args[0] == "usage" {
		return usageCommand(*state, *entry, args[1:])
	}
	if len(args) > 0 && args[0] == "diagnose" {
		return diagnoseCommand(*state, *entry, args[1:])
	}
	p, err := config.Resolve(*state)
	if err != nil {
		return err
	}
	if len(args) > 0 && args[0] == "daemon" {
		if *entry != "" {
			return errors.New("daemon lifecycle belongs to the local execution home")
		}
		return manageDaemon(p, args[1:])
	}
	if err = p.Ensure(); err != nil {
		return err
	}
	socket := p.Control
	if *entry != "" {
		if !filepath.IsAbs(*entry) {
			return errors.New("entry socket must be an explicit absolute path")
		}
		socket = *entry
	}
	c := protocol.NewClient(socket)
	ctx := context.Background()
	if *entry == "" {
		if err = ensureDaemon(ctx, c, p); err != nil {
			return err
		}
	} else {
		if len(args) > 0 && (args[0] == "run" || args[0] == "stop" || args[0] == "link" || args[0] == "reconcile") {
			return errors.New("this entry supports permitted inventory and native attachment; run management commands at the execution home")
		}
	}
	if len(args) == 0 {
		if *entry == "" {
			return tui.RunLocal(ctx, c, p.Place)
		}
		return tui.Run(ctx, c, p.Place)
	}
	switch args[0] {
	case "projects":
		return manageProjects(ctx, c, args[1:])
	case "ircd":
		return manageIRCD(ctx, c, args[1:])
	case "channels":
		return manageChannels(ctx, c, args[1:])
	case "directory":
		directory, err := c.Directory(ctx)
		if err != nil {
			return err
		}
		return printJSON(directory)
	case "list":
		s, err := c.List(ctx)
		if err != nil {
			return err
		}
		return printJSON(s)
	case "run":
		rfs := flag.NewFlagSet("keelmux run", flag.ContinueOnError)
		name := rfs.String("name", "", "session name")
		workspace := rfs.String("workspace", "", "exact workspace path")
		operation := rfs.String("operation", "", "retry the same launch operation ID")
		profile := rfs.String("profile", "default", "exact reusable capability profile")
		application := rfs.String("application", "work", "verified workspace application ID")
		if err = rfs.Parse(args[1:]); err != nil {
			return err
		}
		rest := rfs.Args()
		if len(rest) == 0 {
			if *operation != "" {
				// Retry the original home-recorded intent before inspecting current
				// library selections, native files, PATH or provider versions.
				otherFlags := false
				rfs.Visit(func(f *flag.Flag) { otherFlags = otherFlags || f.Name != "operation" })
				if otherFlags {
					return errors.New("operation-only retry accepts only --operation ID; the recorded original selects its workspace, provider and profile")
				}
				r, retryErr := c.RetryLaunch(ctx, domain.ID(*operation))
				if retryErr != nil {
					return fmt.Errorf("launch operation %s: %w", *operation, retryErr)
				}
				return printJSON(r)
			}
			return errors.New("run needs codex, claude, gemini, grok or opencode; flags precede provider")
		}
		provider := rest[0]
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
		explicitProfile := false
		rfs.Visit(func(f *flag.Flag) {
			if f.Name == "profile" || f.Name == "application" {
				explicitProfile = true
			}
		})
		nativeArgs, snapshot, err := prepareLibraryLaunch(ctx, p.Dir, provider, *workspace, *profile, *application, explicitProfile, rest[1:])
		if err != nil {
			return err
		}
		argv, version, err := providers.SupportedLaunch(ctx, provider, nativeArgs)
		if err != nil {
			return err
		}
		if *name == "" {
			*name = provider + " · " + filepath.Base(*workspace)
		}
		if *operation == "" {
			*operation = string(domain.NewID("op"))
		}
		r, err := c.Launch(ctx, protocol.LaunchRequest{OperationID: domain.ID(*operation), Name: *name, Workspace: *workspace, Provider: provider, ProviderVersion: version, Argv: argv, Library: snapshot})
		if err != nil {
			return fmt.Errorf("launch operation %s: %w; retry the recorded original with keelmux run --operation %s", *operation, err, *operation)
		}
		return printJSON(r)
	case "link":
		if len(args) > 2 || (len(args) == 2 && args[1] != "status" && args[1] != "off") {
			return errors.New("link accepts only status or off")
		}
		r := protocol.LinkRequest{SessionID: domain.ID(os.Getenv("BP_SESSION_ID")), ExecutionID: domain.ID(os.Getenv("BP_EXECUTION_ID")), OperationID: domain.ID(os.Getenv("BP_OPERATION_ID")), Token: os.Getenv("BP_LINK_TOKEN"), Status: len(args) > 1 && args[1] == "status", Off: len(args) > 1 && args[1] == "off"}
		if r.SessionID == "" || r.ExecutionID == "" || r.OperationID == "" {
			return errors.New("link requires verified managed context; use keelmux run, then invoke keelmux link inside that session")
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
		if args[0] == "attach" && (len(args) > 3 || (len(args) == 3 && args[2] != "--read-only")) {
			return errors.New("attach accepts SESSION [--read-only]")
		}
		if args[0] == "inspect" && len(args) != 2 {
			return errors.New("inspect accepts only SESSION")
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
			op := domain.NewID("op")
			if len(args) == 4 && args[2] == "--operation" {
				op = domain.ID(args[3])
			} else if len(args) != 2 {
				return errors.New("stop accepts SESSION [--operation ID]")
			}
			fmt.Fprintln(os.Stderr, "Stop intent:", op)
			if err := c.Stop(ctx, protocol.SessionRequest{SessionID: s.ID, ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, OperationID: op}); err != nil {
				return fmt.Errorf("stop operation %s: %w; retry only with this same --operation", op, err)
			}
			return nil
		}
		ro := len(args) > 2 && args[2] == "--read-only"
		return directAttach(ctx, c, p, s, ro)
	case "reconcile":
		return c.Reconcile(ctx)
	case "doctor":
		_, err := c.List(ctx)
		if err != nil {
			return err
		}
		if *entry != "" {
			fmt.Println("Private entry reachable. Use keelmux directory for execution-home availability; native credentials remain at each home.")
			return nil
		}
		ready := true
		for _, binary := range []string{"keelmux-supervisor"} {
			self, _ := os.Executable()
			path, e := exec.LookPath(filepath.Join(filepath.Dir(self), binary))
			if e != nil {
				path, e = exec.LookPath(binary)
			}
			if e != nil {
				ready = false
				fmt.Println(binary + ": unavailable")
			} else {
				fmt.Println(binary + ": " + path)
			}
		}
		records, err := providers.Discover(ctx)
		if err != nil {
			return err
		}
		providerInstalled := false
		for _, r := range records {
			providerInstalled = providerInstalled || (r.Installed && r.NativeTerminal)
			fmt.Printf("%s: installed=%t version=%s credentials=%s method=%s native-terminal=%t\n", r.Name, r.Installed, r.Version.Value, r.Auth.State.Value, r.Auth.Method.Value, r.NativeTerminal)
		}
		fmt.Println("Private daemon reachable. Native credential status does not establish account identity, model access, quota, or subscription eligibility. Use keelmux providers for source-qualified details; keelmux setup previews connection skills.")
		if !ready || !providerInstalled {
			return errors.New("local prerequisites missing; install the complete Keelmux release and at least one supported native CLI, then run keelmux doctor")
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q; keelmux help", args[0])
	}
}
func ensureDaemon(ctx context.Context, c *protocol.Client, p config.Paths) error {
	if _, err := c.List(ctx); err == nil {
		return nil
	} else if !errors.Is(err, syscall.ENOENT) && !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("existing daemon could not be verified: %w; refusing to start a second daemon", err)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	binary := filepath.Join(filepath.Dir(self), "keelmuxd")
	if _, err = os.Stat(binary); os.IsNotExist(err) {
		binary = filepath.Join(filepath.Dir(self), "bpd")
		_, err = os.Stat(binary)
	}
	if err != nil {
		return errors.New("daemon unavailable beside keelmux; reinstall the complete distribution or start keelmuxd --state explicitly")
	}
	log, err := config.OpenPrivateLog(filepath.Join(p.Dir, "daemon.log"))
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

const help = `Keelmux — keep your agents running, keep your place
keelmux [--state DIR]                 Open remembered work
keelmux --state CLIENTDIR --entry SOCKET
                                 Open permitted work through a private entry
keelmux run [--name NAME] [--workspace PATH] [--operation ID] PROVIDER [native args]
                                 Providers: codex, claude, gemini, grok, opencode
                                 Profile: --profile ID --application ID
keelmux run --operation ID            Retry only the original recorded launch
keelmux list                          Inspect registered work as JSON
keelmux directory                     Inspect home availability and permitted inventory
keelmux attach SESSION [--read-only]   Enter native terminal; Ctrl-G d returns
keelmux link [status|off]              Link exact inherited managed context
keelmux inspect SESSION               Inspect capabilities and unknown evidence
keelmux stop SESSION [--operation ID]  Stop current execution; preserve ID on retry
keelmux reconcile                     Recheck surviving executions
keelmux daemon status|stop|restart    Inspect or restart only the owned daemon
keelmux doctor                        Check local binaries and daemon
keelmux diagnose [--json]             Read existing state without starting services
keelmux version [--json]              Print release and protocol provenance
keelmux providers                     Inspect qualified native status without model work
keelmux channels --help               Bind chat roots, read authored inbox and reply
keelmux ircd --help                   Provision and manage a private IRC server
keelmux projects --help               Create and inspect durable project identities
keelmux groups --help                 Manage company/product navigation collections
keelmux peers --help                  Explicit pinned SSH inventory
keelmux peer-policy --help            Scoped home read-only SSH export policy
keelmux usage --help                  Contextual source-qualified usage
keelmux plugins --help                Reusable skill/rule/tool packs and toggles
keelmux profiles --help               Exact reusable capability selections
keelmux tools --help                  Tool switches, declarations and explicit health
keelmux setup [--root DIR] [--scope user|project] [--providers codex,claude] [--apply]
                                 Preview or install owned connection skills
keelmux skills remove --root DIR [--scope user|project] [--providers codex,claude]
                                 Remove matching owned skill files
`

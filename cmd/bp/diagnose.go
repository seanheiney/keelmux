package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/seanheiney/keelmux/internal/config"
	"github.com/seanheiney/keelmux/internal/diagnostics"
)

const diagnoseHelp = `Keelmux read-only diagnostics

  keelmux [--state DIR] diagnose [--json] [--probe-versions]

Inspect existing private paths, saved client place, registry and channel/IRC
configuration. An existing local socket is queried only after owner-process
checks. Remote entry sockets are inspected statically. Missing state is never
created. No daemon/server startup, repair, authentication or model work occurs.

--probe-versions explicitly permits bounded codex/claude --version subprocesses.
Without it, provider availability is a static PATH/filesystem observation.
`

// Captain dispatches this before Ensure/ensureDaemon. Keep the writer helper
// independent of process-wide stdout so read-only behavior is testable.
func diagnoseCommand(stateDir, entry string, args []string) error {
	return diagnoseTo(context.Background(), os.Stdout, stateDir, entry, args)
}
func diagnoseTo(ctx context.Context, w io.Writer, stateDir, entry string, args []string) error {
	fs := flag.NewFlagSet("keelmux diagnose", flag.ContinueOnError)
	fs.SetOutput(w)
	fs.Usage = func() { fmt.Fprint(w, diagnoseHelp) }
	jsonMode := fs.Bool("json", false, "machine-readable redacted observations")
	probe := fs.Bool("probe-versions", false, "explicitly run native --version probes")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("diagnose accepts only --json and --probe-versions")
	}
	p, err := config.Resolve(stateDir)
	if err != nil {
		return err
	}
	r := diagnostics.Collect(ctx, p, entry, *probe)
	if *jsonMode {
		e := json.NewEncoder(w)
		e.SetIndent("", "  ")
		return e.Encode(r)
	}
	if _, err = fmt.Fprintf(w, "Diagnostics for %q\n", p.Dir); err != nil {
		return err
	}
	for _, c := range r.Checks {
		line := c.Name + ": " + c.State + " — " + c.Summary
		if c.Freshness != "" {
			line += " Evidence: " + c.Freshness + "."
		}
		if len(c.Counts) > 0 {
			keys := make([]string, 0, len(c.Counts))
			for k := range c.Counts {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				line += fmt.Sprintf(" %s=%d", k, c.Counts[k])
			}
		}
		if _, err = fmt.Fprintln(w, line); err != nil {
			return err
		}
		if c.Action != "" {
			if _, err = fmt.Fprintln(w, "  "+c.Action); err != nil {
				return err
			}
		}
	}
	if r.Daemon != nil {
		if _, err = fmt.Fprintf(w, "Daemon instance %s, PID %d, protocol %s\n", r.Daemon.Instance, r.Daemon.PID, r.Daemon.Protocol); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(w, strings.TrimSpace(r.Coverage))
	return err
}

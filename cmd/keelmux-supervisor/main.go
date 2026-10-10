// keelmux-supervisor is an installed process-lifetime helper, not a daemon shim.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/seanheiney/keelmux/internal/adapters/nativepty"
	"github.com/seanheiney/keelmux/internal/buildinfo"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "keelmux-supervisor:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println(buildinfo.String(protocol.Version))
		return nil
	}
	if len(args) == 0 {
		return errors.New("expected serve or attach")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	state := fs.String("state", "", "private native supervisor directory")
	execution := fs.String("execution", "", "exact execution identity")
	operation := fs.String("operation", "", "exact launch operation identity")
	instance := fs.String("instance", "", "exact runtime instance")
	readOnly := fs.Bool("read-only", false, "observe without input or resizing")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	switch args[0] {
	case "serve":
		return nativepty.Serve(*state, domain.ID(*execution), os.Stdin)
	case "attach":
		return nativepty.Attach(context.Background(), *state, domain.ID(*execution), domain.ID(*operation), *instance, *readOnly, os.Stdin, os.Stdout)
	default:
		return errors.New("expected serve or attach")
	}
}

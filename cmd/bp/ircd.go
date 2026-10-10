package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

const ircdHelp = `Keelmux managed IRC

Provision and control a private pinned Ergo server explicitly:
  keelmux ircd init [--irc-port PORT] [--api-port PORT]
  keelmux ircd status
  keelmux ircd start [--operation OPERATION_ID]
  keelmux ircd stop --instance EXECUTION_ID
  keelmux ircd restart --instance EXECUTION_ID [--operation OPERATION_ID]
  keelmux ircd account
  keelmux ircd bind --session SESSION_ID --allow-user SERVICES_ACCOUNT \
    [--binding BINDING_ID] [--operation CHANNEL_OPERATION_ID]

Use the exact live instance from status for stop/restart. Start/restart and bind
print durable retry IDs; preserve the same IDs and configuration after a lost
response. Repeated channel attempts only confirm registration read-only. An
intent consumed before unsuccessful authentication can require manual repair.

Init/account expose local connection fields and private credential file paths;
status excludes those references. Repeat --allow-user for each exact permitted
services account. A bind creates a deterministic registered channel for the
session's stable thread, with authored inbox and explicit replies. Native
terminals handle model interaction; semantic dispatch remains unavailable.
`

func validIRCDID(raw, kind string) bool {
	if len(raw) != len(kind)+33 || !strings.HasPrefix(raw, kind+"_") {
		return false
	}
	for _, r := range raw[len(kind)+1:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func validIRCDOperation(raw string) bool {
	return validIRCDID(raw, "operation") || validIRCDID(raw, "op")
}

func manageIRCD(ctx context.Context, c *protocol.Client, args []string) error {
	if len(args) == 0 {
		return errors.New("ircd needs an action; use keelmux ircd --help")
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return errors.New("ircd status takes no arguments")
		}
		out, err := c.IRCD(ctx)
		if err != nil {
			return err
		}
		return printJSON(out)
	case "init":
		fs := flag.NewFlagSet("keelmux ircd init", flag.ContinueOnError)
		ircPort, apiPort := fs.Int("irc-port", 0, "loopback IRC TLS port; zero selects an available port"), fs.Int("api-port", 0, "loopback authenticated API port; zero selects an available port")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 || *ircPort < 0 || *ircPort > 65535 || *apiPort < 0 || *apiPort > 65535 || *ircPort != 0 && *ircPort == *apiPort {
			return errors.New("ircd init needs distinct bounded port flags and no positional arguments")
		}
		out, err := c.InitIRCD(ctx, protocol.IRCDInitRequest{IRCPort: *ircPort, APIPort: *apiPort})
		if err != nil {
			return err
		}
		return printJSON(out)
	case "start", "restart":
		fs := flag.NewFlagSet("keelmux ircd "+args[0], flag.ContinueOnError)
		operation := fs.String("operation", "", "reuse the original durable operation ID after a lost response")
		var instance *string
		if args[0] == "restart" {
			instance = fs.String("instance", "", "exact live execution instance from ircd status")
		}
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 || *operation != "" && !validIRCDOperation(*operation) {
			return errors.New("ircd lifecycle needs a valid exact operation ID and no positional arguments")
		}
		if instance != nil && !validIRCDID(*instance, "execution") {
			return errors.New("ircd restart requires --instance with the exact live execution ID")
		}
		if *operation == "" {
			*operation = string(domain.NewID("operation"))
		}
		fmt.Fprintln(os.Stderr, "IRC lifecycle intent:", *operation)
		var out protocol.IRCDStatus
		var err error
		if instance == nil {
			out, err = c.StartIRCD(ctx, domain.ID(*operation))
		} else {
			out, err = c.RestartIRCD(ctx, domain.ID(*instance), domain.ID(*operation))
		}
		if err != nil {
			return fmt.Errorf("IRC lifecycle %s: %w; preserve this same --operation and expected instance for reconciliation", *operation, err)
		}
		return printJSON(out)
	case "stop":
		fs := flag.NewFlagSet("keelmux ircd stop", flag.ContinueOnError)
		instance := fs.String("instance", "", "exact live execution instance from ircd status")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 || !validIRCDID(*instance, "execution") {
			return errors.New("ircd stop requires --instance with the exact live execution ID")
		}
		return c.StopIRCD(ctx, domain.ID(*instance))
	case "account":
		if len(args) != 1 {
			return errors.New("ircd account takes no arguments and manages only the private bot")
		}
		out, err := c.IRCDAccount(ctx, "keelmux-bot")
		if err != nil {
			return err
		}
		return printJSON(out)
	case "bind":
		fs := flag.NewFlagSet("keelmux ircd bind", flag.ContinueOnError)
		session := fs.String("session", "", "existing stable Keelmux session ID")
		binding := fs.String("binding", "", "reuse the original immutable binding ID")
		operation := fs.String("operation", "", "reuse the original registered channel operation ID")
		var allow allowedUsers
		fs.Var(&allow, "allow-user", "permitted exact services account; repeat for each sender")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 || !validIRCDID(*session, "session") || *binding != "" && !validIRCDID(*binding, "binding") || *operation != "" && !validIRCDOperation(*operation) || len(allow) == 0 || len(allow) > 256 {
			return errors.New("ircd bind requires session, allow-user and valid optional binding/operation IDs")
		}
		seen := map[string]bool{}
		for _, actor := range allow {
			if seen[actor] {
				return errors.New("ircd bind rejects duplicate allow-user identities")
			}
			seen[actor] = true
		}
		if *binding == "" {
			*binding = string(domain.NewID("binding"))
		}
		if *operation == "" {
			*operation = string(domain.NewID("operation"))
		}
		fmt.Fprintln(os.Stderr, "IRC binding intent:", *binding, "channel operation:", *operation)
		out, err := c.BindIRCD(ctx, protocol.IRCDBindRequest{ID: domain.ID(*binding), SessionID: domain.ID(*session), OperationID: domain.ID(*operation), Allowlist: allow})
		if err != nil {
			return fmt.Errorf("IRC binding %s channel operation %s: %w; retry only with these same --binding, --operation and configuration", *binding, *operation, err)
		}
		return printJSON(out)
	default:
		return errors.New("unknown ircd action; use keelmux ircd --help")
	}
}

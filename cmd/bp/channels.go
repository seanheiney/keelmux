package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/seanheiney/keelmux/internal/channels"
	"github.com/seanheiney/keelmux/internal/domain"
	"github.com/seanheiney/keelmux/internal/protocol"
)

const channelHelp = `Keelmux channel bindings

Bind an existing chat root to a session's stable Keelmux thread:
  keelmux channels bind --provider mattermost --session SESSION --server HTTPS_URL \
    --channel CHANNEL_ID --root POST_ID --credential-file ABS_PATH --allow-user USER_ID
  keelmux channels bind --provider slack --session SESSION --channel CHANNEL_ID \
    --root ROOT_TS --credential-file ABS_PATH --allow-user USER_ID
  keelmux channels bind --provider irc --session SESSION --server ircs://HOST:PORT \
    --channel '#channel' --credential-file ABS_PATH --allow-user SERVICES_ACCOUNT

  keelmux channels list
  keelmux channels history [--after SEQUENCE] [--limit 50] THREAD
  keelmux channels send --binding BINDING [--operation MESSAGE_ID] --text 'Reply'
  keelmux channels delivery MESSAGE_ID
  keelmux channels sync BINDING
  keelmux channels reconcile MESSAGE_ID
  keelmux channels disable BINDING

Repeat --allow-user for each exact permitted identity. Credentials stay in owned
private files. Bind/send print durable retry IDs; reuse the same ID and content
after a lost local response. Uncertain external posts are never resent.

This is authored inbox and explicit replies. Native terminals handle model
interaction; semantic dispatch and remote approvals remain unavailable.
`

type allowedUsers []string

func (v *allowedUsers) String() string { return fmt.Sprint([]string(*v)) }
func (v *allowedUsers) Set(s string) error {
	if !channels.ValidID(s) {
		return channels.ErrInvalid
	}
	*v = append(*v, s)
	return nil
}

func manageChannels(ctx context.Context, c *protocol.Client, args []string) error {
	if len(args) == 0 {
		return errors.New("channels needs an action; use keelmux channels --help")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("channels list takes no arguments")
		}
		out, err := c.Channels(ctx)
		if err != nil {
			return err
		}
		return printJSON(out)
	case "bind":
		fs := flag.NewFlagSet("keelmux channels bind", flag.ContinueOnError)
		provider := fs.String("provider", "", "mattermost, slack or irc")
		session := fs.String("session", "", "exact existing session ID")
		endpoint := fs.String("server", "", "TLS server endpoint")
		channel := fs.String("channel", "", "exact channel ID or IRC channel")
		root := fs.String("root", "", "existing root post ID or Slack timestamp")
		credential := fs.String("credential-file", "", "owned private credential file")
		id := fs.String("binding", "", "reuse original binding ID after a lost response")
		var allow allowedUsers
		fs.Var(&allow, "allow-user", "permitted exact sender ID; repeat for each sender")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 {
			return errors.New("binding flags accept no positional arguments")
		}
		if *session == "" || *channel == "" || *credential == "" || len(allow) == 0 {
			return errors.New("binding needs session, channel, credential-file and at least one allow-user")
		}
		if *provider == "slack" && *endpoint == "" {
			*endpoint = "https://slack.com/api"
		}
		if *id == "" {
			*id = string(domain.NewID("binding"))
		}
		path, err := filepath.Abs(*credential)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Binding intent:", *id)
		out, err := c.BindChannel(ctx, protocol.ChannelBindRequest{ID: domain.ID(*id), SessionID: domain.ID(*session), Provider: channels.Provider(*provider), Endpoint: *endpoint, ChannelID: *channel, RootID: *root, CredentialRef: path, Allowlist: allow})
		if err != nil {
			return fmt.Errorf("binding %s: %w; retry only with this same --binding and configuration", *id, err)
		}
		return printJSON(out)
	case "send":
		fs := flag.NewFlagSet("keelmux channels send", flag.ContinueOnError)
		binding := fs.String("binding", "", "destination binding ID")
		id := fs.String("operation", "", "retry the original message operation ID")
		text := fs.String("text", "", "authored reply text")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 || *binding == "" || !channels.ValidText(*text) {
			return errors.New("send needs binding and nonempty bounded text")
		}
		if *id == "" {
			*id = string(domain.NewID("message"))
		}
		fmt.Fprintln(os.Stderr, "Message intent:", *id)
		out, err := c.SendChannelMessage(ctx, protocol.ChannelSendRequest{ID: domain.ID(*id), BindingID: domain.ID(*binding), Text: *text})
		if err != nil {
			return fmt.Errorf("message %s: %w; retry only with this same --operation, destination and text", *id, err)
		}
		return printJSON(out)
	case "history":
		fs := flag.NewFlagSet("keelmux channels history", flag.ContinueOnError)
		after := fs.Int64("after", 0, "authored sequence cursor")
		limit := fs.Int("limit", 50, "page size, at most 100")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 1 || *after < 0 || *limit < 1 || *limit > 100 {
			return errors.New("history needs THREAD and bounded after/limit flags before the thread")
		}
		out, err := c.ChannelHistory(ctx, domain.ID(fs.Args()[0]), *after, *limit)
		if err != nil {
			return err
		}
		return printJSON(out)
	case "disable", "sync", "delivery", "reconcile":
		if len(args) != 2 {
			return fmt.Errorf("channels %s needs one exact ID", args[0])
		}
		id := domain.ID(args[1])
		switch args[0] {
		case "disable":
			return c.DisableChannel(ctx, id)
		case "sync":
			out, err := c.SyncChannel(ctx, id)
			if err != nil {
				return err
			}
			return printJSON(out)
		case "delivery":
			out, err := c.ChannelDelivery(ctx, id)
			if err != nil {
				return err
			}
			return printJSON(out)
		default:
			out, err := c.ReconcileChannelDelivery(ctx, id)
			if err != nil {
				return err
			}
			return printJSON(out)
		}
	default:
		return errors.New("unknown channel action; use keelmux channels --help")
	}
}

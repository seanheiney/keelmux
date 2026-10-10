# Bind a thread to chat

**Development branch:** this guide describes the next release. The published v0.1.0 installer provides the qualified terminal core; it does not include channel bindings. Check the installed `keelmux version` and the release's qualification before using this guide.

A binding connects a session's stable Keelmux thread to one Mattermost or Slack root, or to one authenticated IRC channel. Read authored notes in the switcher and send an explicit reply to the selected binding. The daemon preserves messages and delivery receipts independently of the terminal process.

Chat messages are notes. They do not submit model prompts, accept native approvals or execute commands. Continue model interaction in the native terminal. A `delivered` receipt means the external transport acknowledged the post; it does not establish that someone read it or that a model accepted it.

## Mattermost first

Use a dedicated bot on the intended Mattermost server, give it access to the intended channel, and create the root post there. Obtain the exact channel ID, root post ID and permitted human user IDs. Display names are insufficient. Keelmux verifies the authenticated bot, channel team and existing root before it activates a binding. [Mattermost bot accounts](https://docs.mattermost.com/developers/integrate/reference/bot-accounts), [post API](https://docs.mattermost.com/api/reference/create-post).

Save the bot token in a local file using your preferred secret manager or editor. The file must be an owned regular file at an absolute path, with no group or other access, containing at most 8 KiB. Do not put the token in a command argument, repository, chat, screenshot or issue attachment. Keelmux reads the file without changing permissions; a missing, public or symlinked file is refused. Tokens are not stored in the message registry or printed in channel status.

Find the selected session:

```sh
keelmux list
```

Replace every uppercase placeholder with its exact value:

```sh
keelmux channels bind --provider mattermost --session session_EXACT \
  --server https://mattermost.example.com \
  --channel CHANNEL_ID --root ROOT_POST_ID \
  --credential-file /absolute/private/mattermost-token \
  --allow-user HUMAN_USER_ID
```

Repeat `--allow-user` for each permitted sender. The command prints a `binding_…` intent before contacting the daemon. Preserve it if the local response is lost, and retry the identical configuration with `--binding binding_ORIGINAL`. An existing binding ID cannot be redirected or reactivated with different settings.

Intake starts at activation. The bot's own messages, senders outside the allowlist, other roots and unsupported message kinds do not enter the authored inbox. The stored note is the first observed text: subsequent remote edits or deletions are not projected. Treat the external platform as the authoritative current message view. This is a scoped inbox, not a complete compliance archive.

## Read and reply

List the verified routing scope, including stable thread ID, bot identity, channel/root, allowed users and activation time:

```sh
keelmux channels list
keelmux channels history --after 0 --limit 50 thread_EXACT
```

History uses an authored sequence cursor. To read the next page, pass the last returned sequence as `--after`. It does not mix terminal output into chat history.

Every reply names one destination. Multiple bindings on one thread do not broadcast to one another:

```sh
keelmux channels send --binding binding_EXACT --text 'An explicit reply'
```

The command prints a `message_…` intent and returns the durable local message and delivery receipt. The daemon sends asynchronously. Inspect its outcome:

```sh
keelmux channels delivery message_EXACT
```

| Receipt | Meaning and next action |
| --- | --- |
| `queued` | Local intent committed; no external attempt claimed yet |
| `attempted` | The daemon committed its one allowed external attempt |
| `delivered` | Matching transport acknowledgement and remote message ID recorded |
| `failed` | Known rejection or route disabled before claim; inspect the finite reason code |
| `uncertain` | A post may exist; preserve the intent and inspect read-only reconciliation |

After a lost local response, repeat the exact original binding, operation ID and text. This returns the stored operation; it does not authorize a second external post:

```sh
keelmux channels send --binding binding_ORIGINAL \
  --operation message_ORIGINAL --text 'The exact original text'
```

A changed destination or text with that operation ID conflicts. Do not create a fresh ID to resolve uncertainty. Reconciliation only looks for a matching existing post:

```sh
keelmux channels reconcile message_ORIGINAL
```

Bounded history, a missing post or a timeout can leave the receipt uncertain. The daemon never automatically resends it, including after restart.

`channels sync binding_EXACT` performs one bounded worker pass: it can send one queued reply and fetch a page. It is not a read-only command. Ordinary background intake observes provider rate limits; it does not require repeated manual sync.

## Disable or change a binding

```sh
keelmux channels disable binding_EXACT
```

Disable prevents future claims and cancels queued work. It waits for an admitted send to settle or become uncertain; an already sent post can still exist remotely. Stored messages and receipts remain available. To change the allowlist, bot, server or destination, create a new binding with a new ID. Disable the old active route first if the external address is unchanged.

Rotating a token for the same authenticated bot is supported through its existing private file. Changing to another bot identity is refused before posting and needs a new binding. Keep credentials available while resolving old uncertain effects; a new identity cannot authenticate the original one.

## Slack

Use an app credential with posting and thread-history capabilities for the intended conversation. Slack bot history permissions vary by conversation type and app distribution; a token that can post may still be unable to read replies. Keelmux refuses unavailable history rather than substituting a broader credential. [Slack thread history](https://docs.slack.dev/reference/methods/conversations.replies/), [posting](https://docs.slack.dev/reference/methods/chat.postMessage/).

```sh
keelmux channels bind --provider slack --session session_EXACT \
  --channel CHANNEL_ID --root 'EXACT_ROOT_TS' \
  --credential-file /absolute/private/slack-token --allow-user USER_ID
```

The endpoint is fixed to `https://slack.com/api`. Preserve the exact string root timestamp, including its decimal portion. Keelmux verifies the workspace and authenticated self, follows bounded pagination and honors rate limits. Local correlation metadata helps reconcile a post; it does not guarantee server-side exactly-once creation.

## IRC

IRC uses one persistent TLS connection to an explicit server and channel. The server must support SASL, authenticated account tags, opaque message IDs and the capabilities required for correlated echo. Allowed senders are exact services accounts, never nicknames. IRC has no native root-thread equivalent in this mode.

```sh
keelmux channels bind --provider irc --session session_EXACT \
  --server ircs://irc.example.com:6697 --channel '#agents' \
  --credential-file /absolute/private/irc-account.json \
  --allow-user HUMAN_SERVICES_ACCOUNT
```

The private credential file uses bounded JSON:

```json
{"account":"bot-services-account","password":"SASL-password","nick":"optional-bot-nick"}
```

The server-authenticated account determines identity. The optional nickname is presentation. A labeled echo records server acceptance only; it is not proof every recipient received the message. Missing history and disconnected intervals remain explicit coverage limits. Reconnection does not silently establish recovered history. [SASL](https://ircv3.net/specs/extensions/sasl-3.2.html), [account tags](https://ircv3.net/specs/extensions/account-tag.html), [echo semantics](https://ircv3.net/specs/extensions/echo-message.html).

## Operational boundaries

Channel control runs at the local execution home through its private socket. The private entry does not expose channel management. Bindings are per-user authority; groups and native terminal attachment permissions do not grant remote senders command access.

The registry and client drafts contain private message text and provenance. Back them up and redact support reports accordingly. Logs and receipts can be shared only after inspecting their content. Keep the database on a local filesystem and operate backups with the [recovery guide](operations.md).

Before claiming a live integration, record the exact Keelmux source, server/app version, authenticated bot/account, intended channel/root, incoming allowlisted note, outgoing matching remote post, lost acknowledgement behavior and cleanup. Protocol fixtures establish implementation behavior; they do not establish acceptance on your server. See the [support matrix](support-matrix.md) and [routing contract](../research/CHANNEL_ROUTING.md).

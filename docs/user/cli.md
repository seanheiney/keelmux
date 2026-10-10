# CLI reference

`keelmux` is the primary command. `bp` is the compatibility alias. Examples use the release name; a pre-rename development checkout provides `./bin/bp`.

Global flags precede the command. Flags belonging to `run` precede the provider name. A session argument is its exact `session_…` ID from inventory or a receipt.

| Command | Behavior |
| --- | --- |
| `keelmux` | Open the local remembered switcher, starting its daemon when appropriate |
| `keelmux --state DIR` | Use the selected private state directory |
| `keelmux --state CLIENTDIR --entry SOCKET` | Connect to an explicit private entry; do not start a local daemon |
| `keelmux version` | Display application/protocol version; the development base prints a development version |
| `keelmux help` | Show command help |
| `keelmux providers` | Probe native installation/version and qualified login status |
| `keelmux doctor` | Report daemon and dependency checks; no model turn or sign-in |
| `keelmux list` | Return registered sessions as JSON |
| `keelmux directory` | Return inventory and execution-home availability |
| `keelmux inspect SESSION` | Return exact session capabilities and evidence |
| `keelmux run [--name NAME] [--workspace PATH] [--operation ID] codex\|claude [native args]` | Launch an owned native terminal |
| `keelmux attach SESSION [--read-only]` | Attach to the selected terminal; Ctrl-G d returns |
| `keelmux stop SESSION [--operation ID]` | Stop that current owned execution; preserve the operation ID on retry |
| `keelmux reconcile` | Inspect surviving local runtimes; do not relaunch missing ones |
| `keelmux link [status\|off]` | Link the exact inherited managed context or inspect/disable its link |
| `keelmux setup [--root DIR] [--scope user\|project] [--providers codex,claude] [--apply]` | Preview or apply the selected owned skill bundle |
| `keelmux skills remove [--root DIR] [--scope user\|project] [--providers codex,claude]` | Archive only matching owned skill bundles |

The selected release contract also provides `keelmux --version` and `keelmux version --json`. Version metadata identifies the application version, source commit and `bp/1` protocol; help and version return before state or daemon access. The pre-rename base supports the text `version` command; the new metadata and clean `--help` exits are being integrated for release.

## Native arguments

`run` uses an argument array. It validates the selected interactive launch and preserves supported native arguments. Provider management commands such as login, logout, install, upgrade and app-server are separate native workflows; they are refused through managed `run`. Claude print/background workflows are outside this interactive path.

```sh
keelmux run --name "Backend" --workspace /absolute/project codex --model gpt-6.1-sol
```

This requests a native model. It does not prove that account has access or that a future turn actually used that model. Native options retain their documented effects and permissions.

## Operation IDs

A launch or stop prints its operation identity. Preserve it after a lost response:

```sh
keelmux run --name "Backend" --workspace /absolute/project --operation op_ORIGINAL codex
keelmux stop session_EXACT --operation op_ORIGINAL_STOP
```

A launch retry must preserve the full original name, workspace, provider and native arguments. A changed request with the same operation ID conflicts. A replacement execution may make an old stop stale. Neither a new ID nor a repeated keystroke reconciles a possibly completed effect.

## Entry and companion executables

The release names are `keelmuxd` for the daemon and `keelmux-entry` for the private entry, with `bpd` and `bpg` compatibility names. Local use auto-starts its daemon. For an explicit foreground daemon, use its real `--state DIR` flag; closing that daemon is different from stopping its native sessions.

The private entry accepts:

```sh
keelmux-entry --state /absolute/entry-state --config /absolute/private-entry.json \
  --companion /absolute/path/to/keelmux
```

The configuration and scope are described in the [private entry guide](private-entry.md). Entry clients can browse and attach. Launch, stop, link and reconciliation management remain at the execution home. `drive` is an internal attachment companion with generation and acquisition arguments; it is not a public prompt API.

## Commands still on the roadmap

There are no implemented `join`, `connect`, `continue`, `subscriptions`, `usage`, daemon-shutdown, backup or semantic-message commands at this source. Proposed syntax in the product specification must not be mistaken for an available command. Structured Codex prompt and approval control remains experimental and has no product launcher.

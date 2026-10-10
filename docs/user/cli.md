# CLI reference

`keelmux` is the primary command. `bp` is the compatibility alias. The build also provides the three compatibility names alongside the canonical commands.

Global flags precede the command. Flags belonging to `run` precede the provider name. A session argument is its exact `session_…` ID from inventory or a receipt.

| Command | Behavior |
| --- | --- |
| `keelmux` | Open the local remembered switcher, starting its daemon when appropriate |
| `keelmux --state DIR` | Use the selected private state directory |
| `keelmux --state CLIENTDIR --entry SOCKET` | Connect to an explicit private entry; do not start a local daemon |
| `keelmux version` | Display application version, source commit and protocol |
| `keelmux help` | Show command help |
| `keelmux providers` | Probe native installation/version and qualified login status |
| `keelmux doctor` | Check tmux, native providers and daemon; missing prerequisites exit nonzero |
| `keelmux daemon status` | Inspect the live daemon instance and loaded build without starting one |
| `keelmux daemon stop [--instance INSTANCE]` | Stop only that live daemon; preserve native sessions |
| `keelmux daemon restart [--instance INSTANCE]` | Restart the daemon and reconcile surviving native terminals |
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

The CLI also provides `keelmux --version` and `keelmux version --json`. Version metadata identifies the application version, source commit and `bp/1` protocol; help and version return before state or daemon access. `daemon status` on absent state reports `running: false` without creating it. Use the exact `--instance` from a stop intent when retrying an uncertain stop; a replacement instance is preserved.

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

There are no implemented `join`, `connect`, `continue`, `subscriptions`, `usage`, backup or semantic-message commands at this source. Proposed syntax in the product specification must not be mistaken for an available command. Structured Codex prompt and approval control remains experimental and has no product launcher.

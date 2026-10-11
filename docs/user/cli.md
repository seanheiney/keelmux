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
| `keelmux projects --help` | Create and inspect durable local project identities; requires 0.2 |
| `keelmux ircd --help` | Manage a private IRC server, project channels and distinct stable thread bots; requires 0.2 |
| `keelmux doctor` | Check the native supervisor, providers and daemon; missing prerequisites exit nonzero |
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

## Fuller source commands

These commands are newer than the published v0.2.1 archive. Build the reviewed source using the [contributor workflow](../../CONTRIBUTING.md); the default installer continues to select the published release.

| Command | Behavior |
| --- | --- |
| `keelmux groups --help` | Nested optional company/product collections; membership, selection and removal preserve work and drafts |
| `keelmux usage --help` | Read existing local qualified observations; exact filters and explicit atomic manual import |
| `keelmux peers --help` | Explicit pinned SSH enrollment, permitted inventory, refresh, grant and revoke |
| `keelmux plugins --help` | Import verified packs and explicitly preview/apply shared skills, rules and MCP declarations |
| `keelmux profiles --help` | Create and inspect independent saved capability selections |
| `keelmux tools on\|off [--profile ID] PACK/TOOL` | Save a pinned component preference; a disabled pack remains disabled |
| `keelmux run --profile ID [--workspace PATH] gemini\|grok\|opencode [native args]` | Select an applied profile with a reviewed interactive native contract; native lifecycle qualification remains open |
| `keelmux run --operation ID` | Retry the original stored launch at its execution home without rebuilding argv or selections |
| `keelmux diagnose --json` | Structured existing-state diagnosis; no startup or repair |
| `keelmux diagnose --probe-versions` | Additionally permit bounded Codex, Claude, Gemini, Grok and OpenCode version subprocesses |

Press **u** in the source switcher to read usage for the captured host/session. **r** rereads the local ledger; **Esc** returns to the same place. See [the source feature guides](feature-expansion.md).

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

In the development source, prefer the original operation ID alone:

```sh
keelmux run --operation op_ORIGINAL
```

This loads and validates the original stored request and session generation at the local execution home. It does not reconstruct native arguments, provider version or capability selections from current configuration. A completed or failed operation is never relaunched; an attempted/uncertain operation is reconciled without blindly replaying its effect. This form accepts only `--operation ID`. Supplying a provider selects the full-payload compatibility form, which must exactly match the recorded request. The operation must already exist; a missing ID creates no intent. Launch management is unavailable through a private entry.

The full-payload compatibility form above must preserve the full original name, workspace, provider and native arguments. A changed request with the same operation ID conflicts. A replacement execution may make an old stop stale. Neither a new ID nor a repeated keystroke reconciles a possibly completed effect.

## Entry and companion executables

The release names are `keelmuxd` for the daemon and `keelmux-entry` for the private entry, with `bpd` and `bpg` compatibility names. Local use auto-starts its daemon. For an explicit foreground daemon, use its real `--state DIR` flag; closing that daemon is different from stopping its native sessions.

The private entry accepts:

```sh
keelmux-entry --state /absolute/entry-state --config /absolute/private-entry.json \
  --companion /absolute/path/to/keelmux
```

The configuration and scope are described in the [private entry guide](private-entry.md). Entry clients can browse and attach. Launch, stop, link and reconciliation management remain at the execution home. `drive` is an internal attachment companion with generation and acquisition arguments; it is not a public prompt API.

## Authored channels

The published v0.1.0 binary does not provide these commands. Keelmux 0.2 adds `keelmux channels --help`, `bind`, `list`, `history`, `send`, `delivery`, `sync`, `reconcile` and `disable`. [Channel setup and receipts](channels.md) document their exact flags and recovery behavior. These local-home commands manage authored chat only; they do not submit native model turns or approve tools.

For owned IRC, use `keelmux projects create --name NAME --workspace PATH`, preserve the returned project ID, and bind a session with `keelmux ircd bind --project PROJECT_ID --session SESSION_ID --allow-user ACCOUNT`. Threads in the same project share its registered channel and appear as distinct bots. [Managed IRC](managed-irc.md) covers addressing, credentials, retry identities and restart recovery. Project and IRC management belong to the local execution home and cannot be routed through `--entry`.

## Commands still on the roadmap

There are no implemented `join`, `connect`, `continue`, `subscriptions`, backup or semantic-message commands at this source. Proposed syntax in the product specification must not be mistaken for an available command. Structured Codex prompt and approval control remains experimental and has no product launcher.

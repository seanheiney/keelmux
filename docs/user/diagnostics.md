# Inspect a home without starting it

`keelmux diagnose` examines existing local state and reports useful next actions. It never creates missing state, repairs permissions, migrates a database, starts a daemon or IRC server, authenticates a provider, or sends model input. The command belongs to the **source feature batch after v0.2.1**; check `keelmux diagnose --help` and [feature availability](feature-expansion.md).

Use diagnostics before deciding whether a service needs attention. `doctor` follows a different workflow that can start a daemon; it is not a substitute when you need an observation without startup.

## Start with the selected home

```sh
keelmux diagnose
keelmux --state /absolute/private/keelmux diagnose
keelmux --state /absolute/private/keelmux diagnose --json
```

The human view prints one result per check, relevant counts and a suggested action. JSON includes `version`, `observed_at`, the selected `state_dir`, `checks`, optional verified owner-process details under `daemon`, and a coverage statement. A check may have `observed_at`, `freshness` and `counts` of its own.

**A successful exit means the report was produced, not that every check passed.** For automation, parse the JSON checks and decide which observations your workflow requires. The command does not turn unknown evidence into readiness.

The default path follows normal [state selection](configuration.md). An explicitly selected missing path is reported as missing without being created. Recheck that you selected the execution home you intended before considering any recovery action.

## What the report covers

| Check | What it establishes |
| --- | --- |
| `state_directory` | Existing owned private directory with safe access |
| `host_identity` | Saved host ID has the expected structure; no identity is generated |
| `client_place` | Saved navigation, drafts and pending-attachment structure can be decoded; content is omitted |
| `registry` | If safely available offline, existing database integrity and supported authority/channel/project schemas validate |
| `saved_native_runtime` or `live_registry_runtime` | Counts of saved PID/boot/process-start evidence that matches, is stale or is unavailable |
| `daemon_socket` / `daemon` | Socket availability, then bounded checks of the actual local socket peer and lifecycle instance |
| `live_registry`, `live_channels` | Bounded read-only projections from the same verified owner process |
| `channel_configuration` | Saved bindings and receipt provenance validate; no remote channel request is made |
| `ircd_configuration` | Saved IRC configuration, recorded runtime digests and release metadata validate |
| `provider_codex`, `provider_claude`, `provider_gemini`, `provider_grok`, `provider_opencode` | Executable availability on PATH by default; reported version only with an explicit probe |

Some checks are omitted when earlier prerequisites are unavailable. The absence of an optional IRC configuration is normal when you have not initialized managed IRC. Provider observations distinguish executable availability and a reported version from reviewed launch support. They do not qualify native loading or lifecycle; see [additional providers](providers.md).

Offline registry inspection requires the existing owned private authority lock. If a writer holds it, diagnostics reports `in_use` and skips offline inspection. It may still obtain a bounded live projection from an already running local daemon. It never removes a lock, invokes recovery or opens SQLite in a mode that writes sidecars. A journal/WAL/SHM file, unsupported schema, corrupt data or unsafe file prevents offline validation.

## Interpret liveness and freshness

`socket_present` means the private socket exists but the stronger process checks did not complete. It is not a running-daemon verdict. `owner_process_verified` means the kernel peer UID/PID, process lifetime and exact lifecycle instance matched on one connection and remained consistent through the bounded read. It does not prove the daemon's running executable image or authenticate its claimed execution-home identity.

Similarly, a runtime count under `matching` only means saved process-lifetime evidence matches. It does not establish native transport readiness, conversation identity or model availability. `stale` means a supported saved lifetime no longer matches. `unavailable` means required evidence is missing, unsupported or could not be checked; that alone does not prove the process died.

Saved timestamps are labeled `recent` up to five minutes, `stale` after that, `unknown` when absent, or `invalid_future` for a timestamp beyond the accepted clock tolerance. Registry event recency is not a heartbeat. A quiet healthy home can have old events, and a recent saved record can describe an unavailable runtime.

Live reads have strict time, size and representation limits. A large or incompatible response can be `unavailable` while the daemon is running. Treat that as a limit of this observation and inspect the selected home with its compatible tools; do not clear state to make the report pass.

## Request native versions explicitly

```sh
keelmux --state /absolute/private/keelmux diagnose --probe-versions
```

This permits `--version` for Codex, Claude, Gemini, Grok and OpenCode executables found on PATH, with a three-second bound per probe and bounded captured output. Without this flag, diagnostics launches no provider subprocess. A probe that fails or returns an unsupported format stays unavailable.

A reported version does not establish login, entitlement, actual model, quota or terminal readiness. The command does not run login, update, billing, native account or model operations.

## Inspect an entry client

```sh
keelmux --state /absolute/private/client \
  --entry /absolute/private/entry/control.sock diagnose --json
```

The report inspects the local client state and the entry socket's static filesystem properties. It does not connect to the entry, fetch its peers or inspect a remote provider. Run `diagnose` at the execution home to investigate its registry and daemon. [SSH peers](ssh-peers.md) provides explicit inventory-refresh commands when that is the action you want.

## Choose a recovery action

| Finding | Useful next step |
| --- | --- |
| Missing state or host identity | Verify the state path and original home. Initialize only through the intended product workflow; preserve an existing host's identity. |
| Unsafe permissions, symlink or corrupt state | Inspect ownership and the original files. Preserve recovery evidence and restore a trusted compatible backup when appropriate. |
| Registry `in_use` | Review the live daemon checks; an active authority lock is expected for a running daemon. |
| Socket present without owner-process verification | Inspect the exact socket and home daemon. Do not signal a PID from a saved file. |
| Stale or unavailable runtime evidence | Inspect the exact saved session, execution and generation at its home before an explicit reconciliation. |
| Stale channel evidence or history gaps | Review that binding's receipts and configured route. Static configuration validity does not prove delivery. |
| Incomplete or invalid IRC state | Review the interrupted initialization and original configuration before an explicit retry. |
| Missing provider or failed version probe | Inspect the native installation and PATH using that provider's supported workflow. |

When you deliberately need current lifecycle status or session recovery, use the selected state consistently:

```sh
keelmux --state /absolute/private/keelmux daemon status
keelmux --state /absolute/private/keelmux inspect SESSION_ID
keelmux --state /absolute/private/keelmux reconcile
```

These are separate product operations: `daemon status` does not start a daemon, while normal local session commands can start it and reconciliation refreshes authority records. They are not run by diagnostics. Follow [operations and recovery](operations.md) for exact-instance stops, lost acknowledgements and backups. Do not delete SQLite sidecars, authority locks or pending receipts as a shortcut.

## Share a report deliberately

Diagnostics omits native content, drafts, account credentials, channel text and raw internal errors. It still includes your selected local path and, when verified, process and lifecycle identifiers. Review the report before sharing it. Include the source commit from `keelmux version --json`, the relevant finite check results and what you expected; do not attach the private state directory wholesale.

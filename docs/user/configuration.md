# Configuration and local state

Each execution home owns its own private state and SQLite database. Keep it on a local filesystem. Another machine gets its own authority; it must not open this database over a shared filesystem.

## Select a state directory

The release selection order is:

1. Explicit `--state DIR`.
2. `KEELMUX_STATE_DIR`.
3. Compatibility `BP_STATE_DIR`.
4. Existing `~/.local/state/agent-backplane` for legacy installations.
5. Fresh-install default `~/.local/state/keelmux`.

An existing legacy path must pass the same ownership and directory checks. An invalid legacy path refuses startup instead of creating a second authority. No automatic database copy or path move occurs. Both CLI names use the same resolution rules.

Use a short absolute path for an isolated instance:

```sh
keelmux --state /absolute/private/keelmux list
```

The state directory must be a real owned directory with mode `0700`. Managed private state files and sockets use mode `0600`. Symlinked, foreign-owned or over-permissive state is refused. The control and tmux socket paths must be no longer than 100 bytes, so a deeply nested state path may be unsuitable.

## Important files

| Path beneath the selected state | Purpose |
| --- | --- |
| `host-id` | Stable execution-home identity; do not regenerate to bypass repair |
| `registry.sqlite` | Sessions, operations, journal, grants, experimental broker records; development schema v3 also contains channel bindings, authored messages and receipts |
| `place.json` | Client identity, optional groups, remembered view, drafts and pending attachment |
| `daemon.lock` | Single-daemon ownership lock |
| `control.sock` | Local control API |
| `tmux.sock` | Dedicated owned terminal backend, separate from default tmux |
| `tmux.sock.bp/` | Private backend ownership/configuration and per-execution receipts |
| `daemon.log` | Local daemon diagnostics; inspect before sharing |

The entry additionally uses its own `entry-receipts/` beneath its separate state. Socket files and lock descriptors are live locators, not portable backup objects.

`place.json`, runtime receipts and the registry can contain drafts, workspace paths, acquisition tokens or private provenance. Do not post them as routine issue attachments. Editing them directly can corrupt recovery or authority; use supported controls and retain a backup before a reviewed repair.

## Native configuration

Ordinary managed launches retain the selected CLI's native configuration and credential behavior. Keelmux does not turn an account alias into a verified identity or change authentication for you. Effective model/account/native-thread evidence may remain `unknown` even when a terminal is healthy.

The current connection helper uses `BP_*` inherited variables for compatibility. These are private launch correlation, not settings to manufacture by hand. The separate Codex broker experiment uses isolated native homes in its tests; those homes and no-auth loopback providers are fixture state, not product defaults.

## Development schema upgrade

The channel branch upgrades the registry from v0.1.0 schema v2 to v3 transactionally, preserving existing sessions, journal, grants and broker records. A declared v3 registry with missing or malformed channel authority is refused; startup does not silently recreate it.

v0.1.0 cannot open a v3 registry. Switching the installer to an older binary selects application files only; it does not downgrade the database. Before opening existing state with a channel build, take a private, consistent backup as described in [Operations](operations.md). To roll back, keep the new state intact and use a compatible pre-upgrade snapshot or a separate fresh state directory. Account for external posts after the snapshot before continuing work.

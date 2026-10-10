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

The state directory must be a real owned directory with mode `0700`. Managed private state files and sockets use mode `0600`. Symlinked, foreign-owned or over-permissive state is refused. The native execution socket adds a fixed basename beneath `native/`; its whole path must be shorter than 104 bytes. Control and compatibility socket paths must be no longer than 100 bytes, so a deeply nested state path may be unsuitable.

## Important files

| Path beneath the selected state | Purpose |
| --- | --- |
| `host-id` | Stable execution-home identity; do not regenerate to bypass repair |
| `registry.sqlite` | Sessions, operations, journal, grants, experimental broker records; schema 4 also contains channel bindings, authored messages, receipts, explicit projects and immutable thread membership |
| `place.json` | Client identity, optional groups, remembered view, drafts and pending attachment |
| `daemon.lock` | Single-daemon ownership lock |
| `control.sock` | Local control API |
| `native/` | Native supervisor private per-execution receipts, sockets and locks |
| `irc/` | Optional managed IRC configuration, private TLS/accounts, database and operation journals |
| `tmux.sock` | Optional legacy terminal backend, separate from default tmux |
| `tmux.sock.bp/` | Private backend ownership/configuration and per-execution receipts |
| `daemon.log` | Local daemon diagnostics; inspect before sharing |

The entry additionally uses its own `entry-receipts/` beneath its separate state. Socket files and lock descriptors are live locators, not portable backup objects.

`place.json`, runtime receipts and the registry can contain drafts, workspace paths, acquisition tokens or private provenance. Do not post them as routine issue attachments. Editing them directly can corrupt recovery or authority; use supported controls and retain a backup before a reviewed repair.

## Native configuration

Ordinary managed launches retain the selected CLI's native configuration and credential behavior. Keelmux does not turn an account alias into a verified identity or change authentication for you. Effective model/account/native-thread evidence may remain `unknown` even when a terminal is healthy.

The current connection helper uses `BP_*` inherited variables for compatibility. These are private launch correlation, not settings to manufacture by hand. The separate Codex broker experiment uses isolated native homes in its tests; those homes and no-auth loopback providers are fixture state, not product defaults.

## Registry schema upgrade

The 0.2 runtime upgrades older registries to schema 4. Schema 2 first gains authored-channel authority; the additive schema 3 to 4 transaction creates explicit projects and immutable thread membership, and adds exact addressed-recipient routing. Existing sessions, journal, grants and broker records are preserved. Historical binding JSON and delivery hashes remain byte-identical. A declared schema 4 registry with missing or malformed project/channel authority is refused; startup does not silently recreate it.

v0.1.0 and earlier schema 3 binaries cannot open a schema 4 registry. Switching the installer to an older binary selects application files only; it does not downgrade the database. Before opening existing state with 0.2, take a private, consistent backup as described in [Operations](operations.md). To roll back, keep the new state intact and use a compatible pre-upgrade snapshot or a separate fresh state directory. Account for external posts after the snapshot before continuing work.

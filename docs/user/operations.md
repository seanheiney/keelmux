# Operations and recovery

Closing the client leaves healthy owned native terminals running. Recovery inspects their original identity; it does not blindly relaunch them or replay input.

## Recover a session

```sh
keelmux list
keelmux inspect session_EXACT
keelmux reconcile
```

Reconciliation checks the dedicated terminal backend, exact execution/launch markers, host boot and process-start evidence. A surviving matching terminal remains the same execution. A missing terminal stays visible as missing. An unavailable or changed runtime stays in repair.

After client loss, reopen the same state. Pending attachment identity remains saved. Follow the switcher's retry or reconcile/release action; a surviving writable client must detach before another managed writer acquires input.

After a lost launch or stop response, retry only the same operation and request. **Completed**, **failed** and **outcome uncertain** are distinct. Unknown effects do not trigger automatic replay. Preserve the original execution and inspect it before taking another action.

The tested daemon-restart journey preserves surviving tmux executions. It does not prove native conversation resume after provider exit or host reboot. If the native CLI exits, keep the registry and its native history and use that provider's supported recovery workflow separately. Keelmux's product native-resume command is unfinished.

## Run the daemon in a foreground terminal

For an explicitly operated instance:

```sh
keelmuxd --state /absolute/private/keelmux
```

Stop that exact foreground daemon with Ctrl-C in its own terminal. This closes the control service; it is different from `keelmux stop SESSION`, which stops a native execution. The next local client reconciles state when starting the daemon again.

The current CLI has no daemon-shutdown command. For an auto-started daemon, identify and verify its exact state path and process before an operator sends a signal. Do not kill by program name or a stale saved PID. A supported lifecycle command is a release-onboarding improvement, not a command to invent in instructions.

## Back up state

A database backup preserves registry and journal records, not running processes, vendor credentials, repository files or complete native conversation history. Keep those in their own appropriate backups.

Use SQLite's online backup operation for a consistent database snapshot. Copying a live database file with `cp` is not the documented database backup method. The following example requires the external `sqlite3` command; replace the selected paths:

```sh
umask 077
KEELMUX_STATE=/absolute/private/keelmux
KEELMUX_BACKUP=/absolute/private/keelmux-backup
mkdir -m 700 "$KEELMUX_BACKUP"
sqlite3 -readonly "$KEELMUX_STATE/registry.sqlite" ".backup \"$KEELMUX_BACKUP/registry.sqlite\""
sqlite3 -readonly "$KEELMUX_BACKUP/registry.sqlite" 'PRAGMA quick_check;'
cp "$KEELMUX_STATE/host-id" "$KEELMUX_BACKUP/host-id"
chmod 600 "$KEELMUX_BACKUP/registry.sqlite" "$KEELMUX_BACKUP/host-id"
```

`quick_check` should return `ok`. A snapshot made during live work is consistent at its capture point; later operations are outside that snapshot.

Close the switcher before copying `place.json` to keep its saved navigation and drafts. For a full coordinated authority bundle, also preserve the private `tmux.sock.bp/` receipts and any entry receipts. Those files are not in SQLite and are captured at a separate instant during an online backup. Stop the exact owned daemon/entry through its verified lifecycle before taking a coordinated offline bundle. Copy owned regular state files and directories, excluding live sockets and lock files. Treat the whole backup as private: runtime receipts can contain launch correlation.

## Restore conservatively

Restore only while every daemon, entry and client using the target state is stopped and the destination is private and owned. Do not replace a live database. Keep the existing state as a separate recovery copy and use an application version that understands the saved schema.

Preserve the original `host-id` and runtime receipts together with the registry. Start the home daemon and run reconciliation. Surviving processes still need live exact proof; restored records cannot grant ownership over a changed PID/socket. A backup restored after reboot or on another machine restores records, not arbitrary process state or transferable native login. Cross-host state cloning and authority movement are not supported recovery shortcuts.

There is no automatic schema downgrade or merge of two authority databases. If the application's schema is newer than a previous binary supports, retain the new state and restore a compatible snapshot for rollback. Reconcile any external work since that snapshot before resuming managed control.

## Upgrade and uninstall

Keep the previous release and a private backup, read the release's schema/runtime notes, then install the chosen exact version. Preserve state selection. Updating application files does not prove a surviving daemon now runs the new version; verify the process/build and its reconciled behavior separately.

The installer contract uses an atomic `current` link and unchanged owned command links. A checksum failure leaves the existing installation available. Do not replace a custom `bp` command or edit shell profiles to hide a collision.

The planned removal command is:

```sh
curl -fsSL https://raw.githubusercontent.com/seanheiney/keelmux/main/install.sh | \
  sh -s -- --uninstall --prefix "$HOME/.local/share/keelmux" --bin-dir "$HOME/.local/bin"
```

It requires the implemented release installer described in [Installation](install.md). Uninstall retains state, drafts, native history, logins and skills. Remove matching skills separately with `skills remove`; their owned bundles remain archived. Removing a command while work runs can impair reconnection, so detach and plan daemon/client lifecycle first.

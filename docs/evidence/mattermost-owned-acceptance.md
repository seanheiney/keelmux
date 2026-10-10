# Owned Mattermost qualification

The clean source `cb828b8a588f0ce6d49f36f696de5da78b08f66b` passed an
isolated real Mattermost test through both the channel service and source-built
product daemon/CLI. The finite receipt is
[`mattermost-owned-cb828b8.json`](mattermost-owned-cb828b8.json). This receipt
qualifies that source and environment; an integrated or packaged release needs
its own exact-source run.

The harness creates a fresh labeled network, PostgreSQL container, Mattermost
container, and Linux runtime container. Every user, bot, credential, post, CA,
and local database belongs to the fixture. It reads fixture authority from owned
private regular files, refuses symlinks/FIFOs/public files/foreign ownership,
and leaves original wire bodies and diagnostics in private files. Cleanup
verified removal of all three exact containers, anonymous volumes, the network,
and explicit host secret files.

## Real service and adapter checks

All 11 checks passed against Mattermost Team Edition 11.11.1 and PostgreSQL 16:

- Verify the exact bot, team, channel, and top-level root over trusted TLS;
  reject an untrusted CA and a nested reply used as a root.
- Import allowed replies while excluding the bot, outsiders, and other roots;
  walk bounded thread pages and preserve the first authored snapshot after an
  edit. Remote edits and deletions are not projected.
- Send an explicit authored reply and verify its server acknowledgement and
  subsequent GET against the original author, route, text, delivery marker,
  and payload hash; retry the same operation without another POST.
- Forward a second actual POST and discard its actual acknowledgement. The
  delivery remains uncertain across SQLite/service reopen and retry, without
  resubmission. A changed remote text with unchanged markers cannot settle it.
  After restoring the original text and disabling the binding, read-only
  reconciliation finds the matching post without another POST.

The instrumented adapter made exactly two POSTs, deliberately dropped one
actual acknowledgement, and observed 54 verified TLS responses.

## Source daemon and CLI checks

All seven checks passed using source-built Linux arm64 `keelmux` and `keelmuxd`
through the real owned Unix daemon socket. Every CLI invocation checked the
foreground daemon's actual spawned PID and instance before and after the
command; the daemon's embedded commit matched the receipt source.

Bind/list/history/sync/send/delivery/reconcile/disable exercised the production
route and ordinary production HTTP client. The test verified one matching
server reply, preserved its receipt across an owned daemon stop/restart, retried
the original operation without a duplicate post, and refused a new intent after
disable. Both foreground processes were joined before the final verdict.

The fixture's stable session is an explicitly seeded record with no native
process. Standard `SSL_CERT_FILE`/`SSL_CERT_DIR` applied only to the Linux
children; system trust remained unchanged. Binary digests are in the receipt.

## Environment and limits

Mattermost's pinned Linux amd64 image ran under emulation on Docker Engine
29.2.1 Linux arm64. The source binaries ran in a fresh pinned Go 1.26.6 Debian
bookworm arm64 container as UID 0 with tmux 3.3a installed only in that child.
There were no host mounts. Mattermost exposed only a dynamic host loopback
port; the Linux child shared the exact Mattermost container's network namespace.
The owned ordinary bridge does not prove network-egress isolation.

The focused service test used race instrumentation; the cross-built Linux
functional test did not. Separate race tests passed for channels, store,
adapter, and service, and service vet passed. This is not packaged-release,
TUI, customer, native-terminal, model-dispatch, exactly-once-post, or database
downgrade evidence. Earlier failed harness receipts and private diagnostics
remain retained under ignored `work/`.

Run the reusable harness from a clean checkout with the three pinned images
already available:

```sh
python3 scripts/mattermost-acceptance.py --receipt work/mattermost-acceptance.json
```

The command requires Docker, Go, Python 3, OpenSSL, and public Debian package
access for tmux in the disposable Linux child. It never installs dependencies
on the host or uses an existing Mattermost server.

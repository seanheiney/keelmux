# Actual product SSH inventory fixture

`TestOwnedLoopbackSSHProductInventory` is a separate opt-in qualification of the
built `keelmux`, `keelmuxd` and `keelmux-supervisor` wiring. The older real SSH
test invokes an exporter helper with a fixture handler; this test invokes the
actual CLI and daemon over a disposable localhost SSH server.

Run it only against an explicitly identified clean source build:

```sh
KEELMUX_PEER_PRODUCT_SSH=1 \
KEELMUX_PEER_PRODUCT_BIN_DIR=/absolute/path/to/sibling/product/binaries \
KEELMUX_PEER_PRODUCT_SOURCE_SHA=FULL_40_CHARACTER_LOWERCASE_COMMIT \
go test -race ./internal/peers -run '^TestOwnedLoopbackSSHProductInventory$' -count=1 -v
```

The test logs all three binary SHA256 digests and checks embedded Go VCS settings
against that source SHA when available. The build invocation and exact source
commit remain part of the external build evidence.

It creates only owned private fixture directories, fresh SSH host/login keys,
an ephemeral loopback `sshd`, the isolated product daemon, and two managed
`/bin/sh` sessions that execute bounded sleeps. The SSH server's quoted absolute
ForceCommand runs the actual `keelmux peer-export`. Socket and grants travel
through stdin. Existing SSH configuration, native credentials and services are
not modified.

Checks include zero-session host identity, different host identity rejection,
actual CLI enrollment/list/inspect/refresh, exact granted stable session/work/
thread/lane/execution identities, hidden session exclusion, absent runtime/action
authority, wrong principal, invalid flag value privacy, grant shrink/expansion,
natural TTL staleness, wrong selected host key, stopped-home staleness, exporter
dispatch before state resolution, revocation, and no client daemon/database/
runtime or ambient state creation. Cleanup stops exact fixture session
executions, the exact daemon instance and owned SSH process; failures are
reported rather than replaying a launch.

This is same-machine built-product evidence. Independent physical second-host
enrollment and any remote installation, terminal attachment, authority movement,
model turns or customer chat writes remain outside its scope.

## Exact-source result, 2026-10-10

Actual product source: `6a6b20cd1def5e6ad0e93d4e59020a79659c89f3`, built clean
at `2026-10-10T23:28:48Z`. Captain build manifest:
`/Volumes/DataSSD1/agent-backplane/work/full-features/build.json`.
The independently hashed binaries matched that manifest:

| Binary | SHA256 |
| --- | --- |
| keelmux | `0e15a812e3ed39ffadd0bbaafb76f5cb5253924b25bfce03c62ac6aa19a7661b` |
| keelmuxd | `6b31801a5c91f61b5445663220fb37f91ab7eff670a30046ea23544749f64f1c` |
| keelmux-supervisor | `40888cc29e55cd8659e6528cab33952ae1544f2cd1bf777ede8af6228323ce70` |

The prepared harness was committed as
`3438df62ebbcbac1045bf9b7a1fb90cbc9aae4a8`; fixture SSH-key placement correction
is `e400a8f24f31ad2b02077206734c6d83723a1365`. Product binaries stayed unchanged
between the following attempts.

1. **Failed fixture attempt:** clean source and all binary hashes passed; the
   initial empty-home SSH fetch returned `ErrAuthentication`. No managed shell
   session had been launched. Exact owned daemon/SSH process and directory
   cleanup reported no errors. Receipt: owned local
   `work/product-ssh-6a6b20c.log`. The SSH artifacts were under the short canonical
   `/private/tmp` directory used for native socket state.
2. **Fixture correction:** keep daemon/native socket state short, and put only
   SSH keys, authorized_keys and SSH configuration into a separate canonical
   owned ambient temporary directory. OpenSSH StrictModes and all selected
   host-key checks stayed enabled. This was a test-only path correction; no
   production or existing native SSH configuration changed.
3. **Passed actual product attempt:** the corrected opt-in race test passed in
   3.53 seconds (package 4.920 seconds), against the exact same product source
   and hashes. Receipt: owned local `work/product-ssh-6a6b20c-owned-keys.log`.
   All checks described above ran, including empty-home identity, two actual
   managed shell sessions, all six CLI operations, wrong principal/key/host,
   malformed-argument privacy, TTL/stopped-home stale states, grant revocation
   and absence without state/daemon/runtime creation.

The passing command was:

```sh
KEELMUX_PEER_PRODUCT_SSH=1 \
KEELMUX_PEER_PRODUCT_BIN_DIR=/Volumes/DataSSD1/agent-backplane/work/full-features/bin \
KEELMUX_PEER_PRODUCT_SOURCE_SHA=6a6b20cd1def5e6ad0e93d4e59020a79659c89f3 \
go test -race ./internal/peers -run '^TestOwnedLoopbackSSHProductInventory$' -count=1 -v
```

Cleanup stopped both exact fixture session executions, requested shutdown of
the exact daemon instance and observed its clean process exit, terminated the
owned SSH process, and removed both owned temporary directories without error.
The endpoint was absent after daemon shutdown; subsequent export/refresh did
not recreate it. Client inventory operations created only the peer store.

This result qualifies **same-machine actual product SSH inventory** on Darwin
arm64. Independent physical second-host proof remains unqualified. Model turns,
customer chat writes, remote installation/service mutation and existing native
credential changes were zero.

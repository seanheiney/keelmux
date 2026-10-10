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

## Result

Awaiting the captain's exact committed source build. No completed product-level
SSH qualification is claimed by preparing this harness.

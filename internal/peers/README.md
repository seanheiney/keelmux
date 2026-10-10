# Explicit SSH inventory backend

This package adds a read-only enrollment/discovery backend to the existing
Host/Work/Thread/session model and `federation.Grant`. Product CLI, daemon and TUI
wiring belong to the captain. The implementation base is
`71d831f05405b68fc80cb38132b321f3ba1e6cc2`.

## Wiring contract

- Mount `HomeHandler(hostID, source)` at `GET /v1/peer-inventory` on the existing
  private home socket. The source's `List` must honor context cancellation. This
  endpoint reads the home registry and returns its exact Host identity even for
  zero sessions. It does not reconcile or invoke a terminal backend.
- Before any CLI state creation or daemon startup, a fixed `peer-export` verb
  calls `Export(ctx, stdin, stdout)`. Reject extra flags/args. The exporter reads
  one strict `ExportRequest`, contacts only its already running Unix socket, and
  applies the supplied directional session grant before any SSH output.
- `ReadConfig` accepts an owned, regular mode-0600 file. `Open` uses only an
  explicitly selected private peer store; `Enroll` verifies the configured SSH
  key and existing product Host identity before persisting enrollment.
- `SetGrant` and `Revoke` are durable local scope changes. `Refresh`, `View` and
  `Directory` re-read current grants, including other processes' changes.
  Revocation during a fetch discards the late result. Directory uses one final
  grant snapshot, including earlier homes revoked during another home's fetch.
- `Source` implements the existing `InventorySource` only. It returns fresh
  permitted sessions to the private entry; stale/unavailable/revoked views
  return an error. It never implements `StreamSource` or attaches a terminal.

`Config` requires the existing `host_id`, an SSH `alias`, an absolute
`known_hosts_file`, one explicitly selected `SHA256:` fingerprint, absolute
`remote_executable` and `remote_socket`, the existing `federation.Grant`, and
explicit freshness/retention limits. Optional `known_host` selects the exact
known_hosts hostname or `[host]:port` when the SSH alias differs; hashed entries
are supported. Optional `ssh_config_file` selects a reviewed owned native SSH
configuration. Omit it to reuse the user's ordinary SSH alias configuration.

The existing untagged `federation.Grant` currently serializes its JSON fields as
`Principal`, `HostID`, `Sessions`; no shared schema was changed in this lane.

## Scope and truthfulness

OpenSSH receives one temporary mode-0600 known-host record containing only the
selected public key. BatchMode and StrictHostKeyChecking are mandatory. Global
trust files, DNS host-key trust, host-key updates, connection sharing, agent/X11
forwarding, configured RemoteCommand and local commands are disabled. Host key
negotiation is restricted to the selected key algorithm. Native SSH login keys
stay local. The command is a correctly shell-quoted absolute executable plus
the fixed `peer-export` verb; socket and grant are sent solely through stdin.
No key scanning, password prompts, remote install, daemon auto-start or service
configuration occurs.

Views distinguish `available`, `stale`, `unavailable`, and `revoked`. `available`
means a fresh registry observation from the pinned home; it does not establish
live process health. Failures retain a still-permitted prior view as visibly
stale with a qualified reason. Expired views hide their sessions/workspaces.
New host identity, changed stable Work/Thread/Lane identities, older execution
generations, reused generations and older timestamps cannot replace newer
evidence. Freshness expires at the earlier remote/local observation deadline;
future clock skew and local rollback cannot extend readiness.

Workspace discovery derives exact paths only from permitted session records
and reports `recorded`. Existence, Git state and destination readiness are not
probed. Runtime locators and all terminal/dispatch/stop/resume capabilities are
removed from every returned and persisted observation. Grant expansion never
resurrects records outside the prior permitted cache. Peer membership conveys
no transitive grant, process movement, home authority transfer or failover.

Bounds: 16 enrolled homes, 256 sessions per home/grant, 64 KiB request, 1 MiB
snapshot, 8 KiB SSH stderr, three-second fetch/shared directory budget and a
200 ms process pipe-cleanup limit. Freshness is 1–300 seconds; stale-view
retention is at most one day. Certificates, wildcard trust and automatic key
rotation/replacement are unsupported. The selected key's `@revoked` marker
always rejects that key. Explicit re-enrollment after revocation requires the
same reviewed route/key pin and another successful identity check.

The home socket and SSH account authenticate the same-user owner. This is an
owner-managed private inventory path, not an independent remote-principal login
or a sandbox against that same OS user's own terminals/sockets. The exporter
applies the reviewed local directional grant; an independent peer-side grant
authority is a later gate.

## Verification

```sh
KEELMUX_PEER_REAL_SSH=1 go test -race ./internal/peers ./internal/federation ./internal/config
go vet ./internal/peers ./internal/federation ./internal/config
```

The opt-in loopback test creates disposable native SSH host/login keys and an
owned localhost sshd. A real `/usr/bin/ssh` connection invokes the exporter over
an existing owned Unix home socket, verifies stable identities and the exact
session grant, suppresses an ambient configured remote command, and rejects a
wrong selected key. It changes no existing SSH configuration, service, model
login or running user terminal. The ordinary tests separately cover host-key
selection/revocation, hashed host names, shell/option injection, malformed,
duplicate, transitive and oversized envelopes, timeout, absence without state
creation, stale/expired/changed identity, generation replay, scope changes,
cross-process revocation during fetch, private-file corruption and partial
directory collection.

The loopback proof is same-machine transport evidence. Independent physical
second-server enrollment, production remote attachments, remote installation,
execution movement, authority failover, provider/model turns and customer chat
writes were not performed or qualified by this lane.

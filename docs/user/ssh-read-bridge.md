# Observe an enrolled home through one entry

Development source adds an explicit read-only terminal bridge to an existing SSH
enrollment. Both the entry and home need this compatible source build. Published
v0.2.1 does not include these commands. Runtime qualification is recorded
separately; configuring a route does not prove a native provider is attachable.

The home owns its execution, login, native permissions and terminal size. This
route can show a permitted terminal. Input, resize, launch, stop, model changes
and automatic resume are unavailable through it.

## Prerequisites

First [enroll the home's inventory](ssh-peers.md) using a reviewed native SSH
alias and selected known-host key. Its daemon must already be running. Keep the
same exact principal, HostID and session IDs. A visible session needs all three
allowlists: enrollment, entry and home export policy. Observation additionally
needs the home's current native read-only attachment capability.

Use the canonical absolute enrollment directory at the entry. The read route
requires its existing private record and lock; removing or replacing that lock
or directory closes the incarnation. It does not recreate missing enrollment
authority. A fresh entry incarnation is explicit, and shutdown reports an
unconfirmed owned SSH cleanup as an error.

The bridge is for an owner-operated SSH account. Its principal is a policy
label; it does not authenticate separate team members or isolate the OS user.
For a dedicated exporter account, a reviewed fixed OpenSSH ForceCommand can
restrict dispatch. Keelmux does not edit sshd or install a service automatically.

## Provision a home policy

At the home, create an owned mode-0700 directory using its canonical absolute
path. Save a desired policy as an owned mode-0600 JSON file. Replace every example
ID and path below, including a fresh export scope ID; do not reuse another home's
scope or authority record.

```json
{
  "version": "keelmux-peer-bridge/1",
  "scope_id": "scope_00000000000000000000000000000000",
  "host_id": "host_11111111111111111111111111111111",
  "principal": "owner",
  "socket": "/home/operator/private/keelmux/control.sock",
  "revision": 1,
  "enabled": true,
  "sessions": [
    {
      "session_id": "session_22222222222222222222222222222222",
      "inventory": true,
      "terminal_read": true
    }
  ]
}
```

Use an existing owned mode-0600 daemon socket inside its owned mode-0700 directory.
The socket path, host, principal and scope remain immutable for this policy.

```sh
keelmux peer-policy provision \
  --file /home/operator/private/export-desired.json \
  /home/operator/private/export-authority.json
keelmux peer-policy inspect /home/operator/private/export-authority.json
```

Provision refuses existing authority state. It creates a private aggregate policy
and operation journal, plus locks. `inspect` returns policy only, excluding lease
receipts. Exporter startup requires all provisioned files and the existing home;
it never repairs missing state or starts a daemon.

The SSH exporter command is the fixed absolute executable plus
`peer-read-bridge /home/operator/private/export-authority.json`. Do not forward
the home's general control socket or accept arbitrary caller-selected commands.
Keelmux uses strict pinned host-key checking, batch login, no forwarded agent,
X11, ports or sockets, no SSH multiplex reuse and no allocated terminal.

## Select the route at the entry

Add `terminal_read` to the enrolled peer in the existing private entry JSON:

```json
{
  "principal": "owner",
  "peers": [
    {
      "host_id": "host_11111111111111111111111111111111",
      "inventory_store": "/Users/operator/private/client/peers",
      "terminal_read": {
        "policy_path": "/home/operator/private/export-authority.json",
        "scope_id": "scope_00000000000000000000000000000000"
      }
    }
  ],
  "grants": [
    {
      "host_id": "host_11111111111111111111111111111111",
      "sessions": ["session_22222222222222222222222222222222"]
    }
  ]
}
```

Each peer chooses exactly one direct socket or enrolled inventory store. Only an
inventory-store peer can select this scoped SSH bridge. Without `terminal_read`,
its existing inventory-only behavior remains unchanged.

```sh
keelmux-entry --state /Users/operator/private/entry-state \
  --config /Users/operator/private/entry.json
```

In another terminal, open that entry's switcher:

```sh
keelmux --entry /Users/operator/private/entry-state/control.sock
```

Select a permitted observer-only row and press Enter. The footer says **Enter
observe**; `v` also observes. A saved intent records the exact read-only choice,
execution generation, client and operation. Only a live matching Ready handshake
opens the terminal; an inventory row or saved receipt cannot substitute for it.

## Update or revoke

Create the next desired JSON with the same immutable bindings, a higher revision
and the changed explicit session actions. To revoke the whole policy, set
`enabled` to false. Compare the current revision on update:

```sh
keelmux peer-policy update \
  --file /home/operator/private/export-next.json \
  --expect-revision 1 \
  /home/operator/private/export-authority.json
```

Every policy revision closes the previous stream incarnation, including quiet
terminals. A revoke followed by regrant cannot revive it. The home serializes
Ready, inventory, acquisition and output admission with the current policy;
already admitted bytes cannot be recalled. Its watcher checks quiet streams at
100 ms intervals plus bounded outstanding IO. Local enrollment changes likewise
invalidate the entry's SSH incarnation.

## Recover a disconnect

Native work remains at the home when the observer or entry disconnects. The
exporter closes only its owned attachment clients. The original acquisition and
once-only open-attempt record survive restart. Repeating that operation cannot
open a second native client, even when its acknowledgement was lost.

Confirm exact cleanup before explicitly requesting a new observation with a new
operation. Cleanup of a durably confirmed original lease can remain valid after
navigation revocation, but cannot bypass a replaced SSH key or execution
generation. An unknown acquisition without a confirmed token needs home repair;
it cannot be reconstructed with a new Attach request under revoked scope.

The journal has finite lifetime capacity and preserves uncertain records. Keep
the private authority and its provisioned `.lock` and `.serve` files together
with the home backup, after stopping the entry/exporter and home. These two
bridge files are an exception to the general advice to exclude live process
locks: an offline restore requires the original owned private files. Do not
reprovision over the retained journal. Do not delete them to
clear an error, reuse an attempted operation ID, copy lease capabilities between
homes or replace execution identity. The [architecture contract](../architecture/0004-bouncer-read-bridge.md)
describes limits and the distinct two-server/native-provider acceptance gates.

This source also upgrades opened home registries to schema 5 for empty captain
metadata. Before using it with an existing home, take an offline backup following
[Operations](operations.md). Published v0.2.1 cannot read schema 5; rollback needs
compatible application bytes and a matching offline state backup. This guide
does not add a captain model runtime.

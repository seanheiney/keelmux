# Discover an existing SSH execution home

SSH peer enrollment brings a permitted inventory from an existing execution home into a private local directory. It can also supply inventory to `keelmux-entry`. These commands and the `inventory_store` entry configuration belong to the **source feature batch after v0.2.1**; both the local client and the remote exporter need a compatible source build. See [feature availability](feature-expansion.md).

Default SSH enrollment exposes session and workspace metadata only. An explicit home-owned policy and entry selector can opt into the separate [SSH read bridge](ssh-read-bridge.md) in development source. Inventory enrollment alone does not attach a terminal, send input, stop work, install a remote service, transfer execution authority or resume a conversation. An SSH-discovered workspace is a recorded path, not proof that the directory currently exists or is ready for work.

## Prepare one home

Start with a home you already operate through native SSH login. Review its existing SSH alias and selected host key. The Keelmux daemon at that home must already be running; enrollment never starts it.

At the home, obtain its stable host ID and exact permitted session IDs from its existing Keelmux inventory or saved receipts. `keelmux list` follows the normal local workflow and may start a daemon; use it only when that is intended. Record the absolute path to the compatible remote `keelmux` executable and that home's existing `control.sock`. Do not regenerate `host-id` or copy another home's registry.

On the client, select an existing known-host record and verify its SHA256 fingerprint through your trusted host-administration process. For example, the local read-only command below lists fingerprints in an existing file; selecting the right one remains your responsibility:

```sh
ssh-keygen -lf /Users/operator/.ssh/known_hosts -E sha256
```

Enrollment will not discover or accept a new key automatically. The known-host file must be owned, regular and not writable by group or others. Exact names and hashed known-host entries are supported; wildcard trust, host certificates and automatic key rotation are not.

## Save a reviewed private configuration

In an owned private directory, set `umask 077` and save `/Users/operator/private/studio-peer.json` with the following shape. Replace **every** example ID, path, alias and fingerprint with your selected values. JSON paths must be absolute and do not expand `~`.

```json
{
  "host_id": "host_00000000000000000000000000000000",
  "alias": "studio",
  "known_hosts_file": "/Users/operator/.ssh/known_hosts",
  "known_host": "studio.example",
  "fingerprint": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  "remote_executable": "/home/operator/.local/bin/keelmux",
  "remote_socket": "/home/operator/.local/state/keelmux/control.sock",
  "grant": {
    "Principal": "owner",
    "HostID": "host_00000000000000000000000000000000",
    "Sessions": ["session_11111111111111111111111111111111"]
  },
  "fresh_for_seconds": 30,
  "retain_for_seconds": 300
}
```

The capitalized `Principal`, `HostID` and `Sessions` keys are intentional. `grant.HostID` must match `host_id`. `Principal` is your exact local policy label, not a new SSH account or independent login identity. An explicit list of session IDs grants only that inventory; there is no wildcard grant.

`alias` selects the existing native SSH configuration. `known_host` selects the exact known-host name, or `[host]:port`, when it differs from that alias; omit it when they match. Optional `ssh_config_file` selects a reviewed absolute native SSH configuration file. Omit it to use ordinary SSH alias configuration. Keep native SSH login keys in their existing native setup, never in this JSON.

Freshness must be 1–300 seconds. Retention must be at least freshness and at most 86,400 seconds. Use a short absolute remote socket path, at most 100 bytes. The existing remote socket must be owned and mode `0600`, inside its owned mode-`0700` directory.

```sh
chmod 600 /Users/operator/private/studio-peer.json
keelmux --state /Users/operator/private/keelmux-client peers enroll \
  --config /Users/operator/private/studio-peer.json
```

Enrollment verifies the selected key and the returned product host identity before saving the enrollment. It uses noninteractive SSH with strict host-key checking and bounded output/time. It does not prompt for passwords, forward your agent, update known-host files or run configured remote/local commands. Native SSH login must already work under these restrictions.

The local store is `/Users/operator/private/keelmux-client/peers`. Enrollment may create this private client state. Do not reuse an execution home's database as a peer store.

## Browse and refresh

```sh
keelmux --state /Users/operator/private/keelmux-client peers list --principal owner
keelmux --state /Users/operator/private/keelmux-client peers list --principal owner --refresh
keelmux --state /Users/operator/private/keelmux-client peers inspect --principal owner HOST_ID
keelmux --state /Users/operator/private/keelmux-client peers refresh --principal owner HOST_ID
```

Replace `HOST_ID` with the enrolled ID. Flags precede it. All commands return JSON. `list` and `inspect` read the permitted cache; `--refresh` or `refresh` performs SSH discovery. On an absent peer store, `list` returns an empty directory and `inspect` reports that enrollment is needed. Neither creates an enrollment or contacts an arbitrary home. Fetches have a three-second budget; a directory refresh shares that budget across its homes.

| State | Meaning and next action |
| --- | --- |
| `available` | Inventory is within its freshness window. It is still a registry observation, not live terminal health. |
| `stale` | Still-permitted cached records are retained, but freshness expired or a fetch failed. Review `reason` and refresh after correcting the route. |
| `unavailable` | No usable current cache, an expired cache or another failed prerequisite. Sessions and workspaces may be hidden. |
| `revoked` | No current inventory grant. Refresh cannot restore revoked scope. |

An outage on one home does not grant access through another. Discovery rejects changed host identity, older execution generations and older observations rather than replacing newer evidence. Correct local and remote clocks before relying on freshness after a clock-related error.

## Change scope or revoke

```sh
keelmux --state /Users/operator/private/keelmux-client peers grant \
  --principal owner --sessions SESSION_ID,ANOTHER_SESSION_ID HOST_ID
keelmux --state /Users/operator/private/keelmux-client peers refresh --principal owner HOST_ID
```

`grant` replaces the session list. Keep the list comma-separated, without spaces, and use exact distinct IDs. A grant change invalidates freshness; expansion does not resurrect discarded records. Refresh is required to see newly permitted records.

Remove all scope while retaining the enrollment, or revoke the enrollment:

```sh
keelmux --state /Users/operator/private/keelmux-client peers grant \
  --principal owner --sessions "" HOST_ID
keelmux --state /Users/operator/private/keelmux-client peers revoke --principal owner HOST_ID
```

Revocation durably discards cached records and prevents further fetches until explicit re-enrollment. To restore a revoked enrollment, review and enroll the same pinned route/key configuration again; another successful identity check is required. Changing an active grant uses `grant`, not re-enrollment. Pin or route replacement has no automatic rotation workflow in this batch; preserve the existing record for deliberate review.

## Show the inventory through a private entry

After enrollment, save a separate owned mode-`0600` entry configuration, for example `/Users/operator/private/entry.json`:

```json
{
  "principal": "owner",
  "peers": [
    {
      "host_id": "host_00000000000000000000000000000000",
      "inventory_store": "/Users/operator/private/keelmux-client/peers"
    }
  ],
  "grants": [
    {
      "host_id": "host_00000000000000000000000000000000",
      "sessions": ["session_11111111111111111111111111111111"]
    }
  ]
}
```

Use the same principal and host. A session must be allowed by both the enrollment and the entry grant. Each peer selects exactly one `inventory_store` or direct `socket`, never both. The inventory store must already exist and contain that enrollment; entry startup does not enroll a host.

Start the entry explicitly in one terminal, then connect in another:

```sh
chmod 600 /Users/operator/private/entry.json
keelmux-entry --state /Users/operator/private/entry-state \
  --config /Users/operator/private/entry.json
```

```sh
keelmux --state /Users/operator/private/switcher \
  --entry /Users/operator/private/entry-state/control.sock
```

The entry's `keelmux` companion defaults beside the entry executable; [private entry](private-entry.md) covers explicit companion selection. Entry inventory requests refresh enrolled sources. Only fresh permitted sessions are returned from this source, with attachment, stop and semantic-input capabilities removed. Use `peers inspect` for its detailed stale-cache reason if the entry cannot display it.

Peer grant and revoke changes are reread from the shared local peer store. Entry configuration grants apply when that entry starts, so restart its own foreground process after editing that configuration. A busy peer transaction should be retried; do not delete lock files.

## Troubleshoot without widening access

For `host_key_rejected`, verify the selected exact record and fingerprint; do not disable strict checking. For `host_identity_changed`, inspect the execution home's original state before changing any pin. For `authentication_unavailable`, repair native SSH login separately. For timeout or transport failure, inspect the existing remote executable, compatible build, running daemon and socket permissions; refresh never installs or starts them.

Peer inventory can contain private session names and workspace paths. Keep its store and JSON output private. Enrollment authenticates the owner-managed SSH route and home socket, with a local directional session grant. It is not an independent remote-principal authorization service or a sandbox against the same OS user.

Source tests include real same-machine SSH transport coverage. That does not qualify an independent physical second server, remote terminal attachment or execution movement. Consult the [support matrix](support-matrix.md) before depending on a broader deployment claim.

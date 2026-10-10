# Private entry experiment

The recorded real-provider entry journeys qualify the historical tmux runtime across two homes on one computer. A newly qualified v0.2.1 native-provider gateway journey and independently authenticated second server remain pending; see the [support matrix](support-matrix.md).

A private entry exposes one permitted inventory and routes native terminal attachments to explicit execution homes. Each home retains its database, native terminals and credentials. The client companion runs on the client machine.

The tested experiment uses two homes on one physical Mac. It does not certify two independent servers, SSH enrollment, public listeners or a production remote-access service. Use [the support matrix](support-matrix.md) for that boundary.

## Select exact homes and sessions

At each home, read `keelmux list` and record its exact `host_id` and permitted session IDs. Prepare a configuration in a private location:

```json
{
  "principal": "local",
  "peers": [
    {"host_id": "host_EXACT_A", "socket": "/absolute/home-a/control.sock"},
    {"host_id": "host_EXACT_B", "socket": "/absolute/home-b/control.sock"}
  ],
  "grants": [
    {"host_id": "host_EXACT_A", "sessions": ["session_EXACT_A"]},
    {"host_id": "host_EXACT_B", "sessions": ["session_EXACT_B"]}
  ]
}
```

Replace every example ID and path. An empty session grant grants no inventory or attachment. The JSON must be one owned regular file, mode `0600`, at most 64 KiB. Peer sockets must be clean absolute Unix paths.

## Start the entry and connect

```sh
chmod 600 /absolute/private-entry.json
keelmux-entry --state /absolute/entry-state --config /absolute/private-entry.json \
  --companion /absolute/path/to/keelmux
```

From the client terminal:

```sh
keelmux --state /absolute/client-state --entry /absolute/entry-state/control.sock
keelmux --state /absolute/client-state --entry /absolute/entry-state/control.sock directory
```

Use the same client state when returning so exact acquisition recovery remains available. The entry scrubs home runtime locators from permitted inventory. Launch, stop, link and reconciliation management stay at the execution home.

## Availability and grants

Entry loss and home loss are distinct. An unreachable home is shown separately while healthy homes remain usable. Directory collection has a shared three-second home deadline; it returns healthy partial inventory rather than treating silence as a stopped process.

Configuration changes apply on entry restart. Live executable configuration reload is not implemented. Restart only this owned entry; preserve its private receipt state so a lost acquisition acknowledgement can reconcile with the original home authority.

The configuration binds one principal through a private local file. It is not a team login system or automatic federation enrollment. An independently authenticated SSH-forwarded socket is a possible transport to test later, rather than a verified two-server setup recipe.

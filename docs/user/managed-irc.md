# Managed IRC

The development build can provision and supervise its own pinned Ergo 2.19.1 server, register a stable channel for a Keelmux thread, and connect that channel to the authored communications inbox. This feature is under release qualification; v0.1.0 does not include it. Use [external channels](channels.md) for Mattermost, Slack or an existing IRC server.

The server is optional. It uses verified TLS on loopback, a private CA and private account files. Initialization downloads a checksum-pinned official archive for your supported platform. Ordinary session launch and daemon startup do not download or start an IRC server. Native PTY/process supervision owns the server lifetime; tmux is unnecessary.

## Provision and start

```sh
keelmux ircd init
keelmux ircd start
keelmux ircd status
```

Initialization chooses separate available loopback IRC/API ports. To request fixed ports, use `ircd init --irc-port PORT --api-port PORT`. A listener collision is refused. Existing owned configuration is reused; the manager cannot adopt a foreign listener or replace its configuration.

`start` prints its durable operation ID before contacting the home. Save that ID if the response is lost and retry with `ircd start --operation ORIGINAL_ID`. Repeated operations inspect the existing execution. An unknown supervisor or launch acknowledgement remains uncertain and prevents a replacement server.

`status` reports the exact execution instance, verified running version, endpoint and observation time. It excludes credential paths and bytes. `init` and `account` deliberately return local connection paths so you can configure your client.

## Connect a thread

Find the session ID with `keelmux list`. Create a services account for your IRC client with `keelmux ircd account ACCOUNT`; omitting the name selects the owned bot. Account credentials are stored in a private local file. Use that file and the returned CA path in a local IRC client that supports SASL and verified TLS. Keep the password in the client's credential store.

```sh
keelmux ircd account alice
keelmux ircd bind --session SESSION_ID --allow-user alice
```

Each thread gets a deterministic `#keelmux-…` channel derived from its full stable thread identity. Registration is confirmed through the authenticated server API before a binding is accepted. The exact server services account is the sender identity; changing a nickname does not change that identity. Repeat `--allow-user` for each permitted account.

Bind prints both the immutable binding ID and registered-channel operation ID before its request. If the response is lost, preserve the same IDs, session and allowlist:

```sh
keelmux ircd bind --session SESSION_ID --allow-user alice \
  --binding ORIGINAL_BINDING_ID --operation ORIGINAL_CHANNEL_OPERATION_ID
```

An already attempted channel operation only confirms registration read-only. A lost registration reply cannot authorize another JOIN/REGISTER. If an attempt was consumed before authentication or connection succeeded, the original record can require explicit repair. Retain its IDs and private state; do not delete the journal to force a retry.

## Read and reply

Use `keelmux channels list`, `channels history`, `channels sync` and `channels send` as described in the [channel guide](channels.md). In the switcher, select a session and press **c** to open its authored inbox; choose a binding with **b** and compose an explicit reply with **p**.

Incoming notes remain text. They cannot press Enter, approve tools, submit a model turn or route terminal keystrokes. Replies select one immutable binding and never fan out automatically. The selected native CLI remains the model interaction surface.

IRC intake starts at activation and observes live messages. Reconnect and bounded-buffer gaps remain visible; it does not promise complete server history. Missing or uncorrelated echoes leave replies uncertain. Read-only receipt reconciliation can find matching retained evidence, but cannot resend automatically.

## Stop or replace the server

Use the exact `instance` from the current status:

```sh
keelmux ircd stop --instance EXECUTION_ID
keelmux ircd restart --instance EXECUTION_ID
```

Restart prints a fresh durable operation ID. Preserve that ID and the original expected instance after a lost response. A stale instance cannot stop its replacement. Accounts and registered channels remain in the owned server database across a verified server restart; live chat connections can have a history gap.

Closing the switcher or restarting the control daemon preserves a surviving supervised server. Host reboot, lost supervisor authority, foreign listeners and malformed state require explicit recovery. Stop cannot kill an arbitrary process found on the same port.

## Local configuration and coverage

Owned configuration, TLS material, accounts, registration journals and Ergo's database live beneath the selected home at `irc/`. Keep this directory private and include its regular files in a coordinated backup. Native supervisor receipts live at `native/`; live sockets and lock descriptors are not portable backup objects. See [configuration](configuration.md) and [operations](operations.md).

The first real acceptance uses a pinned official Ergo runtime, verified TLS/SASL, actual registration, concurrent connections, lost replies and persistence across native control-process loss. Final CLI, authored-message and packaged-runtime qualification is recorded per source/artifact in the [support matrix](support-matrix.md). Public network deployment, server linking, multi-tenant access and model dispatch are separate capabilities and remain unfinished.

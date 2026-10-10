# Architecture and trust

Keelmux is a per-user terminal bouncer. Its client locates work and attaches; the home daemon owns durable registry and cooperating input authority; the terminal backend owns the native process and PTY. The vendor CLI remains authoritative for its conversation, model, login and permission behavior.

```mermaid
flowchart LR
  Client[Keelmux switcher] --> Home[Private home daemon]
  Entry[Optional private entry] --> Home
  Client --> Entry
  Home --> State[Local SQLite registry and journal]
  Home --> Channels[Authored channel service]
  Channels --> Chat[Mattermost / Slack / IRC]
  Home --> Terminal[Independent native PTY supervisor]
  Home --> Process[Native process supervisor]
  Process --> IRC[Optional managed Ergo]
  Terminal --> CLI[Real native CLI]
```

New development executions use the bundled native supervisor. Existing tmux executions retain an optional compatibility adapter; there is no fallback launch through tmux. Published v0.1.0 is the historical tmux release. The [runtime decision](../architecture/0002-native-runtime-ownership.md) describes exact ownership, query handling and recovery boundaries.

The entry routes permitted access to the home. It does not move the process, merge credentials or become that home's execution authority. Current entries use explicit private sockets and configuration; independent-server enrollment remains unfinished.

## Authored channels

The channel service uses the same stable host, work and thread identities, with a separate authored-message sequence and delivery journal. It has no terminal-input or model-submit dependency. A note that contains a shell command, approval or prompt remains text. Each outbound reply selects one immutable binding; there is no automatic fanout between channels attached to a thread.

A binding records the verified provider endpoint, realm, authenticated self, channel/root, allowlist and activation cutoff. A credential reference identifies an owned private file; credential bytes are read only by the transport adapter and never enter the registry or public status. Disabling a binding prevents new claims, waits for its admitted attempt to settle, and retains historical receipts. Changing scope requires a new binding ID.

SQLite commits an authored message and queued delivery before transport IO. A serialized binding gate claims that delivery once before its one send attempt. Only matching remote identity, address, text and correlation can establish delivery. Timeout, malformed acknowledgement or process loss leaves the effect uncertain. Restart converts stranded attempted deliveries to uncertain; read-only reconciliation may find an existing post, but cannot authorize another send. Correlation metadata is evidence for reconciliation, not a server-side exactly-once guarantee.

Incoming first-note snapshots and their cursor are committed atomically. Source identity includes provider endpoint, realm, channel and opaque remote ID. Overlap and edits cannot create another authored note or replace its original text. Actor, root and original creation time must remain consistent. Provider history is bounded; failed polls retain the last successful observation, and IRC connection gaps are explicit. Intake freshness describes observation, independently of delivery receipts and model activity.

The TUI writes the exact reply intent to private client state before its queue request. A lost local acknowledgement survives quit/restart with the same message, binding, thread and text. The restored client queries that receipt before any explicit retry. Client state and database versions reject older readers once these schemas are written. See [channel operation](channels.md) and [upgrade recovery](operations.md#upgrade-and-uninstall).

## Stable identity

| Identity | Meaning |
| --- | --- |
| Host | Execution-home authority |
| Work | Stable work identity |
| Shared thread | Stable product communication/history reference |
| Lane | Stable participant/recipient |
| Session | Navigable context for work, thread, lane and workspace |
| Execution and generation | Current run attempt and target binding |
| Native thread | Provider's own conversation, separately observed where supported |
| Runtime locator | Socket, terminal session/pane, PID, host boot and process start |

A matching directory or PID is insufficient to establish an old run. Inherited launch correlation and live owned runtime evidence are checked against the durable product identity. Ordinary terminal capability does not imply semantic prompt, approval or native history access.

## Intent before effect

The home persists an operation before attempting runtime effects. Outcomes distinguish recorded, attempted, completed, failed and uncertain. The same operation/request returns its known receipt; a conflicting reuse is refused. Reconciliation inspects exact external evidence. Missing acknowledgement is not permission to replay a launch, stop or keystroke.

The serialized service gate and existing `grants` table enforce one cooperating managed writer. A read-only client observes without owning input/size. Release verifies the exact acquisition and detached writer; stale client, token or execution generation is refused. A pending acquisition survives client loss so recovery can refer to its original authority.

## Enforcement boundary

This is cooperative control among managed clients, not an OS security sandbox. The same user can reach its own terminal socket, files and native programs outside Keelmux. Separate worktrees do not isolate ports, databases, credentials or arbitrary file writes. Native tool permissions remain native.

Credentials stay in their selected native realm. Discovery probes qualified status without copying credential stores. An account label, configured model or successful process launch is not verified effective account/model identity. Unknown values remain unknown.

Inventory, drafts, runtime receipts and logs can contain private paths or operational correlation. Federation permission, group membership and navigation are distinct: putting a session in a company group does not grant another user or peer access.

## Recovery boundaries

Client detach preserves surviving terminals. Daemon restart reconciles surviving owned processes. Provider/runtime restart and host reboot require separately tested native resume as new attempts. Missing workspace or uncertain external work remains a repair state. Restored screen/history is not proof of a live process or continued model work.

The experimental Codex broker further separates a persistent authoritative app-server from disposable native UI processes. Its grants, receipts and final output gate share the home authority. Unknown replies/callbacks are blockers and unsupported methods are denied. That internal architecture has no product launcher and does not add semantic capability to the released terminal route. See its [contract and prerequisites](../research/CODEX_BROKER_CONTRACT.md).

Keelmux's product interface stays terminal-first. TUI/native terminal, later shared chat and scoped tools use the same durable identities. A documentation site is reference material; a product web dashboard is outside the design.

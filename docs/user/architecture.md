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

New 0.2 executions use the bundled native supervisor. Existing tmux executions retain an optional compatibility adapter; there is no fallback launch through tmux. Published v0.1.0 is the historical tmux release. The [runtime decision](../architecture/0002-native-runtime-ownership.md) describes exact ownership, query handling and recovery boundaries.

| Runtime path | Ownership and recovery |
| --- | --- |
| Native 0.2 | A per-execution supervisor owns the process and terminal lifetime independently of the switcher and control daemon. Reconnect uses a bounded parsed screen snapshot; restoration is qualified per terminal/provider, not a complete native conversation history. |
| Existing legacy tmux | The recorded tmux adapter remains responsible for that execution. Compatibility is optional, and native-launch failure cannot create a tmux replacement. |
| Unknown or unavailable | Retain the durable identity and inspect its original authority. Missing supervisor evidence does not establish child exit; uncertainty prevents an automatic replacement. |

The switcher's selected runtime line reads the recorded backend and execution state from inventory. Offline observations are marked stale. It does not discover ownership from terminal text or infer account/model identity from attachment. [Daily use](daily-use.md#selected-runtime) describes this compact view; `keelmux inspect` exposes the underlying qualified evidence.

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

The saved acquisition stays bound to its original session, execution, generation, operation and client. Moving the switcher selection or observing a new generation cannot retarget it. Its recovery footer names the saved destination; retry and reconcile/release use the stored request, with tokens kept private. Native input is never replayed by this recovery. See [saved attachment recovery](daily-use.md#recover-a-saved-attachment).

## Enforcement boundary

This is cooperative control among managed clients, not an OS security sandbox. The same user can reach its own terminal socket, files and native programs outside Keelmux. Separate worktrees do not isolate ports, databases, credentials or arbitrary file writes. Native tool permissions remain native.

Credentials stay in their selected native realm. Discovery probes qualified status without copying credential stores. An account label, configured model or successful process launch is not verified effective account/model identity. Unknown values remain unknown.

Inventory, drafts, runtime receipts and logs can contain private paths or operational correlation. Federation permission, group membership and navigation are distinct: putting a session in a company group does not grant another user or peer access.

## Recovery boundaries

Client detach preserves surviving terminals. Daemon restart reconciles surviving owned processes. Supervisor loss, provider/runtime restart and host reboot are distinct recovery boundaries; any native resume requires its own tested new attempt. Missing workspace or uncertain external work remains a repair state. Restored screen/history is not proof of a live process or continued model work.

The optional [managed IRC service](managed-irc.md) uses the same native process supervision, with its own pinned Ergo runtime, private local configuration, account/channel operations and exact-instance lifecycle. Server ownership, an authored-message receipt and a model turn remain separate facts. Its actual command guide is generated into the reference site directly from the canonical Markdown; publication and packaged qualification follow the [support matrix](support-matrix.md).

The experimental Codex broker further separates a persistent authoritative app-server from disposable native UI processes. Its grants, receipts and final output gate share the home authority. Unknown replies/callbacks are blockers and unsupported methods are denied. That internal architecture has no product launcher and does not add semantic capability to the released terminal route. See its [contract and prerequisites](../research/CODEX_BROKER_CONTRACT.md).

Keelmux's product interface stays terminal-first. TUI/native terminal, later shared chat and scoped tools use the same durable identities. A documentation site is reference material; a product web dashboard is outside the design.

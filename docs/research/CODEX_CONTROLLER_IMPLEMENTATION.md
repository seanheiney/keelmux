# Bounded Codex primary lifecycle implementation

2026-10-10. Experimental source library in `internal/codexcontroller`, pinned to
Codex protocol 0.159.0. Product wiring remains unavailable. This contribution
implements the durable lifecycle coordinator and its fault tests; it does not
implement or certify the authenticated native `Upstream` adapter or the shared
`Service` lifecycle gate. Neither dependency has a permissive default.

## Public host-internal contract

`OpenJournal(privateDirectory)` creates or opens the lifecycle ledger.
`New(journal, supervisor, upstream, gate, options)` refuses missing or typed-nil
dependencies. Trusted options identify the reviewed native executable and its
SHA-256, native HOME/CODEX_HOME paths, and authorized realm revision. No credential
file is read or copied by the controller. The executable is hashed before spawn.
Both native home directories must already exist and belong to the effective
local user. Their symlinks are resolved, their opened directory descriptors are
validated, and their canonical paths plus filesystem device/inode identities are
persisted. Lifecycle and binding operations recheck those identities. A realm
revision change or an alternate path to the same native home cannot bypass the
live native-thread fence. These checks do not isolate the same local OS user.
The caller must qualify that digest against the installed 0.159.0 CLI beforehand;
the controller does not treat an arbitrary requested version string as evidence.

| Method | Implemented behavior |
| --- | --- |
| `Start(ctx, StartRequest)` | One foreground app-server launch and one exact existing-thread resume via the injected verified upstream. Persist intent and attempt before each effect. Same operation/target returns its durable state, without replay. |
| `Inspect(ctx, executionID)` | Durable last-observed state only, with timestamp. It does not claim fresh liveness. |
| `Reconcile(ctx, executionID)` | Exact owned supervisor lookup and read-only upstream evidence. No launch, resume, turn, grant or UI action. |
| `Binding(ctx, Target)` | Fresh verified host-internal `codexbroker.AgentBinding`, requiring exact current identity. Does not acquire writer authority. |
| `VerifyAgent(ctx, AgentBinding)` | Primary portion of `codexbroker.Verifier`, using immutable journal identity and fresh owned process/wire evidence. |
| `Stop(ctx, StopRequest)` | Under the common gate, require exact incarnation, no primary writable attachment and authoritative idle/empty queue/background evidence. Persist stop attempt before the bounded exact-process stop. Confirm death separately. |

There is no method for native UI creation, native keys, prompts, model turns,
approval decisions, config/auth modification, thread discovery or thread creation.
The existing broker must still validate and acquire the single `store.grants`
writer row and verify each native/typed connection. `VerifyAgent` alone is not a
complete `codexbroker.Verifier` and intentionally does not register a writer.

## Shared authority integration required

The captain-approved narrow extension is:

```go
WithPrimaryLifecycle(context.Context, LifecycleRequest, func(context.Context) error) error
```

`LifecycleRequest` binds action, operation, host/session/execution/generation,
native thread and authorized realm revision; stop also supplies the original
complete app-server binding. It must be implemented on the existing Service/store
authority. A separate lock beside the broker is insufficient.

Lock order is **common Service gate → controller journal → bounded supervisor or
wire operation**. The controller snapshots stop routing data without retaining
its journal mutex, enters the gate, then revalidates that snapshot. It never calls
the gate while holding its journal lock. `VerifyAgent`/`Binding` never enter the
common gate. Injected supervisor/upstream methods must not reenter that gate.

Start must validate current managed execution and trusted exact-thread/realm
authorization. Stop must reject durable pending/uncertain effects, callbacks,
admitted/queued work and attached native writers, and fence admission before
invoking the controller callback. The same gate remains held through the bounded
effect. Errors or uncertain effects retain that fence and writer reservation.
Controller process death does not retire a broker grant, resolve RPC receipts,
advance an execution generation or create another writer. Grant retirement remains
an explicit existing-authority operation after its own exact evidence.

## Persistence and recovery

The host-private journal uses a versioned, bounded ledger (1,024 primaries / 4 MiB),
mode 0700 directory, mode 0600 records, exclusive nonblocking flock, fsync of a
temporary file, atomic rename and parent-directory fsync. The flock prevents two
processes from concurrently owning this file; it is not an input lease. No broker
grant table or `one_writer` constraint is duplicated. History/tombstones are not
automatically deleted; reaching the bound refuses new records.

Journal file opens use no-follow/nonblocking flags and validate the actual opened
descriptor's owner, mode, regular-file type and single-link count. Reads and atomic
writes are relative to the opened directory descriptor; a changed directory or
lock-file identity refuses further operations. Version 2 accepts only the exact
canonical JSON representation emitted by its writer, rejecting duplicate keys,
case aliases, unknown fields and null/reset ambiguity. Earlier version 1 ledgers
fail closed without deletion or automatic migration; retained attempts must not
be bypassed by creating fresh state.

Each record preserves original start request, randomized app-server incarnation,
private per-launch capability, binary/realm references, exact supervisor process
reference, observed socket identity and stop operation. Public `View` omits private
paths, capability, process IDs, account/model inference and broker tokens.
`Launch` refuses JSON serialization and redacts ordinary Go string formatting.
The opaque `Journal` handle also refuses JSON serialization and redacts ordinary
Go string formatting, including formatting that would otherwise expose private
map entries.

State transitions are `recorded → attempted → process_owned → resume_attempted →
ready`, and `ready → stop_attempted → stopped`. Any invoked effect with unknown
acknowledgement becomes `outcome_uncertain`; a failed final journal commit leaves
the preceding durable attempted record and poisons the open handle. Reopening
never repeats launch/resume/stop. Read-only reconciliation may recover an exact
live anchor; pending stop stays uncertain until exact process death is observed.
An absent supervisor receipt or unavailable helper is unknown, never an absence
proof. An unexpected primary exit stays uncertain. A different execution cannot
replace a non-stopped primary for the same session or same realm/native thread.

`Journal.Close` only releases file ownership. A native UI detach is outside this
module and cannot stop the primary. A verified stop can close its anchor connection
without claiming anything about historical broker receipts or model completion.

## Native supervisor and wire boundary

At base `4c265651855478c16408173852beb2d2fbca0704`, `nativepty.Backend` exposes the
required `LaunchProcess`, `Lookup`, `Inspect`, and `Stop` shape. The fixed argument
array requests a foreground app-server, an owned Unix listener, capability-token
authentication and a per-launch SHA-256 token digest. The plaintext capability
stays in private state and the trusted upstream adapter; it is not an argument or
environment variable. Native supervisor capability and app-server capability are
different values and prove different connections.

The required `Upstream` implementation must authenticate the exact recorded
app-server child on the actual connection using its per-launch capability, private
socket owner/mode/inode, kernel peer PID, boot/process-start and owned supervisor
receipt. Listener existence, socket pathname, version text and native helper
authentication are insufficient. A launch alias may only resolve to a proved
owned native endpoint; no shared native parent may be changed. It must account for
the executable launch race rather than assuming an earlier path hash proves the
running image. Any unsupported platform/transport combination fails closed.

`Resume` must retain an authoritative anchor and allow only the pinned reviewed
initialize/initialized/exact-thread resume sequence. It must preserve native
configuration and never use global history/config/account discovery as identity.
`Inspect` may reconnect read-only, but must never resume/start work. Evidence must
prove the exact loaded thread, full empty queue and full empty background-terminal
result (including pagination); truncated or unknown results cannot assert empty.
Callback/notification handling must use the existing broker quarantine/policy
contract. The injected adapter cannot silently answer a callback or drop a blocker.
Fresh native projection endpoints and trusted writer/connection registration remain
separate captain-owned integration work.

## Sources and qualification

Read-only installed CLI inspection reported `codex-cli 0.159.0` and generated its
experimental JSON schema. Local help lists `--listen unix://PATH`, `--ws-auth
capability-token`, and `--ws-token-sha256`. The schema requires exact `threadId` for
resume/read/queue/background requests; it exposes many optional resume overrides
that this controller does not expose. Help/schema availability is not proof that
Unix capability authentication and native projection attachment work together.

[Official OpenAI app-server documentation](https://learn.chatgpt.com/docs/app-server)
describes initialization and native conversation lifecycle. The installed schema
and repository's observed 0.159.0 evidence pin this implementation's narrower
contract. Existing [controlled native helper evidence](../evidence/Codex-broker-native-2026-10-10.md)
uses isolated pregrant legacy fixtures and does not prove this product controller,
its capability-token transport, daemon restart or shared lifecycle gate.

Tests cover durable attempts before effects, duplicate/conflicting operations,
lost launch/resume/stop acknowledgements, restart without replay, wrong native
thread/host/session/generation/incarnation/PID/boot/start/capability/socket evidence,
unknown helper state, live-stop uncertainty, common gate ordering, private journal
ownership, changed executable digest, redacted views and independent primary
lifetime. These use explicit injected fixtures. No native app-server, native UI,
model turn, auth/config/trust modification, real approval, provider contact, remote
host or production process was exercised in this contribution.

Before product enablement: implement/review the common gate; implement/prove
capability-token native wire and exact realm/thread provenance; reproduce actual
isolated zero-turn process/restart/anchor/projection behavior on named macOS/Linux
versions; then qualify separately authorized real model-turn and approval cases.

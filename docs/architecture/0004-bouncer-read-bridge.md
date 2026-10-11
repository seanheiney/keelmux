# Scoped Bouncer read bridge, version 1

Status: frozen implementation contract, based on `e6bbe42f0fb2bb6ea927e6e4b14807bbc8742be9`.
Plane #6 tracks the bridge; #17 tracks the separate advice-captain ledger. This
contract is not runtime qualification. The full Bouncer milestone still requires
two independent servers, addressed communication, scoped control, real captains,
usage/privacy and recovery evidence.

## Authority

SSH inventory enrollment remains inventory-only. Entry configuration may opt in
to one explicit `terminal_read` selector containing an absolute home policy path
and exact export scope ID. Enrollment grant, home policy and native acquisition
are distinct authorities. The selected SSH account is an owner-managed trust
boundary; a principal label does not authenticate a team member or sandbox its
OS user. No raw home socket forwarding or generic remote HTTP dispatch exists.

The home owner explicitly provisions one owned mode-0600 authority record in a
canonical owned mode-0700 directory. Provisioning creates its bounded private
operation journal and lock files; serving and reading never create missing
state. Policy and journal share one atomic durable record. Policy version is
`keelmux-peer-bridge/1`, with exact scope, home HostID, principal, immutable home
socket, monotonically advancing revision, enabled flag and at most 256 explicit
session entries. Each entry independently grants inventory and terminal-read.
No wildcard, inferred group, transitive source or writable action is permitted.

Updates compare the expected current revision, preserve immutable bindings and
advance revision. A stream captures its original revision; revoke followed by
regrant never revives it. Serialized policy admission, durable effect intent and
Ready/output publication observe the same authority revision. A fresh check
precedes each published frame; a bounded watcher cancels quiet/pending streams.
The barrier must not span native opening. Missing/corrupt/replaced policy,
lock, private home socket or endpoint identity fails closed. Socket owner, mode,
canonical directory and endpoint identity are checked before and after use.
The actual home inventory identifies HostID even when it has no sessions.

## Frozen package seams

`internal/peerbridge` owns the fixed wire protocol, home policy/journal, exporter
and typed client. It does not import `internal/peers`, expose `protocol.Client`,
start/reconcile a daemon or invoke a native/model login.

```go
const Version = "keelmux-peer-bridge/1"
type Selector struct { ScopeID, HostID domain.ID; Principal string }
type SessionActions struct { SessionID domain.ID; Inventory, TerminalRead bool }
type ReadPolicy struct {
    Version string
    ScopeID, HostID domain.ID
    Principal, Socket string
    Revision uint64
    Enabled bool
    Sessions []SessionActions
}
type PolicyStamp struct { ScopeID, HostID domain.ID; Principal string; Revision uint64 }
func ReadPolicyFile(path string) (ReadPolicy, error)
func ProvisionPolicy(ctx context.Context, path string, policy ReadPolicy) error
func UpdatePolicy(ctx context.Context, path string, expectedRevision uint64, next ReadPolicy) error
func Serve(ctx context.Context, authorityPath string, input io.ReadCloser, output io.WriteCloser) error
func NewClient(conn io.ReadWriteCloser, selector Selector) (*Client, error)
func (*Client) Hello(context.Context) (PolicyStamp, error)
func (*Client) List(context.Context) ([]domain.Session, error)
func (*Client) Attach(context.Context, protocol.LeaseRequest) (domain.AttachSpec, error)
func (*Client) Open(context.Context, transport.Request) (*transport.Stream, error)
func (*Client) Release(context.Context, protocol.LeaseRequest) error
func (*Client) Close() error
```

The exact JSON tags are lower snake case and each fixed schema rejects unknown,
duplicate/aliased fields, invalid UTF-8, extra JSON values and oversized bodies.
The bridge sends only finite Hello/List/AcquireReadOnly/OpenReadOnly/ReleaseExact/
CloseStream requests. Frame direction is enforced independently of caller
mutable receipts. Client stream frames permit only an empty Close; server stream
frames permit actual Ready, Output, bounded Failure and empty Close.

Root owns the narrow `transport.ReadOnlyStream(ctx, net.Conn, Request)` seam.
It owns its connection, requires an actual exact first Ready frame, excludes
lease tokens and preserves a private immutable observer fence even if the public
Receipt is changed. It never constructs a stream from a historical receipt.

`internal/peers` owns `ReadSelection{PolicyPath string; ScopeID domain.ID}` and
`NewReadSource(*Registry, domain.ID, string, ReadSelection)`, exposing only the
existing List/Attach/Open/Release plus Close. It binds the selected existing
enrollment and its revision, grants, exact pinned SSH key and fixed exporter
command. Permission revision changes permanently cancel the current incarnation.
It never changes the inventory cache or advertises a writer capability.

## Durable effects and cleanup

Before home Attach, persist the exact principal/scope/host/session/execution/
generation/operation/client/read-only intent. Persist the returned exact private
lease before acknowledging it. Public receipts omit socket paths, argv, lease
and native/SSH credentials. Mark `OpenAttempted` durably before home Open; there
is at most one attempted native opening for that operation across exporter loss
or restart. Only an actual matching Ready confirms opening. Historical Ready
never grants a new stream. No automatic reconnect, attachment replay, generation
substitution, prompt replay, resume, execution movement or failover occurs.

An acquire-attempt record without a confirmed token after process loss is
uncertain and needs repair. Existing Attach is acquire-or-return, not a safe
lookup: it must not be used to reconcile unknown acquisition after revocation.
A durably confirmed original receipt can be recovered for exact cleanup after
navigation revocation. Release rejects caller-imported records, changed tuples,
changed identity and substituted tokens. Release outcomes are persisted;
confirmed cleanup does not erase the attempted-operation tombstone. Home
execution continues when the observer disconnects. Cleanup closes/reaps only
owned streams/SSH/native attach clients and never uses arbitrary PID fallback.
A changed/revoked SSH key or old execution generation leaves cleanup retained
for repair, rather than bypassing trust or inventing a new route.

## Bounds and operational behavior

Maximum 16 in-flight calls, 8 streams, 64 KiB control bodies, 1 MiB inventory,
32 KiB terminal frames, 4 queued frames per stream, 1 MiB aggregate output queue,
3 second control/handshake deadlines and 5 second partial-frame/write deadlines.
Quiet terminals remain legal. A slow viewer closes its own subchannel while
other viewers/control continue; shared network loss closes the incarnation.
Retained stderr is bounded and classified, never exposed raw. The bounded
journal refuses capacity before effects and never evicts uncertain records.
Policy checks and revocation polling are bounded to 100 ms plus already admitted
bounded IO; no frame is admitted after the committed revocation barrier.

SSH retains BatchMode, strict selected host-key checking, no automatic key
updates, agent/X11/port/socket forwarding, ControlMaster, remote/local command
from configuration or allocated TTY. Only fixed quoted absolute reviewed
executable + `peer-read-bridge` + owner-selected absolute policy path is sent.
Native OpenSSH manages authentication; model credentials remain at the home.

## Acceptance evidence

Tests cover ownership/symlink/replacement, exact zero-session HostID, scope and
principal mismatches, malformed/duplicate/oversized wire, all forbidden actions,
revoke during blocked acquisition/open and quiet/output streams, grant ABA,
loss before/after durable acquisition and OpenAttempted, uncertain cleanup,
stalled viewers, bounds, cancellation and owned process/FD cleanup.

A subsequent owned same-machine SSH proof must run built product entry, daemon,
supervisor and a finite native PTY child using fresh private keys and a fixed
ForceCommand. Record exact source/binary/config, actual Unicode/ANSI output,
Ready identity, zero forbidden input/resize, loss/no-replay, explicit new operation
after cleanup, revocation, missing daemon/no creation and cleanup. This proves
SSH plumbing; independent physical servers and native provider journeys require
separate evidence and remain outside that claim.

# Unified home actor source evidence

2026-10-10. Source-only zero-turn actor slice, based on published contracts
`fd412b320a4ea28dc99d96f202e9a42afb78e74a` and the Deliver amendment
`0b1daf8`. The commit containing this note implements only `internal/store/`
and `internal/daemon/`; the final commit identity is reported in the handoff.

On one physical macOS arm64 computer, these checks passed:

- `go test -race ./internal/store ./internal/daemon -count=1`
- `go vet ./internal/store ./internal/daemon`
- `git diff --check`

The new tests contain 18 daemon actor groups and 6 store groups. Fixtures use
private temporary SQLite files, registered home provenance, controlled senders
and probes, and owned FIFO/directory fixtures. They execute no vendor CLI,
model turn, external host, network mutation or user-home configuration write.

The tests establish shared-slot native/semantic contention in both directions,
concurrent acquisition and duplicate dispatch, committed Attempted before send,
no database transaction across the send, cancellation/deadline release of the
common gate, lost/partial acknowledgement and restart without replay, computed
hash and provenance collisions, typed channel/request identity, same-principal
new epochs with immutable old endpoints/processes, historical settlement without
current-owner projection, read delivery versus revoke, coherent quiescence
requirements, callback registration versus handover, unscoped/foreign/large
callback quarantine, projection loss without primary release, and fencing while
primary or optional adapter dependencies are unavailable.

Store tests establish transactional v1-to-v2 migration preserving exact legacy
lease/operation/payload/event bytes without inventing broker provenance, rollback
on migration failure, future-version refusal, an attempted-receipt reopen boundary,
historical generation settlement, refusal to heal damaged established v2 metadata,
and bounded rejection of special authority/database files. Existing v2 requires
the expected table/column/primary-key shape, exact unique writer predicate and
matching broker grant/agent/epoch/RPC receipt metadata. Losing grant metadata
cannot authorize legacy release. Directory creation uses the existing supported
platform parent-sync helper; SQLite uses FULL synchronous transactions.

The home controller must still prove real app-server and native launch ownership,
map owned upstream channels to immutable bindings, quarantine unmatched replies,
and supply coherent ordered quiescence evidence. Sender, probe and downstream
delivery adapters must honor the supplied bounded contexts. No actual native
Unix relay, process capability verification, authenticated model work, approval
decision, Linux runtime, host power loss or filesystem durability under power
failure is proved by these fixtures.

This actor advertises no semantic capability and adds no HTTP, protocol or command
wiring. Native projection exit and generic terminal stop/release cannot retire a
broker primary. Broker primary stop/restart is not implemented here; only verified
idle handover retires a writer. Privileged callbacks remain non-actionable repair
blockers, and model mutations and decisions are refused.

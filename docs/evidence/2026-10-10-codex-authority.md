# Experimental Codex authority source evidence

2026-10-10. Initial actor implementation `efb0111eb068f11555eb70a72e7fe8eff4a802c8`, integrated as `b65688986c93c8a58f0e8cde9dc1fb5e0f7f407f`. The source build, full repository race run and vet passed at that integration. An ignored standalone review test initially made `go test ./...` fail outside the product packages; its unchanged repro was preserved under a `.go.txt` filename before the full check passed.

**Independent review qualification:** that source pass did not establish acceptance. Review reproduced a callback registration gap before handover commit and an inconsistent master reservation flag permitting replacement while an old receipt remained uncertain. Their repairs and independent regressions are pending. Counted relay review also reproduced malformed source disposal without a durable blocker; a relay repair is present but requires the actor's non-actionable missing-ID quarantine support. No live native broker journey has passed yet.

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
requirements, callback registration during the quiescence probe, unscoped/foreign/large
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

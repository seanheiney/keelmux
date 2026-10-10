# Experimental Codex authority source evidence

2026-10-10. Initial actor implementation `efb0111eb068f11555eb70a72e7fe8eff4a802c8`, integrated as `b65688986c93c8a58f0e8cde9dc1fb5e0f7f407f`. The source build, full repository race run and vet passed at that integration. An ignored standalone review test initially made `go test ./...` fail outside the product packages; its unchanged repro was preserved under a `.go.txt` filename before the full check passed.

**Independent review qualification:** the initial source pass did not establish acceptance. Review reproduced a callback registration gap before handover commit and an inconsistent master reservation flag permitting replacement while an old receipt remained uncertain. Repairs integrated at `416b33cdae9f92a3d7a4353c33bc3ecc9df2ca5d` passed both barrier orderings, corrupted reservation refusal, session-wide unresolved-effect refusal and absent-ID quarantine regressions. The independent authority reviewer reran focused daemon races ten times and store corruption/migration cases five times, including the original lost-reservation reproduction; no remaining actionable authority finding was reported. Counted relay acceptance now covers durable malformed-source quarantine and original raw-byte digests. The controlled real zero-turn native journey passed; it remains a test helper, not product controller wiring. [Native proof](Codex-broker-native-2026-10-10.md).

On one physical macOS arm64 computer, these checks passed:

- `go test -race ./internal/store ./internal/daemon -count=1`
- `go vet ./internal/store ./internal/daemon`
- `git diff --check`

The tests now contain 19 daemon actor groups and 9 store groups. Fixtures use
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

The bounded authority repair based on `efb0111` adds a registration barrier
between the final handover callback check and its store commit. Deterministic
fixtures hold the existing store mutex at the independent review's boundary:
registration before the barrier prevents retirement; a reader waiting behind
the commit records its original source afterward and fences the replacement.
The test does not wait for registration while the commit barrier is held.

Established v2 now requires master and broker Active values to agree, with
Retired exactly inactive and Admitting/Fenced active. Five damaged-state fixtures
(including the lost master reservation and inverse retired-active case) refuse
reopen with repair required and preserve original master, metadata, operation,
payload and unresolved receipt bytes. New broker and legacy writer acquisitions
also refuse unresolved receipts or callbacks across the entire session, even
after a coherent retired row or a replacement execution projection. Exact
historical settlement clears only its own receipt blocker.

Malformed owned callbacks may omit their native request ID; no ID is fabricated.
They retain digest quarantine and remain unattributed, unresolved and without
decision/display authority across retry and restart. Store admission and v2
schema validation refuse absent-ID records with actionable metadata. JSON null
is the persisted representation of an absent raw ID. The zero-turn decision
profile remains unsupported.

These focused regressions were also run with an ignored overlay restoring only
the `efb0111` production files. The original source failed the commit-gap,
reservation/coherence, unresolved acquisition and absent-ID quarantine checks;
the repaired source passed. Final scoped race/vet and whitespace checks passed
with Go 1.26.6 on Darwin arm64. No root relay or shared contract was changed by
this repair.

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

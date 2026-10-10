# Experimental Codex controller source evidence

Clean source `01af8032bcbc019328c79e592982f06e41b4829d` passed
`go test -race -json ./internal/codexcontroller -count=1` on Darwin 25.6.0 arm64,
Go 1.26.6: 17 top-level groups, 47 passing test nodes, zero failures.
`go vet ./internal/codexcontroller` and `git diff --check` also passed.
The [machine-readable receipt](codex-controller-2026-10-10.json) fingerprints every
controller source file and the retained local test log.

Tests use explicit injected supervisor/upstream/common-gate fixtures. They prove
durable ordering, lifecycle deduplication, read-only recovery, identity fencing,
private record handling and refusal behavior at this module's boundaries. They
do not prove a real native app-server or the existing broker authority integration.
The [implementation contract](../research/CODEX_CONTROLLER_IMPLEMENTATION.md)
documents the exact required shared gate and authenticated upstream adapter.

Only installed version/help/schema inspection ran against Codex 0.159.0. No native
app-server or UI was launched, no provider contact or model turn occurred, and no
configuration, authentication, permission, trust or production process changed.

Product wiring remains blocked on the common Service lifecycle adapter and a real
verified capability-token upstream adapter. Existing helper bootstrap evidence
does not satisfy those gates. Actual daemon restart, native projection attach and
detach, writer handover, account/model attribution, model work, approvals, Linux
runtime, packaging and public release remain outside this result.

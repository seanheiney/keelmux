# Explicit usage ledger

This package is a durable backend unit for Keelmux. It accepts already qualified
structured observations; it contains no provider adapters, transcript parser,
terminal scraper, login, billing mutation, remote calls, or CLI/TUI integration.
The daemon should open one private host-local `usage.db` beside its existing
registry and close it with the daemon. It must never share this database between
servers. There are no new dependencies.

## Ingestion contract

`Open(cleanAbsolutePath)` creates owned mode-0600 regular database and lock files
in an owned mode-0700 directory, following the existing private-directory pattern.
It refuses symlink database/lock files, public permissions, nonregular files, a second cooperating
authority, unknown schemas, and malformed or changed stored observations. SQLite
DELETE journaling and FULL synchronous commits persist before `Ingest` returns.
The integrity digest detects accidental data changes; it is not a signature and
does not defend against another same-user program rewriting the database.

`Ingest(ctx, Observation)` accepts exactly one `Attempt`, `Money`, or `Quota`.
All references must be nonsecret, bounded, opaque identifiers. The caller is
responsible for adapter trust, original-source authenticity, and attribution:
the package does not independently verify a provider's assertion. Do not expose
an unrestricted ingestion endpoint that lets an untrusted client label its own
payload `provider_verified` or `adapter_observed`.

- `Source.ID` names the original stream, with immutable source kind and quality.
  Preserve it and `Observation.ID` on peer replication. An identical replay is
  idempotent even after restart. A changed payload or a reassigned original
  stream/key/sequence fails with `ErrConflict`. Stream sequence is adapter-owned,
  scoped to each canonical key, and must advance for subsequent snapshots.
- An attempt key contains provider, opaque qualified namespace, native request
  ID, and native attempt ID. Only the adapter can establish its namespace and
  stable native identifiers. Missing identifiers are unsupported here: report
  missing coverage rather than guessing a key from a terminal, timestamp, model,
  account alias, prompt, or process. A peer copy enriches the same key.
- Logical request IDs are globally stable product request references. Native
  thread/turn IDs, actual and requested model, execution, host, harness/version,
  account/subscription, retry-of, and parent/child stay separate. Parent and retry
  references must share the qualified provider namespace. Their referenced
  attempt may arrive later. They are never used to synthesize another call.
- `manual` sources always remain `user_entered`. They contribute declared calls,
  not observed provider calls or actual models used. Actual charges require
  provider-verified evidence. User entries cannot establish a shared quota.
- Each token array is a cumulative snapshot for one attempt, not a delta to add.
  The latest nonempty claim per original source enriches prior partial snapshots.
  Explicitly unavailable token, money, and quota measurements supersede old
  measurements from that stream; they cannot restore old capacity or amounts.
  Trusted final snapshots supersede live snapshots. Conflicting current final
  sources produce unknown counts and retained conflicting measurements.
  `IncludedIn` records native subset relations: cache included in input, reasoning
  included in output, or input/output included in total must not be added again.
  The package emits dimensions separately and never synthesizes a universal
  token sum. Cycles, inconsistent subset counts, and arithmetic overflow fail
  closed. Missing parent dimensions stay missing.
- Money has a separate provider/namespace/record-ID identity, so a subscription
  commitment is not replicated per call. Six distinct kinds are actual charge,
  estimate, commitment, credit, top-up, and token-equivalent value. Nonnegative
  exact decimals support 18 integer and 9 fractional digits. Annual/monthly/weekly
  commitments keep their explicit cadence; no monthly equivalent, token price,
  currency conversion, net spend, or invoice completeness is inferred. Refunds
  should arrive as explicit credit records rather than negative charges.
- Quota keys preserve the provider bucket and native window ID. Shared verified
  account/organisation/subscription buckets deduplicate across hosts while units,
  dimensions, limits, resets, and windows remain separate. Observations are not
  reservations, and do not enforce consumption by unrelated native clients.

Limits are 10,000 observations per ledger, 256 per canonical record, 64 KiB per
observation, 32 token dimensions, and 16 quota dimensions. `ErrCapacity` requires
an explicit future retention/rotation policy; the package never silently deletes
evidence. `ErrCorrupt` requires repair; it never opens a fresh empty ledger over
damaged history. Read-side validation repeats on each projection.

## Projection contract

`Project(ctx, Query)` reads a consistent local snapshot. Start is inclusive and
End exclusive. The query provides absolute timestamps, a validated display
timezone, current time, and a bounded freshness age. Source evidence dated in
the future is stale. Timezone does not silently reinterpret the requested UTC
interval or generate daylight-saving-sensitive calendar boundaries.

The caller must supply authorization after checking current product grants.
Empty authorization denies all records. `AllContexts` or explicit host/session
allowlists scope observation reads before enrichment; account detail and money
detail are independent grants. Account/subscription filters require the account
grant. These are projection controls, not same-user security isolation. Use
opaque namespaces/source IDs, since they appear as evidence references even
when account labels are redacted.

`ReadProjection(ctx, path, query)` validates the query even when the ledger does
not exist and returns an empty unknown view plus `present=false`. An existing
ledger is opened with SQLite read-only mode and projected from one read
transaction. No directory, writer lock, migration, chmod, journal, or other
state is created. Unsafe directories/files and changed schemas are refused.

`ValidateBatch` checks 1–128 structured observations before any state is opened,
including conflicting identities or stream sequences within the batch.
`IngestBatch` shares the single-record transaction helper and either commits the
entire batch or returns no results and rolls back all inserts. A final-record
conflict preserves the original database bytes and projection. Unavailable
current tool counts and reset times supersede old known measurements; references
with invalid UTF-8 are rejected rather than normalized into another identity.

Filters cover federation, host, project, task, thread, lane, session, execution,
harness/version, provider, account/subscription, and trusted actual model. Money
supports model filtering when the source supplies `Money.ActualModel`; it does
not infer a model from a requested alias. Quota dimensions remain native, so an
actual-model usage filter does not assert a matching quota entitlement.

Output distinguishes logical requests, native turns, provider attempts, declared
calls, retries, children, outcomes, tool calls, requested aliases, and distinct
actual models. `ModelTotals` and token totals preserve provider/model dimensions.
Unknown actual models remain their own group. Native turn identity includes
provider namespace and native thread. Counts of missing logical/turn/model/tool
attribution accompany measured counts. There is no inferred active-session or
available-model inventory: root must join a qualified current runtime/provider
inventory to add those views.

Token and money `Observed*` totals sum the known subset only. Their unknown and
conflicted counts must be displayed beside them; an absent total is not zero.
Money totals remain separate by kind, currency, and cadence. Quota views represent
current windows at `Now`, not spend within the usage period. Conflicting quota
claims remain in `Fields` with nil chosen values; stale state cannot establish
route readiness. Conflicting money classification/cadence cannot enter an
arbitrary total.

`Coverage.Scope` and money total coverage deliberately stay `unknown`: observing
some requests or invoice lines does not prove a complete provider-wide period
ledger. Per-record coverage is the source's explicit claim, not independently
verified completeness. `ExpectedSources` exposes sources absent from the selected
authorized view. Sources and field freshness, partial/unknown/conflicted/stale
record counts, and original observations persist independently of transcripts.

## Verification and remaining proof

The package tests exercise duplicate replay and concurrent ingestion, restart
recovery, reordered snapshots, conservative attribution/token conflicts, retries
and child calls, native inclusion semantics, exact multi-currency money, annual
commitments, shared quota windows, stale/future evidence, permission projections,
malformed payloads, bounded records, private files, and corruption refusal.

Run `go test -race -count=1 ./internal/usage` and `go vet ./internal/usage`.
Fixture ingestion establishes backend behavior only. Real provider observation,
version/authentication matrices, native runtime attribution, quota freshness,
invoice reconciliation, federation authenticity, daemon lifecycle integration,
and CLI/TUI behavior remain unimplemented or unproved in this isolated unit.

## Local command integration

`cmd/bp/usage.go` exports `usageCommand(stateDir, entry, args)`. Root must dispatch
it before `config.Ensure` or daemon startup. Its testable `usageRun` accepts
explicit input/output streams and a clock. The command rejects remote entry and
performs no socket, native RPC, provider, login, or model action.

```text
keelmux --state /private/state usage
keelmux --state /private/state usage view --host host_ID --session session_ID --provider codex --json
keelmux --state /private/state usage --from 2026-10-10T00:00:00Z --to 2026-10-11T00:00:00Z --timezone America/New_York --money
keelmux --state /private/state usage import observations.json
keelmux --state /private/state usage import - < observations.json
```

Bare `usage` shows the previous 24 hours. Text and JSON views qualify source
coverage, unknown dimensions, conflicts, missing expected sources and freshness.
`--model` matches trusted actual models; user declarations and requested aliases
cannot satisfy it. `--accounts` and `--money` expose local optional detail; this
same-user filesystem command does not authenticate or grant remote principals.

Manual input is a UTF-8 JSON document no larger than 1 MiB:

```json
{
  "version": 1,
  "observations": [
    {
      "id": "declared-call-1",
      "source": {
        "id": "my-manual-records",
        "kind": "manual",
        "quality": "user_entered",
        "observed_at": "2026-10-10T16:00:00Z",
        "sequence": 1,
        "coverage": "unknown"
      },
      "attempt": {
        "key": {
          "provider": "codex",
          "namespace": "my-declared-scope",
          "request_id": "declared-request-1",
          "attempt_id": "declared-attempt-1"
        },
        "at": "2026-10-10T15:59:00Z",
        "context": { "host": "host_ID", "session": "session_ID" },
        "requested_model": "my-requested-alias",
        "tool_calls": null,
        "final": false
      }
    }
  ]
}
```

All authored source IDs and canonical namespaces are deterministically isolated
under `manual_import:`; retries/parents are mapped to the same declared scope.
Kind and quality must be absent or manual/user-entered and are normalized to
manual/user-entered. Coverage is always unknown. Replaying the exact source/ID
is idempotent. Provider authority, actual charges, and all shared quota imports
are rejected. Account labels remain user-entered. Unknown fields, duplicate JSON
keys, concatenated documents, excessive nesting, special files, symlinks,
malformed records and intrinsic batch conflicts fail before state creation.
Existing-ledger conflicts reject the entire transaction without partial records.
If result output fails after commit, the error states that the import committed
and identical input can be replayed safely.

The isolated compiled command harness and CLI tests establish this command
handler only; root-owned main dispatch remains an integration gate until wired
and exercised in the product binary. Native quota mapping remains on hold until
a separately qualified account-read transport receipt is available.

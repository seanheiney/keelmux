# Deterministic local captain ledger

Status: development source prerequisite tracked in Plane #17. This increment
provides Store-only lifecycle observations and durable typed reports. The full
advice-captain runtime still needs authenticated production callers, model
integration, scheduling, remote sources and independent native qualification.

## Scope and identity

Migration to SQLite schema version 5 creates empty captain tables. It does not
adopt an owner, infer a principal from a thread, derive grants from group
membership or start any process. Existing session and operation bytes remain
unchanged. Established schema or receipt corruption requires repair; opening a
database does not reconstruct missing authority.

`internal/captain` defines the typed contracts; `internal/store` owns their
transactional implementation. `AuthCaller` is trusted identity supplied by a
separate authenticated local owner boundary. Its role and revision are checked
against current stored authority. It is deliberately absent from the current
Service, socket API and CLI. A caller must never construct it from request-body
principal or role fields. These cooperating checks do not isolate another
process running as the same OS user.

Principal roles are owner, runner and reader. Explicit directional read grants
bind the recipient to an exact local host, session, thread, execution generation
and first lifecycle sequence. Grant revocation is permanent for that grant ID.
Assignments have an immutable runner, captain execution, source grants, reader
audience, policy and reservation limit. Group membership supplies navigation,
not source authority. A captain cannot watch its own session or thread.

## Read and publication boundaries

Every source read or report publication checks current roles and revisions,
assignment control, captain execution and all source grants. Readers need their
own grants covering every input. Owner control does not confer source read
access. Pause or revoke denies further reads and publication; revocation cannot
be undone for an assignment ID.

Owner cancellation operates on verified metadata and the exact pending
reservation, so source revocation cannot prevent cleanup. Private Store.Open
integrity validation rechecks retained source evidence without publishing it.
These integrity chains detect missing or changed retained receipts and anchors;
they are not cryptographic protection against a trusted OS owner rewriting all
private state.

## Atomic lifecycle consumption

A scan selects a bounded complete prefix across the assignment's explicit local
lifecycle sources. All selected rows contribute digests, including quiet and
unsupported events. It retains finite typed facts and omits event Detail, names,
paths, terminal bytes and account text. Canonical operation IDs can survive the
projection; arbitrary historical operation text becomes unknown.

Unchanged scans create no entry. Quiet or unsupported-only checkpoints advance
all selected cursors atomically without reserving work. Meaningful scans first
reserve an immutable original prefix. Only one pending reservation is allowed.
Committing a report re-reads that exact prefix and atomically stores its typed
source-linked report, advances every cursor and clears the pending reservation.
A changed source row, missing anchor, mismatched revision or partial transaction
cannot commit.

Reports group finite lifecycle facts while preserving every meaningful source
key exactly once. They are constructed inside Store; no free-form producer or
model-supplied text is admitted. Reports and captain controls are not written
into watched worker event streams, avoiding report feedback loops.

Every view declares an observed lifecycle subset, incomplete history, no native
output and unknown historical execution origin. Current execution identity is
an authorization fence, not attribution of old events. Existing lifecycle
sequence allocation cannot prove unseen-tail retention or complete history.

## Budgets, retries and bounds

`MaxReservations` is a hard lifetime count of accepted reservations. Cancellation,
pause and restart never refund it; zero refuses. It does not imply a monetary,
token or provider-turn limit. Native invocation is `not_invoked`; actual model
turns, tokens and charges remain unknown. Provider usage observations remain a
separate source-qualified feature.

Exact retries require current authorization and verified immutable receipts.
They return the original reservation or report without consuming another slot.
Historical configuration acknowledgments never renew role or source authority.
Control, quiet and cancellation retries return verified current status without
reapplying a transition.

Limits include 64 principals, 256 grants, 64 assignments, 8 sources and 16 readers
per assignment, 256 rows and 128 KiB of original source bytes per scan, 1,024
entries and 32 MiB of retained entries per assignment, and 4,096 configuration
commands totaling at most 8 MiB. Bounds apply before retaining blobs. Overflow,
capacity exhaustion and uncertain persistence fail closed. There is no automatic
compaction, identity reuse or inferred missing receipt.

The package [implementation boundary](../../internal/captain/README.md) and
meaningful Store tests document the local API. Runtime scheduling, model advice,
remote report hierarchy and channel delivery need separate implementation and
evidence before they can be called shipped behavior.

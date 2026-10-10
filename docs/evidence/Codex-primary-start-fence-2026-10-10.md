# Primary start admission and stale stop correction

Base: `48619e66a12b42c99d4999972c707d133bcd371c` on the captain integration
branch. Worktree: `codex/primary-fences`. Environment: Darwin 25.6.0 arm64,
Go 1.26.6. This is source and fixture evidence; native transport remains disabled.

The common SQLite reservation now retains an immutable versioned start-attempt
receipt naming the exact controller incarnation and attempted timestamp. Its
transaction commits before a launch/resume callback. Only the newly committed
claim permits that callback. A matching claim returns no new permission; a
different incarnation is rejected. Losing or replacing the controller journal
therefore cannot replay a ready, stopped or uncertain start. The attempted marker
also survives failure before the controller journal is written. This deliberately
requires explicit reconciliation/repair when journal evidence is unavailable.

A matching controller journal retry returns the saved receipt before start gate
admission. The Service still rechecks owned process/thread binding before ready
publication, which must match the common attempted incarnation. No start request,
timestamp or incarnation is rewritten on retry. Legacy published records without
this attempt proof cannot establish a new start or publication automatically.

Stop rejects a mismatched target, incarnation, installation or reused operation
before entering the common gate and retains its checks inside the gate. Rejected
stale requests do not persist a stop fence. Existing legitimate stop retries
continue to check common durable blockers. All Start/Stop/Reconcile errors clear
the `OwnedPrimaryVerified` capability even when the durable view still says Ready.

There is no SQLite schema migration, new authority, product activation, native
probe, model turn, account read or credential publication. The diagnostics-owned
`internal/store/diagnose.go` was reviewed at `7419b387` and remains untouched.

Validation against the implementation tree:

- `go test -race -count=1 ./internal/codexcontroller ./internal/daemon ./internal/store`
  passed (4.016s, 12.823s, 9.157s respectively).
- `go vet ./internal/codexcontroller ./internal/daemon ./internal/store` passed.
- `git diff --check` passed.

New adversarial tests cover ready/stopped/lost-resume/pre-journal failure after
journal loss and registry restart; exact claim ordering before launch/resume;
existing matching claim refusing a second callback; unchanged retry timestamp,
incarnation, operation and event count; forged selections; concurrent claims;
missing/different publication claim; stale stop identity before gate admission;
and cached Ready error responses for Start, Stop and Reconcile. Existing package
tests continue to cover lost stop acknowledgement, callback fencing, grant
blocking, effect-free reconciliation and disabled native adapter construction.

# Scoped SSH observation and local captain prerequisite

Development implementation source: `896d851705035da8071978d64a75e4b90f94734d`, tree `6a7cad32c4b6e5f11f0fe38cea1009e5e806a6b0`. Public v0.2.1 and its installer remain unchanged. This record qualifies source checks and the bounded local implementation; it does not qualify an installed release, native model providers, two physical servers or a captain model runner.

The [read bridge](../user/ssh-read-bridge.md) opts into one explicit home-owned policy and enrolled entry selector. It joins exact home/session/thread/execution identity, preserves an immutable observer direction and durable once-only opening attempts, and refuses writer, input, resize, launch, stop and model actions. The final correction wraps inherited SSH stdio in owned pollable descriptors so quiet cancellation and stalled writes can unblock; unsupported stdio refuses before home effects. Missing authority or replaced enrollment/locks is not recreated, and cleanup uncertainty remains an error.

The [captain ledger](../architecture/0005-captain-ledger.md) is a separate Store-only prerequisite: explicit independent directional read grants, bounded typed lifecycle scans, atomic all-source reports/cursors, and monotonic lifetime reservation slots without refund. Schema 5 migrates empty metadata, inventing no owner or grants. No production authenticated caller, scheduler, model/tool invocation or dispatch surface is supplied. Actual provider turns, tokens and charges remain unknown; `MaxReservations` counts reservations only.

## Source checks and cache scope

| Source | Whole normal command | Whole race command | Vet | Documentation tests |
| --- | --- | --- | --- | --- |
| `c5dda37` | Pass, 103.795 s | Pass, 238.876 s | Pass, 1.025 s | Pass, 0.303 s |
| corrected `896d851` | Pass, 6.281 s | Pass, 9.307 s | Pass, 0.307 s | Pass, 0.251 s |

Commands were `go test -p 1 -tags library_registry ./...`, the corresponding `-race` command, `go vet -tags library_registry ./...`, and `python3 -m unittest discover -s docs/site/tests -v`. Ordinary Go caching was accepted: `c5dda37` logged 8 cached normal and 7 cached race results; corrected `896d851` logged 36 cached results out of 37 passing packages in each suite. These are successful whole-suite commands, not fresh executions of every package at the corrected source. The inherited-stdio regressions changed and executed at the corrected source; the prior working-tree focused result has its own scope and is not relabeled as a committed-source proof.

Four development binaries built from the genuine clean corrected-source clone with VCS metadata enabled. Their recorded command durations total 3.304 s; this is the sum of build command durations, not overall wall time. Exact executable hashes and original receipt/log hashes are in the [finite source record](bouncer-read-captain-896d851.json). Development builds are distinct from installed release archives.

## Retained failures and actual product gate

`product001` at `c5dda37` refused the binary preflight because Go VCS metadata was absent; no product/native fixture was launched. A genuine source clone repaired that build-provenance precondition. `product002` at the same source then reached actual same-machine SSH entry Ready, Unicode/ANSI output, disconnect/restart, original-operation refusal and forbidden input/resize controls, but failed the unchanged two-second quiet-revocation assertion. That failed result and original bytes remain retained. The inherited blocking-stdio correction addresses a source boundary not exercised by the earlier `net.Pipe` controls; this record does not erase either failure or treat partial progress as a passing product journey.

The actual corrected-source `product003` gate passed with the same product fixture and child-source bytes as failed `product002`; no assertion or timeout was weakened. Test duration was 4.04 s, package duration 4.338 s and command wall time 5.937 s. All four product executable files and VCS source stamps matched the clean `896d851` clone; the recorded file hashes remained unchanged after the run. The gate reached actual matching Ready and Unicode/ANSI output, zero forbidden native input/resize, disconnect and entry restart with original-operation refusal, quiet policy revocation, exact cleanup through disabled scope, explicit regrant/new-operation/release, and missing-home refusal without state creation. Test-owned cleanup assertions completed without an error; this is not an independent whole-system PID census.

Private original `product003` receipt SHA-256: `81c46d71c9c9db669e8407f2837425eca7c378d4404b823cabd5ed10cfc29137`; original log SHA-256: `cc4d694548cd64168d3507cb69e1c7eff5af5fab57c041e26076a22004ccb87b`. The finite source record preserves both earlier failure hashes and this new success. The actual workflow used one owned same-machine loopback SSH home and a deterministic PTY child, with zero model turns. It does not qualify an independent physical server, native model provider or installed release.

Owner-managed SSH policy labels remain distinct from authenticated team principals or same-user sandbox isolation. Native credentials stay at the home. No model turn, customer message, native provider loading or public release is established by these source gates. Later documentation-only source must preserve every non-documentation byte from the implementation source above; its own documentation/hosted checks remain separately bound.

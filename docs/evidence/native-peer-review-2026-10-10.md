# Native batching and installed-package peer review

Reviewed `e182d2c46c035f2cb4ecef4c1ac48a2fe84a8581` and its evidence commit
`9111f1f04c6ec60606bc62909dbf2129300f46e0`. Corrected source:
`19630c795362f79eec0a155ce284f33929c07236`, on
`codex/native-package-cleanup`. Native product files are unchanged since
`812d6401790e2487e04136330e9d774dad9b3607`. This is local source qualification,
not a release or renewed provider/archive qualification.

The original batch change was not safe to integrate alone. At seven columns,
`1234567\x1b(BABC\u0301` produced row two `ABCĆ` when delivered in one batch,
versus `ABĆ` bytewise. SI and HT at the same boundary reproduced the error.
The control path cleared its delayed-wrap flag while the pinned emulator kept
its internal flag. The first ASCII character then wrapped, but the batch's
arithmetic placed the final grapheme one cell too far right.

Commit `fd4c093` preserves delayed wrap across character-set selection/shifts,
nonmoving controls, and a tab at the right edge. Commit `812d640` also guards the
fast path until an ASCII glyph establishes an unambiguous interior cursor and
the tracked cell agrees with that cursor, or autowrap is disabled. Other states
continue bytewise until they can be reconciled. One- and two-column autowrapping
screens conservatively remain bytewise. The first ASCII character still passes
through Unicode-tail handling, and control/UTF-8 framing still uses the parser.
No terminal query policy or product signal handling changed.

The explicit regression asserts the actual A, B, accented C and following blank
cells plus cursor position across bytewise, two-byte, five-byte and full input.
Additional differential cases cover cursor visibility, REP and screen erasure.
The original Prepend, variation-selector, ZWJ, origin/margin, alt-screen and
query-authority tests remain in the passing suite. The post-write glyph-row fix
correctly uses the emulator's actual row after vertical-margin scrolling. The
Linux shell fixture retry still distinguishes trapped SIGINT from actual EOF;
it does not change product signal delivery.

The package-wrapper review found two separate gate gaps, corrected in
`5274e54`:

- `subprocess.run(timeout=300)` could kill the native Python harness before its
  `finally` block, leaving its deliberately persistent supervised processes.
  The wrapper now sends SIGTERM to its unreaped direct Python child, allows a
  90-second cleanup grace period, and records an unresolved failure if that
  period expires. It never signals saved provider/helper PIDs. The child records
  cancellation through a flag and checks it only after ownership bookkeeping;
  its existing exact session/execution/generation and daemon-instance cleanup
  runs in `finally`. Failed/cancelled cleanup receipts are retained in the
  wrapper report. A timeout cannot become a passing qualification.
- The shared `check_installed` helper overwrites canonical public-command hashes
  with selected-version hashes. A fixture with public links to stale bytes
  inside the owned prefix therefore passed that helper. The new wrapper now
  independently verifies every public link's exact target, resolved canonical
  destination and digest against the selected archive. The shared helper itself
  was not changed by this review.

Commit `bf4eb59` extends the same cancellation contract to the wrapper itself.
It installs SIGINT/SIGTERM flag handlers before establishing direct-child
ownership, waits in bounded 100-millisecond increments, and routes cancellation
through the same exact child and cleanup grace period. Repeated signals cannot
unwind its cleanup. Handlers are restored after the child is reaped, and even
confirmed cleanup after cancellation leaves qualification failed. No signal is
sent to a saved provider/helper PID.

Commit `19630c7` closes the separate lost-launch-acknowledgement failure path.
The harness records the exact operation ID before launch I/O. Session and
execution IDs remain explicitly unknown until the daemon's reply binds them to
that operation. Missing, mismatched or uncertain acknowledgements remain
unresolved cleanup issues even when zero process identities have been observed.
The existing exact stop path still handles acknowledged sessions; an unresolved
launch retains its owned daemon/private state for reconciliation, without a
guessed stop or replay. A provider-free regression invokes the real journey and
cleanup path with an API timeout after a possible launch effect, and verifies
the recorded intent, failed cleanup status and absence of guessed stop calls.

Provider-free subprocess regressions use the real child's signal handler,
main, cancellation checkpoints, `finally`, and cleanup functions with a fake
authority. A cancellation arriving during simulated launch acknowledgement
bookkeeping still records the exact execution before requesting its stop. A
second subprocess deliberately ignores SIGTERM and verifies bounded failure;
the only escalated signal targets that direct Python fixture. These tests are
not real-provider or real-supervisor cleanup evidence. An additional subprocess
test sends both SIGINT and SIGTERM to the wrapper during the child's simulated
launch bookkeeping, and verifies the exact child cleanup receipt before the
wrapper exits.

Validation on Darwin 25.6.0 arm64, Apple M4 Max, Go 1.26.6:

| Command / check | Result |
| --- | --- |
| `go test -race ./internal/adapters/nativepty ./cmd/keelmux-supervisor -count=1 -timeout=90s` at `812d640` | Pass; native package 9.048 s; supervisor command has no test files |
| `go vet ./internal/adapters/nativepty ./cmd/keelmux-supervisor` at `812d640` | Pass |
| `python3 tests/native_package_acceptance_test.py -v` on the exact Python bytes committed as `19630c7` | 6/6 pass, including lost acknowledgement and wrapper SIGINT/SIGTERM subcases, 5.617 s |
| Independent temporary-overlay differential probe, seed 918821, 2,000 token streams, widths 1–15 and heights 3–10, at `812d640` | Pass, 1.347 s; original `e182d2c` failed case 1222, subsequently minimized above |
| `go test -race ./internal/adapters/nativepty -run '^$' -bench BenchmarkScreenASCIIBurst -benchtime=1x -count=1 -timeout=60s` at `812d640` | Pass; 2 MiB 3.100350500 s, 284,822,128 bytes allocated, 2,123,623 allocations |
| `git diff --check` | Pass |

The benchmark is one local observation, not a universal throughput guarantee.
The original eight-second slow-reader deadline is unchanged and remains in the
passing race suite.

The finite `work/native-package-db94705.json` receipt was also reviewed. Its
installer, wrapper and child-harness hashes matched the source files present at
review time, the embedded native receipt reproduced its recorded SHA-256, and
the selected binary hashes matched the archive manifest, installed binaries and
frozen child binaries. Its two provider checks record four owned processes no
longer live, no cleanup issues, no native Enter, no model turn, and no tmux
invocation. The uninstall assertions match the installer's deliberate retention
of its private ownership scaffold. Those are qualifications for archive source
`db947051f79dadb45d538604b29a174ffce82056`; they do not qualify these later repairs.
The child receipt's generic development-binary wording is superseded only for
that run by the outer archive/install/hash binding, not by an anonymous public
download proof.

No providers/models were started by this review or repair, and no network writes
or publication were performed. Fresh installed-package/native-provider proof
on final integrated bytes, hosted CI, Linux runtime coverage of these follow-up
repairs, and anonymous public-download proof remain separate gates.

A subsequent independent expected-state review identified a remaining short-tail
screen defect at `812d640`: at width seven,
`1234567\x1b[?25lA\u0301` duplicates A and leaves the cursor at column six
instead of preserving one accented A at column zero with cursor column one.
CSI 2J and an autowrap-disable/enable pair can expose the same underlying shadow
state disagreement. The conservative batch guard preserves bytewise behavior
in these cases, which is insufficient when the bytewise state is itself wrong.
Native source integration remains held for a separate correction and explicit
short-tail expected-cell tests. The package cancellation fixes and their
provider-free test results are independent of that remaining native defect.

Source SHA-256 at `19630c7`:

```text
54e62b5cfb97b427ce619a6b26231395c5671125877ee0f23369bb9dffe755e9  internal/adapters/nativepty/filter.go
1cbaafa2ca9af96d05f35ce4716c49514e5e8785dad200d605ce41a525526c1e  internal/adapters/nativepty/performance_test.go
a756fe17ef2c4abfacf3169bad217dfdb368da9076d7eedea8a8475d45177cce  tests/native_package_acceptance.py
e9a4fdb2faab43e306ed1866409e5a4377a153f2019d9d688620e1b652565f02  tests/native_supervisor_acceptance.py
34130c06cbede6e31dd793d8fcd75393421bfb6511d156b31cd3259e36ece288  tests/native_package_acceptance_test.py
```

## Independent review of the subsequent native correction

The separate author correction
`2397355a6ae9cbd0df742f6df0f8fa5788fe6766` resolves the short-tail hold above.
Its explicit control tracking agrees with the pinned emulator's wrap reset
operations. The post-write cursor reconciles short-glyph positions, while
one- and two-column screens retain the exact delayed-wrap shadow where that
cursor alone is ambiguous. Unicode Prepend followed by ASCII is retained
through the emulator's public cell API after writing the spacing portion;
the parser framing and strict query policy are unchanged.

Independent checks on that exact clean author source:

| Check | Result |
| --- | --- |
| Short-tail expected-cell matrix, reset/REP/restore/backtab/reverse-index/resize cases, and ASCII cases | Pass, 0.998 s |
| Independent seeded 2,000-stream differential overlay described above | Pass, 1.303 s |
| Independent temporary-overlay wrap oracle: 49 controls, five starting states, widths 1/2/7, comparing the shadow to the pinned emulator's actual internal wrap bit after every segment | All 735 combinations pass, 0.661 s |
| `go test -race ./internal/adapters/nativepty ./cmd/keelmux-supervisor -count=1 -timeout=90s` | Pass, native package 8.534 s; supervisor command has no test files |
| `go vet ./internal/adapters/nativepty ./cmd/keelmux-supervisor` | Pass |

The review found a separate, pre-existing origin-margin bookkeeping defect.
At width seven and height four, the exact input
`\x1b[2;3r\x1b[?6h\x1b[2;1H1234567\x1b[3;1rA\u0301`
must leave zero-based row one as `1234567`, row two with a single accented A
at column zero, the cursor at `(1,2)`, and rows zero/three blank. The pinned
emulator rejects the invalid `3;1r` margins and retains `2;3`; the existing
screen callback nevertheless records top three/bottom one. The resulting
accent rewrite instead writes an accented A into row one and leaves a plain A
in row two, cursor `(1,1)`. The defect was reproduced independently on
`2397355`; it was not introduced by that correction. The captain owns the
narrow margin-validation follow-up and its final integrated source freeze.
No source changes were made in the author's worktree by this reviewer.

The captain's follow-up
`c55fff82176f4e8d941a569136e15567872ffbb9` resolves that margin defect. Independent
inspection confirmed that the guard applies the pinned emulator's same defaults
and rejects the same inverted/equal bounds. The new 20 expected-state cases
cover inverted/equal bounds and zero top/bottom/both values across four chunk
sizes, asserting every cell, cursor, pending wrap and tracked region. A separate
race-enabled overlay run passed in 3.468 s: the original minimized margin repro,
all 20 new cases, the 735-combination wrap oracle, and the 2,000-stream seeded
differential probe. The root HEAD and native source bytes were unchanged before
and after that run. This review approves native source integration at `c55fff8`;
no native source blocker remains from this review. Final combined qualification
and fresh installed-package/provider proof remain the captain's separate gates.

Verified native SHA-256 at `c55fff8`:

```text
c7fac861fadbd01321d4ab36977505fa782a47c0356c5ca969b30156e48dcb25  internal/adapters/nativepty/filter.go
1b66bc668054beac35a98ff981984920caef4e78adf083f0d10a69d74204f47b  internal/adapters/nativepty/screen.go
cd5a4dff9a45f5f00364a9d6f9612a14f5f73ae08d7f2193a0bce5c76018c954  internal/adapters/nativepty/glyph_state_test.go
```

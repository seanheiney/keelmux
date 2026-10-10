# Native batching and installed-package peer review

Reviewed `e182d2c46c035f2cb4ecef4c1ac48a2fe84a8581` and its evidence commit
`9111f1f04c6ec60606bc62909dbf2129300f46e0`. Corrected source:
`bf4eb59123e954f168e9629293a2e6996608c6f2`, on
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
| `python3 tests/native_package_acceptance_test.py -v` on the exact Python bytes committed as `bf4eb59` | 5/5 pass, including wrapper SIGINT and SIGTERM subcases, 5.613 s |
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
repairs, and anonymous public-download proof remain separate gates. After the
listed fixes, no remaining integration blocker was found in the reviewed diff.

Source SHA-256 at `bf4eb59`:

```text
54e62b5cfb97b427ce619a6b26231395c5671125877ee0f23369bb9dffe755e9  internal/adapters/nativepty/filter.go
1cbaafa2ca9af96d05f35ce4716c49514e5e8785dad200d605ce41a525526c1e  internal/adapters/nativepty/performance_test.go
a756fe17ef2c4abfacf3169bad217dfdb368da9076d7eedea8a8475d45177cce  tests/native_package_acceptance.py
ddd61ac22b4f66995a51a22946d1c36ec59b66c94862eaaa6238183684121b5e  tests/native_supervisor_acceptance.py
145aeb00747a6b8f4b6646e807b256924e804245ce66f59996ff1e779cfaa1b9  tests/native_package_acceptance_test.py
```

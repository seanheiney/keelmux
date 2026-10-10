# Main-screen history allocation correction

The hosted macOS push run at `e80486f0fc7601db0608efaf8370d7bebd08ec82` failed the unchanged `TestSlowReaderDoesNotBlockNativeOutput` after 8.10s. Its original log is retained at `/Volumes/DataSSD1/agent-backplane/work/release-v030/push-macos-e80486f.log`, SHA-256 `7723159bc968ff0a386466b0348da1515a0cb6a44df7bd1b375468063273ea47`. The accompanying JSON records all profile/log/source hashes, exact commands' result coverage, first authored-fixture failure, and limits.

This isolated `codex/native-throughput-v030` correction changes one production initialization statement in `screen.go`. The existing `SetScrollbackSize(0)` was ineffective: pinned `vt.Scrollback.SetMaxLines` rejects zero, leaving a default 10,000-row main history. Each wrapped-off row is cloned, and a full history also shifts its row headers. Keelmux reconnect regenerates current visible cells and never consumes that emulator history. The raw bounded output ring is a separate structure.

The initialization now assigns the exported zero-valued `vt.Scrollback` to the existing main-history handle. Its `Push` explicitly returns at zero capacity. This affects unused main-history storage only: every output byte still passes the existing parser/emulator; visible cells, styles, modes, cursor, query replies and raw output ring retain their existing behavior. The pinned public accessor exposes only main history. **Alternate-screen history remains the library default; this fix does not claim that all history is disabled or that alternate-screen floods improve.** No dependency files, protocol, daemon, CLI, timeout or existing test were changed.

## Causal measurements

One retained race-enabled `BenchmarkScreenASCIIBurst` iteration processed exactly 2 MiB on Darwin arm64, Apple M4 Max, Go 1.26.6, before and after the change. Baseline: 2.988s, 284,835,688 bytes, 2,123,632 allocations. Fixed: 2.336s, 35,807,464 bytes, 2,097,401 allocations. Allocation bytes fell 87.429%. Baseline allocation sampling attributed 82.45% of bytes to scrollback row clones; the fixed profile has no such clone allocation. TSAN `MemoryRangeSet` accounted for 62.33% of sampled CPU before and 6.94% afterward. Per-character emulator allocations remain; no parser optimization or output skipping was introduced.

The screen benchmark has no PTY, output ring or viewer queue, so it isolates tracker capacity. These single local profiled timings are not a hosted-runner throughput guarantee. The old source overlaid onto the new regression correctly fails at all four terminal sizes: main capacity remains 10,000 with 18, 27, 36 and 216 retained rows. The local pinned `vt` `utf8.go` and `emulator.go` were also compared with their downloaded module ZIP and were byte-identical; no local dependency patch explained the result.

## Final scoped verification

- Unchanged original eight-second slow-reader gate passed three race runs: 2.93s, 2.81s, 2.82s. All of `screen_test.go` and `performance_test.go` remain byte-identical to the base.
- A history-enabled positive reference retained real wrapped rows. Three race runs compared exact visible/reply bytes at every 13-byte split and complete writer/read-only snapshots at bounded control-step checkpoints. Terminal sizes 1x2, 2x3, 7x4 and 80x24 covered long ASCII, combining/emoji tails, wrap, origin/scroll margins, charsets, insertion/erasure, alternate toggles, RIS and resize. The corrected tracker retained zero main rows throughout. Each matrix took 0.53s.
- A real unread viewer remained connected through a 2 MiB native PTY burst. The complete `DONE-e` combining-accent tail was followed by a CPR; the exact seven-byte `ESC[24;7R` was required within the unchanged eight-second bound. Three race runs acknowledged it in 2.806s, 2.726s and 2.693s; reconnect snapshots retained the complete Unicode tail and the exact owned child remained alive.
- `go test -race ./internal/adapters/nativepty`: passed, 11.267s.
- `go vet ./internal/adapters/nativepty`: passed.
- `GOOS=linux GOARCH=amd64 go test -c ./internal/adapters/nativepty -o work/native-throughput-main-linux.test`: passed compilation; Linux runtime of this correction remains separate.
- `git diff --check`: passed.

The first authored new burst fixture failed after 8.33s because it mistakenly read eight bytes for a seven-byte CPR. That failure remains retained in `work/native-throughput-main-focused.log` (SHA-256 in JSON), along with the first 28.10s differential that rendered a full snapshot for every tiny chunk. The fixture now reads exactly seven bytes and checks full snapshots at control-step boundaries while preserving every visible/reply split comparison. No deadline or payload assertion was relaxed. The failed first fixture is not presented as a production failure or erased by the passing correction.

Hosted post-fix source checks, Linux runtime, and a fresh exact-source release archive with installed-product acceptance remain required. No provider/model calls, customer service mutations, native credential changes, shared-root edits, pushes or release publication were involved. The earlier installed SSH qualification at `e80486f` remains evidence for that earlier archive only.

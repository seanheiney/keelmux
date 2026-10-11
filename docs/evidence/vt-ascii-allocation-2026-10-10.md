# Pinned VT ASCII allocation contribution

This contribution preserves the complete pinned VT module and changes one
guarded expression to avoid an allocation in the measured Go runtime. It is
prepared on base `91a106a04568f01868488b86f79633c012fdbced` in an isolated
worktree. Root `go.mod`, `go.sum`, native PTY source, throughput assertions and
eight-second deadlines remain byte-identical to the base. Captain integration
owns the root replacement and CI wiring:

```go
replace github.com/charmbracelet/x/vt => ./third_party/vt
```

[The contribution receipt](vt-ascii-allocation-2026-10-10.json) binds the local
checks to the base, temporary replacement, final copy provenance and retained
log hashes. [The original sanitized hosted receipt](vt-ascii-overlay-hosted-505ad2d.json)
is retained byte-for-byte, SHA256
`89dbfd0b59b90cdf891597abe4a6e7957d48c4f936753da2b32da73e0f7a73c1`.
Private local logs and the supplemental conversion fixture stay in ignored
`work/vt-allocation-gates/`; paths are omitted from public receipts.

The upstream module is `github.com/charmbracelet/x/vt` at
`v0.0.0-20261008172826-faa4adf95555`, full commit
`faa4adf95555331dfb439b716cc83767ecdadeb4`. All 33 official ZIP files are
present: 32 byte-identical and one with the exact approved expression change.
All four upstream test files, `go.mod`, `go.sum` and the MIT license remain
unchanged. `third_party/vt/upstream/` retains the official ZIP, its original
module-origin receipt and the original `utf8.go` sidecar.

| Identity | SHA256 |
| --- | --- |
| Official module ZIP, 48,426 bytes | `33e779af4c1855a14128f1cb623dc11a35e28b5dadb20a3de5a7a23bf416e9f8` |
| Official origin receipt | `8ee7c42a67ebeb1f5e3df9b92065962c9f6b66da13ebde8fd41ed5a107c1c190` |
| Original `utf8.go` | `5362209587478feb6c3dc5367ed5f02c7789a8094736c42d305848ee1aa15247` |
| Changed `utf8.go` | `05154c7c3544764e8c7678c6b933bf582fb31ac092498ce00b97b47f348e560e` |

The offline verifier derives every permitted source byte from the hard-pinned
ZIP and recomputes the original Go module and `go.mod` h1 checksums. It checks
the full Git commit in the preserved origin receipt, every active and authored
file, original sidecars and the exact finite inventory. It refuses another
patch even if its file hash is forged in the provenance metadata. Missing or
extra files/directories, changed source/tests/license/module/guard, malformed
or duplicate provenance fields, symlinks, hardlinks, special files and executable
file modes fail without rewriting the source. The reviewed verifier and Git
are trust anchors; this is not a sandbox against an owner replacing those
anchors.

The only expression change is:

```diff
- e.handleGrapheme(string(e.grapheme[0]), 1)
+ e.handleGrapheme(string(e.grapheme[:1]), 1)
```

At [the unchanged branch condition](https://github.com/charmbracelet/x/blob/faa4adf95555331dfb439b716cc83767ecdadeb4/vt/utf8.go#L39),
the buffered grapheme has exactly one byte and `printableASCII` accepts only
`0x20..0x7e`. Thus indexing and slicing are both in bounds. Integer-to-string
conversion encodes that ASCII code point as the same one byte that the slice
conversion copies. Values at or above `0x80` would not be equivalent and cannot
enter this branch. The Go string remains immutable after `utf8.AppendRune`
reuses the buffer. `handleGrapheme` receives identical content and width; its
charset, style/link, cell, cursor, pending-wrap, mode and callback behavior is
untouched. Non-ASCII grapheme handling and flushing are untouched. This follows
the [Go conversion rules](https://go.dev/ref/spec#Conversions_to_and_from_a_string_type)
and [string immutability](https://go.dev/ref/spec#String_types).

In [Go 1.26.6](https://github.com/golang/go/blob/go1.26.6/src/runtime/string.go),
the one-byte slice conversion uses immutable static byte storage while the
escaping integer conversion allocates. That implementation detail explains the
measured allocation reduction; language equivalence is independent of it.
A supplemental owned standalone race test compares all 95 guarded ASCII bytes,
checks all 128 non-ASCII bytes as negative controls and verifies 95 retained
strings after the same backing buffer is reused. It passed in 1.255 seconds.
The fixture and output hashes are in the contribution receipt.

The final local Go checks used Go 1.26.6 on Darwin 25.6.0 arm64. The root
module selected the copy only through an ignored temporary `-modfile`; a saved
`go list -m` result confirmed the replacement, and both root manifests were
checked against the base after execution. Final documentation and preserved
origin metadata were completed after the Go checks; all compiled Go bytes
remained identical and the completed inventory passed the final verifier.

| Local check | Result |
| --- | --- |
| Offline verifier, independent official cached ZIP | PASS |
| Ten adverse verifier tests, including every upstream file changed/missing | PASS, 0.418 s |
| Unchanged upstream `go test -race -mod=readonly -count=1 -timeout=90s ./...` | PASS, 1.314 s |
| Unchanged upstream `go vet -mod=readonly ./...` | PASS |
| Native PTY `go test -race -p 1 -count=1 -timeout=90s -v ./internal/adapters/nativepty` | PASS, 12.252 s |
| Native PTY vet with the same temporary replacement | PASS |
| Original unread-viewer gate, unchanged eight-second deadline | PASS, 2.87 s |
| Complete Unicode-tail gate, unchanged eight-second deadline | PASS, 2.88 s; full-tail CPR acknowledgement 2.817 s |

Two local setup refusals are retained. The first upstream invocation had no
runtime test execution because the offline cache lacked the original module's
`x/sys v0.41.0` and `x/sync v0.19.0`; authenticated public Go downloads supplied
them without changing module files. That initial invocation also generated
scratch from the wrong working directory; the exact generated scratch was
moved out of the VT inventory into ignored work. A later quoted shell
assignment prevented a read-only module query from invoking Go; its error is
retained, and the separate query used `env` correctly. Neither is counted as a
passing runtime observation.

The earlier [hosted overlay investigation](https://github.com/seanheiney/keelmux/actions/runs/38098753313)
ran workflow commit `505ad2d4be836476fe78bc728ae77e14312f57c0` against source
`4ef3c9afce086a07abd4646bd6c428abe8d43b90`. Its owned compiler overlay changed
exactly the same `utf8.go` bytes, without adopting a production dependency.
The `measured-overlay` job succeeded on actual Linux amd64, Go 1.26.6,
AMD EPYC 7763, `GOMAXPROCS=2`. Artifact
`nativepty-overlay-505ad2d4be836476fe78bc728ae77e14312f57c0`, ID `11686679093`,
has recorded upload digest
`sha256:e316655a877410390a7f19b534fa980f1fa02fc0e63535a7b483a81afdac46fe`.
Only uploaded sanitized evidence was downloaded; none was executed.

| Race benchmark, 2 MiB ASCII | Baseline | Overlay |
| --- | --- | --- |
| Sample seconds | 4.623508, 4.611201, 4.624978 | 4.439785, 4.567537, 4.452761 |
| Median seconds | 4.623508 | 4.452761 |
| Allocations per sample | 2,097,383; 2,097,383; 2,097,382 | 358; 358; 359 |
| Allocated bytes per sample | 35,786,256; 35,786,256; 35,786,144 | 2,233,760; 2,233,760; 2,233,872 |

This is 99.983% fewer allocations and a 3.69% lower median race-benchmark
latency across three sequential samples on that focused runner. It is not a
general latency guarantee. Both baseline and overlay passed the unchanged
original and Unicode-tail eight-second gates three times in that experiment.
The exact visible/reply bytes, full snapshots, cells/styles/links, cursor,
wrap, grapheme and mode states matched at 35,780 valid-margin checkpoints,
including split inputs and resize. Both traces hash to
`4f1647051c1c0fd338d1f74c88e28e9b310fb45eb89e261881e8385caab172e0`.
All 18 commands returned zero and their authenticated owned process groups
were cleaned up. Profiles retain raw and sanitized hashes in the original
receipt; sanitization rewrote absolute-path strings while preserving all
non-string profile fields and indices.

The separate [ordinary Source and packaging run](https://github.com/seanheiney/keelmux/actions/runs/38098753211)
at `505ad2d4be836476fe78bc728ae77e14312f57c0` succeeded in all six jobs:
macOS source, Ubuntu source and Darwin/Linux amd64/arm64 builds. It uploaded
no artifacts. It used the original production dependency; it does not qualify
this newly copied module's integration.

The earlier exact-main Linux amd64 run `38097395544` remains a failure at
source `4ef3c9afce086a07abd4646bd6c428abe8d43b90`: original gate 8.12 seconds,
Unicode-tail gate 8.19 seconds. Its retained log SHA256 is
`959fa7c37afe7f2ce8567e05a79f8f19a30950e32e44a5fd42e607d1ef50145a`.
The focused experiment does not prove that failure's cause or supersede it.
CPU profiling showed substantial race instrumentation cost, limiting the
latency benefit even when allocations disappeared.

The broader original equivalence trace also exposed an independent pinned VT
invalid-margin panic. On a 1-column, 2-row screen, exact bytes
`1b5b323b33721b5b323b31481b5b4d` permit an out-of-screen DECSTBM bottom before
delete-line indexes beyond the buffer. Both baseline and overlay failed.
The reproducer receipt SHA256 is
`32ef56f2654e9279bf5504dedeecf68f4d0716631775da844e4f33e1232d3628`.
Its separate native boundary correction, commit
`a1b104fc67721deeff117ef68d0ce6bd02ded518`, is already in this contribution's
base; no extra upstream fix is folded into the copy. The constrained hosted
trace qualifies valid margins only. Emulated amd64 compilation also failed
before runtime and remains unqualified.

Integrated full-source tests, fresh hosted Linux/macOS, packaging and release
qualification remain captain gates. This lane made zero model/provider calls,
no native configuration changes and no root manifest, CI or publication edits.

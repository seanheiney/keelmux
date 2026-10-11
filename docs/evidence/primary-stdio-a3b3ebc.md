# Supervisor-owned stdio source milestone

Integrated source `a3b3ebcb549db0a11c77f79b82e27170e3678db3` passed five local source/build checks on macOS-26.7.1-arm64-arm-64bit-Mach-O, go version go1.26.6 darwin/arm64. Tracked tree: `c33e66ebda82906d05cd86a395da84d973b85bb3`. Source stayed clean and unchanged during all checks.

The per-execution supervisor owns three pipes, durable native attempts and a chained receive spool. The controller commits evidence to the existing SQLite authority before acknowledgement and uses exact callback/publication/stop barriers. Recovery preserves original launch, bootstrap and signal attempts. Stdio writer/observer grants are unavailable; no public controller route is exposed. Store schema remains version 4 and legacy PTY/process/WebSocket paths stay distinct.

| Check | Command | Result | Seconds | Log SHA-256 |
| --- | --- | --- | --- | --- |
| normal | `go test -count=1 -p 1 -tags library_registry ./...` | PASS | 119.408 | `322d88792d37dba53fb77188395a55e2c8b1f24f3d6ad97fdbbef22d7c334c89` |
| race | `go test -race -count=1 -p 1 -tags library_registry ./...` | PASS | 225.96 | `d63f199483106bdecd7590ed58e93cd8f435b90053d2ca3618fd0ffa3eda7cbd` |
| vet | `go vet -tags library_registry ./...` | PASS | 1.182 | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| build | `make build` | PASS | 3.782 | `df4fd02ed9ce556008e8d60aae65ebae7b523067ddea687f5f590dca020507eb` |
| site | `python3 docs/site/tests/test_site.py` | PASS | 0.252 | `a480e07529784e2e7dde969f23c56877be172c70d481562541f1fd3cf21d1dd0` |

The scoped supervisor worker race/vet and Linux test/helper compilation also passed. Its initial contribution `6a8155db7ccc62c4e75b8e37d0eba0a380821950` has a hash-bound receipt `82e8133f2f0c348bae95e7e2a5d0584f8fb6fc86645abb6bdb1818101fc93dd6`; the separate lifecycle follow-up is `64c829d3426b0ea4d0bdaaa6fccbc45d8df9fc76` with receipt `714fd869edac7563ceced3ac19725516707873cc7279bf9b12f902d08a532d0d`. The latest captured bytes of all thirteen contributed files exactly match this integrated source. Independent finite review closed the initial three controller and three supervisor findings; the later lifecycle assessment identified the bootstrap cache and post-stop admission issues corrected by the follow-up. Original reports, direct negative controls and source bindings remain retained.

## Scope and retained failures

The real pipe/private-IPC fixtures use the compiled nativepty Go test executable as both helper and deterministic protocol child. Package-private test-only image seams cannot be selected in an installed binary. Common Service/SQLite/controller tests use an injected supervisor. These results establish the exercised engine and authority boundaries, not actual Codex behavior or confinement. `NativeProviderQualified` remains false. No native model turn, credential login, approval answer or new external message dispatch was performed by these fixtures.

The initial common-controller temporary-directory alias and supervisor overlong socket fixture refused before helper/child effects; canonical short owned fixture paths corrected setup without weakening production policy. Worker failures additionally exposed a real disconnect race and malformed blocker classification, both corrected and retained. The deliberate pre-review overlay fails the three requested supervisor controls; it is negative evidence, not a final-source failure. Earlier public/main candidate throughput failures remain recorded in their original evidence and are not relabelled by this source result.

The first combined source `8d19ea899dafad106100869965a494f972c2c08c` passed full normal tests but failed tagged race in `TestStdioDisconnectCannotCompleteOrReplayBootstrap`: the retained observation did not become Uncertain within its original three-second assertion. That log does not capture the exact durable bootstrap state or native methods at failure. Its race log `36463e5f7ba47cf9af6bc236bb3e75a2f316bffd1bb250c62bdbeff2904c7acd` and normal/receipt bytes remain immutable. A deterministic regression separately reproduced a same-cursor snapshot retaining BootstrapAttempted after durable Uncertain, which explains the observed symptom. The follow-up invalidates snapshots on bootstrap transitions, stop attempt and retained child exit, and refuses quiescence after a stop operation. It retains the original three-second and no-replay assertions. New source results above do not relabel the first combined failure.

Production Darwin stdio preflight refuses before launch intent/helper/child because running-image verification is unavailable. Linux running-image source and cross-compilation are not installed Linux or native-provider qualification. Actual normal-helper/native Codex qualification, configuration/network/credential isolation, product activation, semantic input/approval authority, native capability loading and account/model proof remain separate gates.

The existing reusable capability library supplies pinned pack/profile/component switches and native preview/apply/status/recovery. Gemini/Grok/OpenCode and OpenRouter selection remain version-reviewed launch contracts; Grok MCP is unsupported. This controller does not extend their native capability or qualification.

Published v0.2.1 and its installer target are unchanged. No new tag, release or published archive is created by this milestone. Full two-server Bouncer work continues. Hosted checks are recorded separately against their own exact checkout after publication of the draft.

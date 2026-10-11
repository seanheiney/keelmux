# Capability controls source qualification

Exact implementation source `da429525953715956bb765cac98fbc8691be8cfc`, tree `1fd70620769c36019656b5e8aeda1cb3fb12c3ea`, passed five fresh local checks on macOS arm64 with Go 1.26.6. The following documentation-only commit preserves all non-documentation source bytes. Hosted qualification of that head is a separate gate; its results belong in the pull request.

Profiles now expose direct `tools on|off [--profile ID] PACK/TOOL` and a terminal saved-profile picker. Component preferences remain independently saved behind the whole-pack gate. Reviewed native model selectors retain their original requested identifier before launch; actual model/account remain unknown. Both retry entry points validate the original stored request and indexed session/generation, then claim only a recorded attempt or reconcile an uncertain effect without replay.

## Local source and build checks

| Check | Result | Duration | Log SHA-256 |
| --- | --- | --- | --- |
| build | PASS | 2.832 s | `40e951de1e23e6a1bffbe3cf13b0f86632680165f084ff2e3d71bb9ff72bdc66` |
| normal | PASS | 114.875 s | `b057de919c97ca7bbc1830e93d0e95335ee7cbfea94b487cf993eda3846e283e` |
| race | PASS | 245.334 s | `f049ee2ebee4b4d471fb51dd905027d6eae037a1c5cb5b2672025fdb7c859e02` |
| vet | PASS | 0.689 s | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| site | PASS | 0.254 s | `a62e8743f32638bae8b454ef3a57d3105634595da1c78a3c98aa22e7ff782206` |

The normal and race suites use `-count=1 -p 1 -tags library_registry ./...`; vet uses the same build tag. `make build` produced all four development binaries, and the static documentation suite passed twelve checks. Qualification receipt SHA-256: `41994cbe0734e9c13ecdaa2e9f36e4be3683f3921a3d24219d0fdda2595f87ec`. These are source/development-build gates, separate from installed release qualification.

The initial `9afc6a0` full race run failed an unchanged SSH overflow fixture: a case with a 50 ms execution budget returned timeout with no captured output. Its original log remains retained (SHA-256 `729ad8cbaddca9177f93019c4755b2a13e68d91f95cac4df787b12f5d9405f89`). Startup/scheduling contention is a source-based explanation, not an independently measured cause. One unchanged focused pass was diagnostic only. Independent review approved a test-only repair separating overflow classification from startup cancellation: the original 50 ms timeout control, one-second wall assertion, byte caps and production code stay unchanged, with exact-limit/+1 cancellation checks added. The checks above are fresh qualification of the corrected source; they do not erase the initial failure.

## Compiled terminal controls

The exact compiled CLI SHA-256 is `4bbe83684be233874a22d10b28283181936f7651ddfda9c7a091103fb1f7b639`. Fifteen captures cover 120×32, 80×24 and 40×14: default pack enabled, saved-profile picker, review-only tool off, unchanged default tool on, and pack disabled with saved-on inactive preference. The direct compiled `tools on --profile review headroom/mcp` command restores the same pinned preference before the pack gate is disabled. All 45 captured PNG/text/ANSI digests were verified.

The product used a real private Registry and a deterministic private Unix inventory fixture. 9 GET `/v1/sessions` requests and zero inventory mutations were observed across the three runs. Each owned TUI quit with `q`, exited zero, was reaped without signals, and its server thread and fixture were cleaned up. PNGs rasterize captured terminal cells; they are not native-window screenshots. The [retained 80×24 capture bundle](../site/evidence/library-profiles-da42952/README.md) preserves the original receipt, SHA-256 `7a45a7c88b1a820af3fa1b790fdbb7f525d36a3ef329724334918bd804b11bdd`.

## Review and remaining gates

Independent finite source reviews covered profile/tool integration, requested-model projection/display, and the shared retry HTTP/client/CLI/store path. The final retry review matched nine frozen files to this clean implementation commit. Focused negative controls include every stored outcome against session-generation, indexed-generation and indexed-session mismatches through both retry entry points; unknown original operations create no intent. Guide review caught three documentation corrections included in the qualified source; the following documentation-only commit adds the exact receipts and screenshot links.

No native provider, model turn, tool invocation, daemon or native application was started by the capture run. This increment does not repeat earlier Headroom initialize/list health, prove native loading or accepted routing, or qualify new Gemini/Grok/OpenRouter lifecycle. The owner-private store remains trusted; canonical request and lifetime binding do not authenticate a same-owner rewrite. Native authority and credentials remain in their original harnesses. The [provider guide](../user/providers.md), [plugin guide](../user/plugins.md) and [support matrix](../user/support-matrix.md) retain these boundaries. Public v0.2.1 remains the installer target.

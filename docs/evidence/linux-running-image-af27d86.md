# Linux running-image and production-helper source qualification

Qualified source: `af27d86a3ec09aeb1e054f632b749a4f0f3ad4eb`; tracked tree `4d1addb6fa3da2cf91f7fc1e24d91b339b2d9c61`. This is a development source milestone, stacked on reusable-capabilities draft PR27 and published through [draft PR28](https://github.com/seanheiney/keelmux/pull/28). It does not extend the public v0.2.1 archives.

## Behavior and reviewed boundary

An open `/proc/PID/exe` descriptor retains the old executable object across an exec. A process start-time check alone cannot distinguish that change. The verifier now hashes through the retained descriptor with a 512 MiB actual-read bound, reopens the current kernel executable, compares its device/inode and stable size/mode/owner/group/mtime/ctime with the held object, and repeats boot/process lifetime checks. Changed, missing or unsafe evidence refuses. The [kernel proc documentation](https://docs.kernel.org/filesystems/proc.html) describes the executable link and process lifetime fields; the owned exec control below establishes the exercised behavior.

This is a checked-instant identity result. It does not prevent future exec or prove that no same-image ABA happened between checks, and it is not same-user process isolation. The typed channel and common controller authority remain separate.

Independent finite source review bound `stdio_image_linux.go` SHA256 `f9cdac00a5d39fc399ecc08533e15daac50bea9eea8884978c069d9f8474c4af` and `stdio_image_linux_test.go` SHA256 `35fac7f6d399ece7a63d33e3183fb6ebe0a81a4750baa1148334655790847234`, finding no remaining concrete issue in those files. The reviewer did not rerun runtime tests.

## Fresh checks and actual scope

Full local tagged normal and race suites, vet, four development binary builds and twelve documentation tests passed on macOS arm64 / Go1.26.6. Receipt SHA256: `10b5400ceca5a0556fe40300ca5953144ce230b9a2844fda52a1e4b71f4f9554`. Linux tests also crosscompiled with CGO disabled; this is compilation evidence only.

[Push source/packaging run](https://github.com/seanheiney/keelmux/actions/runs/38108287980) and [PR source/packaging run](https://github.com/seanheiney/keelmux/actions/runs/38108290634) each passed all six jobs. [Documentation build](https://github.com/seanheiney/keelmux/actions/runs/38108290642) passed; deployment was skipped. The PR tested merge `3af8b9f0d38e67a26fab8bf083f588c47eda1150`, with exactly the qualified source tree. All four source-job raw logs and checkout bindings are retained. Hosted receipt SHA256: `329706467e5964f843776e191282174be71707c105971376ddcb13af6105862b`.

Both Linux jobs explicitly ran and passed these controls:

- Actual owned shell-to-sleep exec after the initial executable FD is opened. PID/start time remains unchanged; the verifier rejects the sustained different executable.
- Stable identity and missing/denied evidence, changed boot/start, wrong digest, changed metadata, identical bytes on a new inode, oversized and empty objects.
- The normal compiled production supervisor and production image verifier with a deterministic Go protocol child: clean initialization, exact image/directories/lifetime, fresh-client retained reads, quiescence and original-operation durable stop retry. Seven fixed fixture requests were observed. The existing fixture cleanup predicate ran. No separate process-inventory/death attestation was retained for this new hosted control, so that predicate is not promoted to an independent exact cleanup receipt.

The last control is production-helper implementation plumbing. Its child is still a deterministic protocol fixture, not native Codex; its helper is a source-built executable, not a frozen installed release archive.

## Open gates and next product slice

`NativeProviderQualified` stays false. No native Codex stdio execution, model turn, login, approval answer, writer grant or semantic delivery occurred. Actual native provider loading, Gemini/Grok/OpenCode/OpenRouter lifecycle/model acceptance, installed stdio archive qualification and full independent-server Bouncer remain open.

Darwin preflight remains unavailable before launch intent/helper/child. Two finite independent primary Apple API assessments found code-directory identity distinct from the current whole-file SHA/backing-object contract. No parser, syscall adapter, dependency or Darwin production enablement was added. A future differently tagged proof needs its own reviewed contract and runtime qualification.

The requirements review identified an implementation gap: enrolled SSH peers implement inventory only, so the entry currently refuses routed attachment. A scoped home-owned read-only export/SSH bridge and captain watch/report/budget ledger have bounded contract proposals; neither is implemented or full-MVP acceptance. Existing inventory defaults and home authority remain intact. Original `8d19ea8` / `56e8040` failures and the separate paired macOS diagnostic are retained at their original revisions.

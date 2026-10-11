# Reusable capability CLI and TUI qualification

**Pass, development source only.** The compiled CLI passed 32 checks; six actual PTY captures passed state and visual review. These receipts bind source `b730500f5fcf1382c0e292c0a088e0f6ace9cf6f`, independently of the commit that publishes this documentation. Published v0.2.1 does not include these library controls.

| Binding | Value |
| --- | --- |
| Platform | macOS 26.7.1 (25G309), arm64 |
| Compiler | Go 1.26.6 |
| Source archive SHA-256 | `68e1d26982ecfe9694c0cd30930582c6c7c64618395f8e2f17f51a547dfad450` |
| Compiled CLI SHA-256, before and after | `10434ba0ad9da04af1509714c9fbf91a91a66b134501650d48a1db27749ae838` |
| [CLI receipt](../site/evidence/reusable-capabilities-b730500/cli-receipt.json) SHA-256 | `528dbb948f3ad6f355ac70cfa12be2e3d103f06e94dfaa599c4b0c903961fe56` |
| [TUI receipt](../site/evidence/reusable-capabilities-b730500/tui-receipt.json) SHA-256 | `2622da01c27f6842395eaf1f0e06379b7c5a6cd543b979c15edca4ce39c31117` |

## Compiled CLI: 32 checks

The actual executable used fresh private state and owned workspaces. Checks covered empty reads without state creation; bundled Headroom import; pack and component switches; independent profiles; disabled-tool refusal; ordinary availability checks without starting an executable; and preview, reviewed apply, status, idempotence, stale-plan refusal, removal after disabling, reapplication and explicit clear. The application fixture generated a Claude supplemental MCP file; no Claude process was started. The [user guide](../user/plugins.md) gives the commands; the CLI receipt names every check and expected exit.

A separate declared pack selected the existing isolated Headroom 0.40.0 installation for explicit `tools check --health`. It negotiated MCP `2025-11-25`, listed three tools, invoked none and reported complete cleanup. The child used a fresh private home/cache/temp, explicit offline environment references and a macOS deny-network wrapper. Ordinary health checks do not acquire that extra operating-system restriction automatically.

The installed public wheel SHA-256 was `f7b0186ad5e76d5c8f75e5de6c57e001637258ead75481575ccca4d57557f5fb`; 579 installed wheel files matched after the check. Python was 3.14.7 and the MCP SDK was 1.30.0. The bundled declaration separately records upstream source revision `976aa714ef3e19174dc0cb586cee7b20d064e4ef`; that revision does not prove the wheel's source provenance. No compression, token savings, native wrapper or proxy behavior was exercised.

The first helper attempt used a state path longer than the existing Unix-socket path limit and was refused. The successful run used a short owned fixture path; no product source changed to accommodate it. That failed setup remains recorded in the CLI receipt.

## TUI: six captures

The actual compiled switcher ran in owned PTYs at **120×32, 80×24 and 40×14**. Each size captured the pack enabled and its MCP component disabled. A deterministic local inventory endpoint supplied fixture rows; the Registry and profile mutations used real private files. The helper verified the saved states through the compiled CLI, observed one inventory GET and zero inventory mutations per size, then quit cleanly without signals. Owned fixture directories were removed.

All six images were inspected. At 40×14 the descriptor and pin truncate with an ellipsis and the footer wraps; controls and the loaded-state notice remain visible. This documentation includes only the two unmodified 80×24 PNGs and text equivalents:

- [Pack enabled](../site/evidence/reusable-capabilities-b730500/library-pack-on-80x24.png) · [text](../site/evidence/reusable-capabilities-b730500/library-pack-on-80x24.txt).
- [MCP component disabled](../site/evidence/reusable-capabilities-b730500/library-component-off-80x24.png) · [text](../site/evidence/reusable-capabilities-b730500/library-component-off-80x24.txt).

These are rasters of interpreted cells captured from the actual PTY, not screenshots of a native Terminal window. The complete TUI receipt preserves all six capture hashes. The site builder verifies the two published PNG/text pairs against that receipt and retains their original source identity separately from older site captures.

## Scope

Native loaded state remains **unknown**. Neither run launched a daemon or native provider, submitted a prompt, invoked an MCP tool, or made a model turn. No native session, live account, reload, permission acceptance or released artifact was qualified. The health result covers this selected installed server and protocol exchange; executable provenance and a portable security sandbox remain separate concerns.

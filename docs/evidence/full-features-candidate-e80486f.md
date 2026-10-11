# Fuller-feature candidate qualification: e80486f

Exact source: `e80486f0fc7601db0608efaf8370d7bebd08ec82`; source epoch `1791675713`. Date: 2026-10-10. This local v0.3.0 candidate was **held before tagging or publication**. Published v0.2.1 remains unchanged. A production correction requires a new clean source and fresh archive/runtime qualification.

The candidate contains group administration, contextual usage, pinned SSH inventory and read-only diagnostics, plus internal durable primary start fences. The structured native product launcher remains disabled. The earlier [compiled CLI and PTY captures](full-features-source-6a6b20c.md) retain their exact source scope.

| Gate | Result and scope |
| --- | --- |
| Local producer | Four clean schema-2 archives; a second complete build reproduced all four archives and checksums byte for byte |
| Local macOS source | Complete race suite and vet passed |
| Isolated Linux arm64 source | Complete offline race suite and vet passed, exact tracked bytes/modes preserved; slow-reader case 6.55 seconds |
| Hosted push | [Run 38095929681](https://github.com/seanheiney/keelmux/actions/runs/38095929681): Ubuntu source and four cross-build jobs passed; macOS source failed the unchanged eight-second slow-reader gate at 8.10 seconds |
| Hosted pull request | [Run 38095931729](https://github.com/seanheiney/keelmux/actions/runs/38095931729): all six jobs passed; this does not erase the separate failed push gate |
| Documentation | [Run 38095931762](https://github.com/seanheiney/keelmux/actions/runs/38095931762): build passed; deployment intentionally skipped on the source branch |
| Installed macOS arm64 | CLI 25 cases, installer 16 groups/11 exact-instance daemon stops, actual Codex 0.159.0/Claude Code 2.1.296 zero-turn lifecycle, and actual managed IRC 152 race checks passed |
| Installed Linux arm64 | CLI 25 cases, installer 16 groups/11 exact-instance daemon stops and native terminal fixture eight groups passed; native providers were not run |
| Installed SSH | Owned actual loopback SSH product workflow passed with all four installed hashes/loaded versions, strict host/principal/grant identity and exact cleanup; both homes are on one physical computer |
| Mattermost | Fresh owned real service passed eleven adapter and seven source-built Linux daemon groups; customer destinations and installed archive Mattermost behavior were not qualified |

The retained first installed SSH wrapper failed its whole fixture-HOME preservation assertion because the Go compiler created telemetry files there. Product child checks passed; exact original cleanup passed. A separate private compiler HOME repaired the harness without weakening whole-HOME, sentinel, source or executable checks. A fresh installed run then passed. This is a harness failure and correction, not a production SSH defect.

## Retained proof hashes

These hashes identify private original records; provider captures, account metadata, local paths and credentials are not published.

| Record | SHA-256 |
| --- | --- |
| macOS four-gate summary | `52838efc6ad555575488760e9f283d7f8cba152fd9dfbc5f40b300ee7fc792aa` |
| Linux qualification summary | `ee13cfde9ecc3072d550342605641e81d0dfb6e9903c2be3a2a1df510631d740` |
| Fresh installed SSH receipt | `4577b1e5603f5b404e53af523817f0fc97b6ed9da0e57ad44d1203b0ac2e8b5d` |
| Failed hosted macOS log | `7723159bc968ff0a386466b0348da1515a0cb6a44df7bd1b375468063273ea47` |

Four local archive hashes:

| Target | SHA-256 |
| --- | --- |
| keelmux_v0.3.0_darwin_amd64.tar.gz | `ecd348ae49e583fb1bc73a915bf36156303a5a635d6904e5321c445fa564185c` |
| keelmux_v0.3.0_darwin_arm64.tar.gz | `f5799f47fb3fde3a094171259701a22d26f13d81801c4a53a4a4ef437723111f` |
| keelmux_v0.3.0_linux_amd64.tar.gz | `85308244c7fff696ed13672894258de413873d31c7b3617eb504bbe965d12560` |
| keelmux_v0.3.0_linux_arm64.tar.gz | `a4874f1818b12eba1bf2c358984d6511e9442e8ec7da90d92163cda13fd0f2c4` |

## Release hold and remaining scope

The hosted throughput failure is a real gate failure. Its log and all candidate receipts remain retained. Investigation identified that the pinned terminal library ignores a requested zero scrollback size, leaving an unused main-screen history in the current-screen tracker. Any correction must preserve parsed visible cells, cursor/modes, terminal replies and raw output, with the original deadline unchanged. A passing rerun alone does not qualify the failed source.

No model turn or native Enter was submitted. Native provider semantics/account authority, a structured product launcher, real reboot/native resume, independent physical second-server proof, customer chat destinations, Slack live deployment and packaged amd64 provider behavior remain outside these receipts. All test-owned runtime processes and installations were cleaned up; native screen captures remain private for audit. Five broader Plane lanes remain In Progress.

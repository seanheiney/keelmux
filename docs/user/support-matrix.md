# Support matrix

Keelmux declares support per provider, version, platform, authentication mode and scenario. An installed CLI, a successful build and a working model turn establish different things.

## Published v0.2.1

[v0.2.1](https://github.com/seanheiney/keelmux/releases/tag/v0.2.1) is frozen at `71d831f05405b68fc80cb38132b321f3ba1e6cc2`. Its [qualification index](https://github.com/seanheiney/keelmux/releases/download/v0.2.1/QUALIFICATION.json) binds actual installed packages, hosted byte comparison, tagged workflow attestations and anonymous default installation to this source. The default runtime owns native PTYs and processes; tmux is an optional historical compatibility adapter.

| Scenario | Qualified evidence | Limit |
| --- | --- | --- |
| macOS arm64, Codex CLI 0.159.0 and Claude Code 2.1.296 | Installed native composer, unsubmitted Unicode/Escape/resize, read-only and writer fencing, switcher detach/return, same-execution recovery after daemon loss, exact cleanup | macOS 26.7.1; zero submitted model turns; effective model/account attribution and reboot resume unqualified |
| Linux arm64 native terminal backend | Installed fixture eight groups and eleven daemon CLI restarts, original authority and lost-response recovery | Nonroot pinned Debian 13.7 container; authored terminal fixtures, no vendor programs |
| Per-user installer, macOS arm64/Linux arm64 | Sixteen actual package groups on each platform, four loaded canonical binaries, default anonymous HTTPS install/uninstall, preserved authentication/state/profile sentinels | No automatic dependency installation or database downgrade |
| Managed Ergo 2.19.1, macOS arm64 | Actual installed three-thread/two-project workflow, 152 checks, exact distinct bot accounts, both restarts and owned cleanup | Loopback TLS/SASL; bounded live history and explicit gaps; public server deployment unqualified |
| Mattermost 11.11.1 | Owned real service, eleven adapter race groups and seven source-built native daemon/CLI groups | Customer server and packaged TUI journey unqualified; daemon fixture has no native session process |
| Slack | Strict protocol fixtures and durable authored-message source gates | Real customer service remains unqualified |
| macOS amd64/Linux amd64 archives | Reproducible cross-builds and exact-source provenance | Packaged native runtime/provider acceptance unqualified |
| Hosted source and release | macOS arm64/Ubuntu amd64 race/vet, installer controls and archive smoke; all four v0.2.1 archives match local qualified bytes; five exact-tag attestations verified | Source/archive smoke does not establish native provider or model-turn support on that runner |

The communication surface contains authored notes and explicit replies. Incoming messages cannot press terminal keys, approve tools or submit model work. Lost acknowledgements remain uncertain and are reconciled without automatic replay.

## Historical development evidence

The published v0.1.0 terminal-runtime evidence covers **macOS 26.7.1 on Apple Silicon**, Go 1.26.6 and tmux 3.7b, with installed **Codex CLI 0.159.0** and **Claude Code 2.1.296**. Four archive targets build: macOS/Linux, arm64/amd64. Independent macOS arm64 packaged installation and authority-refusal acceptance passed on source `0be153b2ae2fc2ad0d47120287bcdce2f8f3cc44`: [package receipt](../evidence/keelmux-release-macos-independent.md). Linux arm64 terminal-backend acceptance runs actual packaged binaries in a pinned Debian container with explicit fixture programs; it does not prove native Linux Codex or Claude behavior. Other provider versions and build-only targets need their own receipts. The tagged release attaches its final exact-source qualification separately.

### Independent native runtime development

The development default owns native PTY/process supervision. Optional tmux compatibility only addresses previously recorded executions; a failed native launch cannot fall back to tmux. See the [architecture decision](../architecture/0002-native-runtime-ownership.md).

Integrated source `7f13006` passed full race tests and vet. Its actual daemon test uses a PATH containing only Keelmux binaries, kills and restarts the control daemon, and verifies the same child PID/start/execution survives. Native transport tests exercise Unicode, resize, Escape, literal Ctrl-G, detach, caller terminal restoration and reconnect using the real helper. Independent terminal review found and repaired control-framing, split-grapheme and special-file refusal defects before this integration.

Real native-supervised pinned Ergo persistence and private services accounts passed race tests and vet separately on macOS arm64 and isolated nonroot Linux arm64. Strict real Codex 0.159.0 and Claude 2.1.296 terminal acceptance passed at `4c265651`, with tmux absent, no startup-query exception and zero model turns. The [native receipt](../evidence/native-supervisor-4c265651-darwin-arm64.json) records actual composer/Unicode/resize, writer/readonly fencing, detach/reconnect and exact daemon-loss recovery.

The clean schema 2 candidate at `db94705` passed installed macOS arm64 native-provider qualification. Its Linux arm64 archive passed native fixture 8 groups, installer 16 groups and four-loaded-binary smoke in an owned isolated nonroot environment. [Exact candidate evidence](../evidence/native-release-linux-db94705-candidate.md) also records successful historical schema 1 installer compatibility and exact-instance daemon cleanup. These candidate gates do not qualify subsequent project routing or terminal changes, final hosted archives, public downloads, Linux native providers or amd64 runtime behavior. The historical provider receipts below qualify the v0.1.0 tmux runtime.

## Historical v0.1.0 terminal core

| Capability | Evidence | Limit |
| --- | --- | --- |
| Native launch and exact inherited link | Both real CLIs entered from owned tmux terminals | Link proves owned product launch context; effective native conversation/account/model identity remains unknown |
| Full native terminal attach | Real composer rendering, unsubmitted Unicode, Escape and resize | No authenticated model prompt was submitted in these acceptance runs |
| Detach and reconnect | Ctrl-G d returned to the actual TUI; same execution/PID/pane on return | Requires surviving host and terminal runtime |
| One cooperating managed writer | Competing writer, forged release and stale generation refused | Same OS user can use its own direct terminal routes |
| Read-only attachment | Input and size changes refused on the managed viewer path | A native viewer is not a semantic history API |
| TUI and daemon loss | Actual SIGKILL and reconciliation retained exact surviving terminals | Host reboot/provider restart/native resume are separate unfinished gates |
| Lost launch/attach/stop response | Durable operation identity and exact retry checked | Unknown effects are not automatically replayed |
| Optional groups, view state and local drafts | Ten-session acceptance used two real programs and eight explicit fixtures | Drafts remain local and unsent |
| Owned selected skills | Installation/removal source tests; native Codex and Claude discovery observed | No skill invocation or model instruction loading was submitted in discovery checks |

The original accepted implementation is `4b36f4d773a31388c7ec8ade146eb1929a8d2a19`: [independent native receipt](../evidence/2026-10-10-independent-repaired-native.md). A later independent legacy-terminal regression at `a0787357dc8c8cb3749deca43074fd4beafff1ca` passed all eight local groups and three native gateway groups: [regression receipt](../evidence/2026-10-10-broker-core-regression.md). Later source changes require their own release qualification; these receipts do not automatically certify a renamed or packaged release.

## Release qualification

The [historical tagged release](https://github.com/seanheiney/keelmux/releases/tag/v0.1.0) supplies machine receipts for the final archives, loaded versions, actual installation and supported runtime checks. Compare their artifact source commit and archive digest with `RELEASE.json` and `SHA256SUMS`; historical receipts above qualify only their own source. macOS amd64 and Linux amd64 are build targets until an actual runtime receipt establishes more. Native authenticated turns, independent remote servers and host reboot remain outside the initial terminal-runtime claim.

## Private entry

The following real-provider entry receipts qualify the historical tmux runtime. No newly qualified v0.2.1 native-provider gateway journey or independent second server is claimed.

| Capability | Evidence | Limit |
| --- | --- | --- |
| One entry routes native attachments | Both real CLIs reached through actual home daemon → entry → client → PTY | Two homes on one physical Mac |
| Scoped inventory and grant recovery | Home locators scrubbed; exact acquisition survives lost acknowledgement and entry restart | Explicit private configuration; no automatic join |
| Slow/unavailable home | Healthy partial inventory and actual healthy-home TUI attachment checked | Not proof of remote network/SSH authentication or authority failover |

[Entry authority evidence](../evidence/2026-10-10-gateway-hardened.md) and [silent-home evidence](../evidence/2026-10-10-gateway-silent-home-latency.md) identify exact scenarios and source.

## Codex broker experiment

Strict default-deny policy, a private Unix WebSocket relay and a durable actor sharing the existing writer reservation exist on the experimental source. Unit/fixture acceptance and controlled zero-turn native work qualify those components. The [native helper qualification](../evidence/Codex-broker-native-2026-10-10.md) records its exact scope and failures.

The helper is not a product app-server launcher. Its isolated no-auth custom provider, fixture thread creation and private test homes are not product defaults. Product native resume, daemon/reboot recovery, authenticated turns, nonempty history, live approval routing, semantic writer handover and model/account/usage attribution remain unproved. `SemanticPrompt` stays false.

The source-integrated primary controller has independent private-journal/native-home review and passing source race/vet. It still requires real adapters for the existing common lifecycle gate and authenticated upstream. The [controller review](../evidence/Codex-controller-peer-review-2026-10-10.md) qualifies injected boundary fixtures only; no product launcher or semantic capability is enabled.

## Authored communications

v0.2.1 includes immutable authenticated Mattermost, Slack and IRC bindings, a durable authored inbox, explicit replies and read-only receipt reconciliation. The [final qualification](https://github.com/seanheiney/keelmux/releases/download/v0.2.1/QUALIFICATION.json) records fresh owned Mattermost service/source daemon proof and the actual installed managed IRC project workflow at `71d831f05405b68fc80cb38132b321f3ba1e6cc2`. Slack has protocol-fixture evidence. Retained thread claims require their original private account authority, and unknown registration or delivery effects cannot authorize replay. Historical thread-channel receipts do not qualify the project-channel mapping.

Remote text never submits terminal input, approves tools or starts a model turn. The owned-service checks do not qualify a customer workspace. The [channel guide](channels.md) and [managed IRC guide](managed-irc.md) describe exact configuration, behavior and recovery limits.

## Outside the current runtime claim

Grok native lifecycle, independent-server enrollment, customer Mattermost/IRC validation, shared login repair, subscription routing, usage totals, model/account/harness/host continuity, optional captains and independent-user accessibility/simplicity pilots remain on the [roadmap](roadmap.md). Source fixtures or a planned command do not establish these features.

Before a release, record its exact SHA, artifacts, OS/CPU, native versions/authentication mode, commands, passing scenarios and all unsupported/not-run cases. Installation, source checks, native runtime acceptance and public publication are separate gates.

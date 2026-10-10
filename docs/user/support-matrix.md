# Support matrix

Keelmux declares support per provider, version, platform, authentication mode and scenario. An installed CLI, a successful build and a working model turn establish different things.

The current runtime evidence covers **macOS 26.7.1 on Apple Silicon**, Go 1.26.6 and tmux 3.7b, with installed **Codex CLI 0.159.0** and **Claude Code 2.1.296**. Linux amd64 cross-compilation passed; Linux runtime remains untested. Other versions and platforms need their own receipts.

## Terminal core

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

## Private entry

| Capability | Evidence | Limit |
| --- | --- | --- |
| One entry routes native attachments | Both real CLIs reached through actual home daemon → entry → client → PTY | Two homes on one physical Mac |
| Scoped inventory and grant recovery | Home locators scrubbed; exact acquisition survives lost acknowledgement and entry restart | Explicit private configuration; no automatic join |
| Slow/unavailable home | Healthy partial inventory and actual healthy-home TUI attachment checked | Not proof of remote network/SSH authentication or authority failover |

[Entry authority evidence](../evidence/2026-10-10-gateway-hardened.md) and [silent-home evidence](../evidence/2026-10-10-gateway-silent-home-latency.md) identify exact scenarios and source.

## Codex broker experiment

Strict default-deny policy, a private Unix WebSocket relay and a durable actor sharing the existing writer reservation exist on the experimental source. Unit/fixture acceptance and controlled zero-turn native work qualify those components. The [native helper qualification](../evidence/Codex-broker-native-2026-10-10.md) records its exact scope and failures.

The helper is not a product app-server launcher. Its isolated no-auth custom provider, fixture thread creation and private test homes are not product defaults. Product native resume, daemon/reboot recovery, authenticated turns, nonempty history, live approval routing, semantic writer handover and model/account/usage attribution remain unproved. `SemanticPrompt` stays false.

## Outside the current runtime claim

Grok native lifecycle, independent-server enrollment, Mattermost/IRC delivery, shared login repair, subscription routing, usage totals, model/account/harness/host continuity, optional captains and independent-user accessibility/simplicity pilots remain on the [roadmap](roadmap.md). Source fixtures or a planned command do not establish these features.

Before a release, record its exact SHA, artifacts, OS/CPU, native versions/authentication mode, commands, passing scenarios and all unsupported/not-run cases. Installation, source checks, native runtime acceptance and public publication are separate gates.

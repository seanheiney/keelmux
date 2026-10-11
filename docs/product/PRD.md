# Keelmux product requirements

Updated 2026-10-11. Owner: Sean Heiney. Development is paused at the owner's request because credits are exhausted. This PRD records the current requirements, implemented scope and remaining acceptance; it does not authorize restarting development.

## Purpose and users

Keelmux is an open-source, terminal-first backplane for persistent agent sessions. A solo operator can find, attach to and return to real model CLIs, keep durable addressed threads across supported communication clients, and reuse capability packs across providers. Company/product grouping, remote hosts and optional captains extend that daily loop without becoming onboarding requirements.

The default home is a clean session switcher with contextual inspection and repair. Native vendor CLIs retain conversation semantics, login realms, model selection and permissions. Local operation needs no Keelmux account or public listener. macOS and Linux are the initial platforms.

## Canonical specification and decisions

The [product vision snapshot](PRODUCT_SPEC.md) and [first-release plan snapshot](../build/BUILD_PLAN.md) preserve the imported October 10 briefs. This PRD reconciles later owner instructions and supersedes their proposed tmux-first runtime and undecided IRC path. Architecture decisions are in [native runtime ownership](../architecture/0002-native-runtime-ownership.md), [reusable capabilities](../architecture/0003-reusable-capability-library.md), [SSH observation](../architecture/0004-bouncer-read-bridge.md) and [captain ledger](../architecture/0005-captain-ledger.md). [Implementation status](../build/STATUS.md), the [build queue](../build/TASKS.md) and the [support matrix](../user/support-matrix.md) hold scoped evidence.

## Requirements and acceptance

| ID | Requirement and acceptance | Closeout state | Plane |
| --- | --- | --- | --- |
| P01 | Own PTY/process supervision, attachment, input fencing and daemon recovery. Preserve provider keys, Unicode, resizing, Ctrl-C and detach/reconnect. tmux is optional compatibility; Herdr is conditional on comparative proof. | Published v0.2.1 native runtime; newer-source/provider proof remains separately scoped. | #8 |
| P02 | A clean TUI session switcher, remembered selection/groups/drafts, keyboard navigation, narrow/plain output and contextual repair. Navigation and search do not invoke models. | Published core; newer controls remain candidate source. | #4, #10, #16 |
| P03 | One pasteable installer, four macOS/Linux architecture archives, checksum/provenance verification, atomic per-user install/reinstall/uninstall and preserved native credentials/state. | Published v0.2.1; v0.3.0 draft artifacts are locally built, checksum-verified and unpublished. | #1, #7 |
| P04 | Bind durable threads to Mattermost first, Slack or IRC using exact endpoint/realm/channel/sender identities, ordered inbox/outbox and no echo/replay duplication. Authored chat delivery and model submission are separate capabilities. | Owned Mattermost and installed IRC scope published; Slack fixture scope and customer destinations remain distinct. | #2 |
| P05 | Optionally configure, run and manage a private owned Ergo IRCD. Each project is a channel; each agent thread has a stable IRC user/bot account and nick. Recover identities across restarts without replaying registration or posts. | Published v0.2.1 project/thread scope; broader acceptance stays explicit. | #3 |
| P06 | Reusable plugins containing model-neutral rules, skills and tool declarations; verified bounded imports, immutable revisions and explicit component selection. Importing a pack does not execute it. | Candidate source and library-only journeys qualified; native loading remains open. | #12, #13 |
| P07 | Pin-preserving independent profiles and tool on/off switches; preview/apply/remove only owned unchanged workspace files. Refuse collisions, drift, pending and partial application. Compare saved configuration, applied receipt and immutable session launch snapshot. | CLI/TUI controls and read-only session status implemented at 2bf766e. Native loading/permissions remain unknown; saved switches do not revoke running tools. | #12, #16 |
| P08 | Headroom is an optional selectable tool with an explicitly installed executable and bounded initialize/list health. No hidden wrapper/proxy, credential routing or guaranteed savings. Tool switches currently select whole MCP servers, not individual MCP methods. | Candidate source; health observations do not prove model/tool use or savings. | #14 |
| P09 | Support native Gemini and Grok launch contracts plus OpenRouter sessions through OpenCode. Keep requested and provider-accepted model/account identities distinct. Reuse supported skills through explicit versioned mappings. | Version-reviewed candidate launch contracts; authenticated new-provider lifecycle, accepted models and native loading remain open. Grok native MCP is unsupported. | #15 |
| P10 | One permitted inventory through one entry across enrolled hosts, with pinned host identity, scoped observation, reconnect/revocation and honest task-home availability. No implicit migration or writer authority. | Owned same-machine SSH journeys qualified; two independent physical hosts and full Bouncer MVP remain open. | #6 |
| P11 | Optional company/product hierarchy and overlapping project/session/thread membership, remembered place and deduplicated counts. Archive/remove preserves work and drafts. | Newer development source; publication and broader integration remain separate. | #10 |
| P12 | Qualified usage/account/readiness observations with source, scope, coverage and freshness; deduplicate request/attempt records and keep token/quota dimensions separate from exact money. Never infer entitlement from terminal text. | Newer usage/diagnostic source; account routing and subscription continuity remain open. | #9, #11 |
| P13 | Optional scoped group/global advice captains with durable reports/cursors, lifetime budgets and zero model activity for unchanged idle observation. | Store-only prerequisite integrated; authenticated runner/model/scheduler acceptance remains open. | #17 |
| P14 | Structured native control must preserve exact execution authority, durable intent and supported request-specific semantics. Native terminal input is not a universal chat/approval API. | Reviewed controller prerequisites; authenticated activation and semantic/model gates remain open. | #5 |
| P15 | Excellent GitHub documentation, runnable guides, actual scoped screenshots, troubleshooting, architecture, upgrade/removal and truthful support/release labels. Static reference site only; any future product GUI is native. | Published documentation baseline plus candidate guides; latest site deployment remains separate from a successful build. | #4, #16 |
| P16 | Preserve the original native capacity workload, assertions and deadlines; any performance correction needs matched measurement and independent review. | Historical macOS SlowReader failure retained with cause unknown; isolated candidate is unmeasured and unintegrated. | #18 |

Fast-food deal discovery was posted to this conversation in error and is excluded from the product and its project tracking.

## Architecture and trust boundaries

- Stable Host/Work/Thread/Lane identities are distinct from execution generation, native conversation, process, terminal and attachment identifiers.
- The home daemon owns the durable registry/journal and scoped authority. A detached supervisor owns a managed execution's PTY/process lifetime. SQLite is local to each authority; servers do not share its file.
- Observation, bridging and management are separate modes. Linking an arbitrary terminal does not acquire its PTY or grant semantic control. A host owner can still access their own processes; cooperating writer arbitration is not a sandbox.
- Record operation intent before side effects. Distinguish persisted, attempted, accepted, completed, failed and uncertain outcomes. Reconcile uncertainty rather than blindly replaying external effects.
- Remote/entry inventory never grants local filesystem authority from a matching wire HostID. Capability inspection is lazy, selected-session, bounded, read-only and rejects stale identity/snapshot results.
- Installed, selected, applied, launch-declared, native-loaded and currently permitted capabilities are separate states. Exact file/digest agreement cannot prove vendor loading or permission acceptance.
- Provider credentials remain in their native local realm. No hidden paid API fallback, permission expansion or account/host migration accompanies a repair.
- Client disconnect, daemon restart, host reboot, native resume and context handoff have separate guarantees and proof. A reboot does not preserve arbitrary processes.

## Closeout evidence and release ledger

Public stable is [v0.2.1](https://github.com/seanheiney/keelmux/releases/tag/v0.2.1), source `71d831f`. The default one-line installer selects the latest published stable release:

```sh
curl -fsSL https://raw.githubusercontent.com/seanheiney/keelmux/main/install.sh | sh
```

Candidate source is `2bf766ec7e583351e0356491995dd4f8e3bcb883`, tree `16080fe8f2bbee621868812cc69fe1075d4ba53d`, in [draft PR32](https://github.com/seanheiney/keelmux/pull/32). The [v0.3.0 closeout candidate](https://github.com/seanheiney/keelmux/releases/tag/untagged-22b092a8bb113d67aeef) is an unpublished draft prerelease. Its four macOS/Linux amd64/arm64 archives and SHA256SUMS have verified remote sizes/digests; they were built locally and are not release-workflow attested artifacts.

Local normal/race source suites, vet, focused read-only/adoption controls, four VCS-stamped binary builds, documentation checks/build, installer controls, package identity/cancellation controls and actual macOS archive install/reinstall/version/uninstall smoke passed. Focused controls passed 34 top-level tests plus 73 subtests. Receipts identify source-suite caching and bind precommit checks to the committed Go files and user guide. These controls did not qualify new native provider/model/tool behavior.

Both exact-head six-job hosted source workflows now passed: [push](https://github.com/seanheiney/keelmux/actions/runs/38127062967) and [PR](https://github.com/seanheiney/keelmux/actions/runs/38127066318). The [documentation workflow](https://github.com/seanheiney/keelmux/actions/runs/38127082506) passed its build; deployment was skipped. This refresh supersedes the earlier closeout's pending CI snapshot, without changing historical failed evidence or qualifying an attested release.

## Open gates and resume order

Development lanes and the captain remain paused. Unfinished Plane acceptance issues belong in Backlog for explicit resumption; previously completed work stays completed. Implementation and source checks alone do not close broader feature acceptance.

1. Review and integrate the existing draft PR stack. The release workflow requires the exact tagged source to be on main.
2. Complete remaining native/new-provider capability loading, authenticated model acceptance, structured controller, two-host and captain runtime gates for each claimed supported combination.
3. Resolve the separately tracked native capacity acceptance using unchanged original thresholds and retained failures; source-suite passes do not establish its historical cause.
4. Complete exact-source package/runtime/Linux archive smoke and attestation gates, then review publication. The workflow refuses replacing an existing release: reconcile the v0.3.0 draft explicitly or choose a fresh unused version.
5. Verify live documentation deployment and supported customer channel bindings before claiming them. Update the support matrix and acceptance issues with their own exact evidence.

Goldeneye [Keelmux Plane](https://plane.local.sean.network/goldeneye/projects/307ddb14-3cca-4188-b5b1-6299c45756ec) is the project system of record, mirrored by GitHub and the build queue. Documentation maintenance does not resume the product build, run model/provider probes or publish the held draft.

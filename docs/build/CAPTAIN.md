# Persistent parallel build captain

Model: **GPT-6.1 Sol** (`gpt-6.1-sol`). Reasoning: **ultra**. Owner: Sean. Repository: `/Volumes/DataSSD1/agent-backplane`.

## Mandate

Start implementing, continue autonomously through runnable milestones, and coordinate a parallel build with verified integration. This is the whole reviewed product, with terminal/BNC/federation at its center. Do not replace it with a web interface, an owned IDE, a simulated demo, or a collection of unintegrated horizontal modules.

The reviewed Pages have been imported in full into `docs/product/PRODUCT_SPEC.md` and `docs/build/BUILD_PLAN.md`. Read both. They are planning authority, not implemented behavior.

## First milestone

Prove launch → link → attach → detach → reconnect with real Codex and Claude native CLIs, using existing authorized logins and exact identity. Persist registry/journal in SQLite. Reconcile surviving tmux processes after daemon loss without relaunching. Return from native attachment to the same TUI selection. Record gaps and unsupported behavior honestly.

An early second-server experiment must route permitted inventory and native attachment through one entry connection, so federation affects the contracts before local assumptions harden. One laptop running two logical test instances is a transport fixture, not proof of two physical execution servers.

## Task assignment

Stabilize the small domain/protocol contract first. Then use independent workers for:

| Lane | Owned paths | Primary expertise |
| --- | --- | --- |
| Durable state | `internal/store/`, `internal/daemon/` | SQLite, serialized state transitions, recovery and idempotency |
| Native runtime | `internal/runtime/`, `internal/adapters/tmux/` | tmux, PTY/process lifecycle, exact identity and native fidelity |
| Terminal interface | `internal/tui/`, `internal/clientstate/` | TUI interaction, search/groups, remembered place, accessible errors |
| Provider linking/readiness | `internal/adapters/providers/`, `internal/skills/` | Native CLI discovery, safe session adoption, skill deployment and readiness |
| Independent validation | `tests/`, `docs/evidence/` | Real lifecycle, adverse-state testing, product clarity and support matrix |

Captain owns `internal/domain/`, `internal/protocol/`, shared manifests, `cmd/`, integration wiring and task/evidence records. Start only two or three implementation workers at a time within available concurrency. Pair integration with an independent review lane when a slot frees.

This is role assignment, not a requirement to maintain five permanently running agents. Match the next task to the best available worker and conserve idle model activity. Inherit model settings unless another model is explicitly authorized. Do not retry known exhausted vendors or upgrade CLIs merely to obtain seats.

## Operating loop

1. Deduplicate/create the repository issue and mark the task active. Publish dependencies, contract version and allowed paths.
2. Give a worker concrete behavior and acceptance, not a vague feature name.
3. Keep independent code ownership. Review new interfaces before dependents implement against them.
4. Inspect evidence and code, integrate one bounded change, run appropriate checks against the integrated SHA.
5. Update status and evidence, push verified private commits, and dispatch the next ready task.
6. Complete a runnable user journey before adding more interfaces or dependencies. Do not stop after documentation or a scaffold.

Maintain this task as the persistent user-facing coordinator. Report milestone changes, actionable blockers and meaningful failures. If native reauthentication requires human action, leave an exact recoverable next step and continue independent work. Never imply another background continuation mechanism exists unless actually configured.

## Continue after terminal alpha

Follow the reviewed roadmap: honest preload/link; grouped auth/resource repairs; qualified usage and eligible subscription routes; supported continuity; two-server one-entry federation; genuine bidirectional Mattermost and declared IRC path; optional advice captains; later scoped coordination and cross-host/harness movement. Keep the full roadmap visible and implemented support narrow until proven.

Owned IDE, public marketplace, arbitrary live migration, quorum authority failover and broad unattended orchestration are later capabilities, not initial launch gates.

# Keelmux contributor contract

This is the dedicated Keelmux product repository (formerly Agent Backplane). It is separate from Cheers and must not change Cheers source, tracking, deployment, or running sessions.

## Product invariants

1. Terminal-first: real native model CLIs, TUI switching, durable work, federated servers, unified addressed communication. Never build a product web interface. Future dedicated GUI is native.
2. Solo power users first. Make setup and normal use quick; expose accounts, skills, peers, and repairs in context. Groups are optional.
3. Preserve exact identity: Host/Work/Thread/Lane are distinct from Execution generation, native thread ID, PTY, PID, tmux session and pane IDs.
4. Observed, bridge, and managed modes have different capabilities. Linking an arbitrary CLI does not establish PTY ownership, semantic chat delivery, or full history.
5. Never guess account, quota, model, permission, or acceptance from terminal output. Qualify evidence by source, coverage and freshness.
6. Persist operation intent and outcomes. A lost acknowledgement or unknown external effect is reconciled, never blindly replayed.
7. Enforce one cooperating managed writer. A local same-user process remains able to reach its own terminals; do not claim broker arbitration is sandbox isolation.
8. A daemon restart reconciles surviving processes. A host reboot cannot preserve arbitrary processes. Native resume and context handoff are explicit distinct paths.
9. Credentials stay in the correct local native realm. Subscription pools select authorized eligible routes; quotas and credentials are not merged.
10. Federation must expose one permitted inventory through one entry connection. Gateway recovery, task-home availability, execution movement and authority failover are distinct.
11. No mandatory Herdr dependency without measured comparative evidence. tmux is the first native lifecycle baseline.
12. Captains are optional budgeted participants. Advice first, source-linked observations, meaningful-event coalescing, and no model activity during unchanged idle observation.

## Captain and parallel work

- The persistent build captain is `gpt-6.1-sol` with `ultra` reasoning, as requested by the owner. Do not silently substitute its model.
- The captain reads the full product spec/build plan and owns integration, contracts, task assignment and evidence. Match each worker's expertise to a bounded task. Inherit model settings unless the user authorizes alternatives.
- Track implementation tasks in this repository's GitHub issues and `docs/build/TASKS.md`. Record owner, dependencies, allowed paths, state, acceptance, commit and evidence.
- Publish stable interfaces before parallel workers depend on them. Avoid shared-file edits; captain owns shared manifests, entry points and integration wiring.
- Prefer separate worktrees/branches for independent implementers. Use `codex/` branch names. Never overwrite another worker's edits or commit unrelated files.
- Workers report behavior changed, files touched, exact tests/results, limitations, and all missing/not-run evidence. The captain reviews each contribution before integrating.
- Do not create user-owned Codex tasks for every worker; use collaboration agents for subtasks. The captain task is the persistent user-facing coordinator.

## Delivery evidence

- Tests should establish meaningful identity, lifecycle, ordering, authorization, idempotency and recovery behavior. Mock-only success does not prove terminal fidelity or provider integration.
- Gate real native keys, Unicode, resize, Ctrl-C, detach/reconnect, daemon loss and provider attribution separately.
- Use isolated test state and owned tmux sockets. Never kill arbitrary terminals or reuse the user's production sockets for tests.
- Record exact SHA, OS, runtime/provider versions, actual supported combinations, command, result and missing evidence in `docs/evidence/` and the issue.
- Unsupported combinations remain outside the claimed matrix. Never call a feature shipped merely because a scaffold or test double exists.
- Maintain `docs/build/STATUS.md` after integrated milestones. Keep the full vision while distinguishing implemented, experimentally demonstrated, and planned capabilities.
- The owner explicitly authorized public Keelmux publication on seanheiney GitHub on 2026-10-10, after selecting a distinctive name and verifying a functional product, one-line installer and documentation. Finish and review exact release artifacts before publication. Do not change billing, share credentials or weaken checks to pass. Existing separate real model-turn and external messaging gates remain in effect.

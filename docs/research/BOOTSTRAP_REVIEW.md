# Independent bootstrap reviews

Recorded 2026-10-10. Two internal agents independently reviewed runtime and terminal-interface boundaries before implementation. These are recommendations, not executed product evidence or model-company endorsements.

## Runtime conclusions

- One user-level daemon, stable domain/protocol, SQLite journal/registry, a dedicated tmux socket, and thin clients.
- Stable Host/Work/Thread/Lane IDs are distinct from execution, native conversation and runtime locators.
- Persist launch intent and idempotency before side effects. On daemon restart, reconcile exact surviving markers rather than relaunching.
- tmux loss is a visible unavailable/ended execution. Screen replay is not process recovery.
- Broker writer arbitration protects cooperating clients; same-user direct tmux access remains outside universal enforcement.
- Start with actual Codex and Claude. Semantic dispatch and identity detection need proven provider adapters, not generic send-keys or output guessing.
- Make the two-host transport experiment early, before local assumptions become permanent.

## TUI conclusions

- One default switcher for real work. Optional company/product picker, search, state, host, context detail and enter-to-attach.
- Native attachment suspends the TUI and returns after detach; avoid reimplementing terminal rendering in a pane.
- Restore group, selected work, supported surface and available anchors. Drafts start when genuine semantic delivery exists.
- Observed-only registrations expose useful supported actions and an adoption path.
- Narrow screens keep work and state; supplementary group/host columns can disappear. No status tile dashboard or empty chat pane.
- Attention and repair are contextual and deduplicated. Events never steal focus.

## Parallel boundary

Captain stabilizes domain/protocol and owns entry points/manifests/integration. Workers own durable state, runtime, TUI/client state, provider/skills, and independent verification in disjoint paths. Each handoff records exact checks and missing evidence.

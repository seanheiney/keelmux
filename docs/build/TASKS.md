# Build queue

GitHub issues are the task records for this separate product repository. The captain must add exact issue links and keep ownership/state current. No Cheers issue is reused.

| ID | Task | State | Owner | Depends on | Acceptance |
| --- | --- | --- | --- | --- | --- |
| BP-001 | Repository and reviewed spec import | Complete; private remote verified | Bootstrap coordinator | — | Full documents saved; clean initial commit; private remote |
| BP-002 | Domain/protocol contracts and runnable build [#1](https://github.com/seanheiney/agent-backplane/issues/1) | Integrated; build/race/vet pass | Captain; codex/captain-core | BP-001 | Explicit IDs/capabilities/outcomes; reproducible Go build and meaningful tests |
| BP-003 | Durable registry, journal and daemon [#2](https://github.com/seanheiney/agent-backplane/issues/2) | Accepted local scope; current eight-group native matrix passed | durable_state; captain; frozen 4b36f4d | BP-002 | Serialized SQLite mutations; intents/outcomes; restart reconciliation |
| BP-004 | Dedicated tmux lifecycle and native attach [#3](https://github.com/seanheiney/agent-backplane/issues/3) | Accepted Codex/Claude local scope; native gateway journeys passed | native_runtime; captain; frozen 4b36f4d | BP-002 | Exact markers; survivor discovery; native attach/detach; no duplicate start |
| BP-005 | TUI switcher and remembered groups/place [#4](https://github.com/seanheiney/agent-backplane/issues/4) | Accepted current remembered-place/ten-session scope | terminal_interface; integrated_acceptance; frozen 4b36f4d | BP-002 | Real inventory; find/open; optional groups; restored selection; clear capability states |
| BP-006 | Provider discovery, link and skill preload [#5](https://github.com/seanheiney/agent-backplane/issues/5) | Owned setup/removal integrated; native discovery observed; invocation pending | terminal_interface; captain d097a39 | BP-002 | Codex/Claude exact linking; truthful modes; owned config preserved; repeatable install/remove |
| BP-007 | Integrated two-provider terminal alpha [#6](https://github.com/seanheiney/agent-backplane/issues/6) | Current local core accepted; full alpha skill/model/remote gates open | integrated_acceptance; codex/bp-acceptance; captain | BP-003–006 | Full native journey, daemon loss and adverse-state evidence against exact SHA |
| BP-008 | Two-server one-entry federation proof [#7](https://github.com/seanheiney/agent-backplane/issues/7) | Same-computer native/authority/latency gates passed; real host unselected | durable_state; native_runtime; integrated_acceptance; captain | BP-002, BP-004 | Authenticated scope, routed inventory/attach, reconnect/revoke, no false host migration |
| BP-009 | Bidirectional communication thread bridge | Waiting | Communication worker | BP-007, BP-008 | Addressed Mattermost path then declared IRC mode; correct delivery/replay/input arbitration |
| BP-010 | Readiness, auth repair, usage and bounded continuity | Waiting | Continuity worker | BP-007 | Shared repair; qualified meters; eligible route; one task/thread; uncertain effects reconciled |
| BP-011 | Optional group and personal advice captains | Waiting | Coordination worker | BP-009, BP-010 | Scoped sourced reports; useful attention; accounted usage; zero unchanged-idle model activity |
| BP-012 | Codex native/semantic authority broker [#12](https://github.com/seanheiney/agent-backplane/issues/12) | Experimental actor/policy/relay integrated; authority repairs and native envelope compatibility pending | codex_broker_contract; durable_state; integrated_acceptance; captain | BP-003, BP-004; prerequisite for BP-009 | Real zero-turn native broker resume; scoped observer; unified writer and durable race/uncertainty gates |

Tasks are implementation milestones, not shipped feature claims. Split them into small issues before parallel assignment. BP-008 remains an early experiment even while local terminal alpha is developed.

Implementation acceptance is frozen at `4b36f4d773a31388c7ec8ade146eb1929a8d2a19`; later test/document commits retain an empty implementation diff. Native acceptance checkout `498abebb` is implementation-equivalent and records its own executable/harness digests. See STATUS and exact-SHA evidence before interpreting any task as accepted. Closing the bounded local core issues does not close full alpha, remote federation or communication work.

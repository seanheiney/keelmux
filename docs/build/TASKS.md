# Build queue

GitHub issues are the task records for this separate product repository. The captain must add exact issue links and keep ownership/state current. No Cheers issue is reused.

| ID | Task | State | Owner | Depends on | Acceptance |
| --- | --- | --- | --- | --- | --- |
| BP-001 | Repository and reviewed spec import | Complete; private remote verified | Bootstrap coordinator | — | Full documents saved; clean initial commit; private remote |
| BP-002 | Domain/protocol contracts and runnable build [#1](https://github.com/seanheiney/agent-backplane/issues/1) | Integrated; build/race/vet pass | Captain; codex/captain-core | BP-001 | Explicit IDs/capabilities/outcomes; reproducible Go build and meaningful tests |
| BP-003 | Durable registry, journal and daemon [#2](https://github.com/seanheiney/agent-backplane/issues/2) | Independent local core matrix passed at 4bf0162 | durable_state 25122b1; captain b962535 | BP-002 | Serialized SQLite mutations; intents/outcomes; restart reconciliation |
| BP-004 | Dedicated tmux lifecycle and native attach [#3](https://github.com/seanheiney/agent-backplane/issues/3) | Independent two-provider core matrix passed | native_runtime 9b1c35f | BP-002 | Exact markers; survivor discovery; native attach/detach; no duplicate start |
| BP-005 | TUI switcher and remembered groups/place [#4](https://github.com/seanheiney/agent-backplane/issues/4) | Core remembered-place/ten-session matrix passed | terminal_interface c28e742 | BP-002 | Real inventory; find/open; optional groups; restored selection; clear capability states |
| BP-006 | Provider discovery, link and skill preload [#5](https://github.com/seanheiney/agent-backplane/issues/5) | Owned setup/removal integrated; native discovery observed; invocation pending | terminal_interface; captain d097a39 | BP-002 | Codex/Claude exact linking; truthful modes; owned config preserved; repeatable install/remove |
| BP-007 | Integrated two-provider terminal alpha [#6](https://github.com/seanheiney/agent-backplane/issues/6) | Core accepted at exact SHA; latest-source/setup qualification active | integrated_acceptance; codex/bp-acceptance; captain | BP-003–006 | Full native journey, daemon loss and adverse-state evidence against exact SHA |
| BP-008 | Two-server one-entry federation proof [#7](https://github.com/seanheiney/agent-backplane/issues/7) | Executable fixture active; authority/IO fixes required; real host unselected | durable_state; native_runtime; integrated_acceptance; captain | BP-002, BP-004 | Authenticated scope, routed inventory/attach, reconnect/revoke, no false host migration |
| BP-009 | Bidirectional communication thread bridge | Waiting | Communication worker | BP-007, BP-008 | Addressed Mattermost path then declared IRC mode; correct delivery/replay/input arbitration |
| BP-010 | Readiness, auth repair, usage and bounded continuity | Waiting | Continuity worker | BP-007 | Shared repair; qualified meters; eligible route; one task/thread; uncertain effects reconciled |
| BP-011 | Optional group and personal advice captains | Waiting | Coordination worker | BP-009, BP-010 | Scoped sourced reports; useful attention; accounted usage; zero unchanged-idle model activity |

Tasks are implementation milestones, not shipped feature claims. Split them into small issues before parallel assignment. BP-008 remains an early experiment even while local terminal alpha is developed.

Integrated contributions: TUI c28e742 → 2a98946; store/daemon 25122b1 → a40d15e; tmux 9b1c35f → 48dbd4d. Source b962535 passes the integrated build/check and fixes native-runtime inheritance of the registry authority lock. See STATUS and exact-SHA evidence before interpreting any task as accepted.

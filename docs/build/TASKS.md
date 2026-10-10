# Build queue

GitHub issues are the task records for this separate product repository. The captain must add exact issue links and keep ownership/state current. No Cheers issue is reused.

| ID | Task | State | Owner | Depends on | Acceptance |
| --- | --- | --- | --- | --- | --- |
| BP-001 | Repository and reviewed spec import | Complete locally | Bootstrap coordinator | — | Full documents saved; clean initial commit; private remote |
| BP-002 | Domain/protocol contracts and runnable build | Ready | Captain | BP-001 | Explicit IDs/capabilities/outcomes; reproducible Go build and meaningful tests |
| BP-003 | Durable registry, journal and daemon | Ready after contracts | Durable-state worker | BP-002 | Serialized SQLite mutations; intents/outcomes; restart reconciliation |
| BP-004 | Dedicated tmux lifecycle and native attach | Ready after contracts | Runtime worker | BP-002 | Exact markers; survivor discovery; native attach/detach; no duplicate start |
| BP-005 | TUI switcher and remembered groups/place | Ready after contracts | TUI worker | BP-002 | Real inventory; find/open; optional groups; restored selection; clear capability states |
| BP-006 | Provider discovery, link and skill preload | Ready | Provider worker | BP-002 | Codex/Claude exact linking; truthful modes; owned config preserved; repeatable install/remove |
| BP-007 | Integrated two-provider terminal alpha | Waiting | Captain plus independent validator | BP-003–006 | Full native journey, daemon loss and adverse-state evidence against exact SHA |
| BP-008 | Two-server one-entry federation proof | Early experiment | Transport worker | BP-002, BP-004 | Authenticated scope, routed inventory/attach, reconnect/revoke, no false host migration |
| BP-009 | Bidirectional communication thread bridge | Waiting | Communication worker | BP-007, BP-008 | Addressed Mattermost path then declared IRC mode; correct delivery/replay/input arbitration |
| BP-010 | Readiness, auth repair, usage and bounded continuity | Waiting | Continuity worker | BP-007 | Shared repair; qualified meters; eligible route; one task/thread; uncertain effects reconciled |
| BP-011 | Optional group and personal advice captains | Waiting | Coordination worker | BP-009, BP-010 | Scoped sourced reports; useful attention; accounted usage; zero unchanged-idle model activity |

Tasks are implementation milestones, not shipped feature claims. Split them into small issues before parallel assignment. BP-008 remains an early experiment even while local terminal alpha is developed.

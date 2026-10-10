# Agent terminal backplane first release build plan

Source: https://chatgpt.com/space/page_f423896d21548191b10beb588787574f

Snapshot imported on 2026-10-10. This repository owns implementation decisions and evidence; the source Page remains the reviewed product brief.

Proposed release plan revised October 10, 2026. Build an open-source terminal bouncer for solo power users: federated execution servers run real model CLIs, logins, threads, and environments; one client connection reaches their permitted work. Preload skills, link from inside the CLI, switch quickly, and keep one task/shared thread through supported model, subscription, harness, or host changes.

Prove the local terminal loop first and the two-server/one-connection path during feasibility. Add bounded continuation and truthful capacity/usage, then bidirectional Mattermost/IRC and robust scoped federation before Personal 1.0. The broader [product specification](https://chatgpt.com/space/page_3c1ef53937d08191b084fe3e702ce9bb) holds optional expansions. Commands are proposed syntax; this plan does not describe an implemented release.

## First release scope

Ship one bp CLI, a persistent user-level bouncer/host service, one TUI switcher, a selected terminal backend, and versioned harness/provider adapters. macOS and Linux first. One server combines local execution and entry roles; more servers add the same protocol and scoped inventory. Local use needs no product account, public listener, remote host, or model call for navigation.

Prepare skill installation mappings for Claude Code, Codex CLI, and Grok Build. Claim native attachment, prompt delivery, login isolation, and resume separately for each tested CLI/version/authentication combination. A provider can initially support observation and managed terminal use while its structured communication controls remain unavailable. The release matrix exposes that boundary.

The first useful loop is setup, link or managed launch, find/attach, detach/return, focused repair, and supported Continue elsewhere. Join a server once and reuse the same client connection. Required tools and account identity are checked contextually. Unrestricted account switching, live process migration, a new terminal emulator, and an owned IDE remain later investments. TUI/chat stay primary; any dedicated graphical client is native, with no product web app.

## Proposed implementation decisions

| Component | Starting choice | Decision gate |
| --- | --- | --- |
| CLI, daemon and adapters | Go in one repository, one bp executable; communicate with existing external CLIs/harnesses | Prove packaging, service lifecycle, process identity and protocol integration before locking it. |
| TUI | Bubble Tea with pinned dependencies | Navigation and contextual management; native rendering delegates to the terminal backend. |
| Durable state | Local SQLite, one event writer per authority, transactional outbox | Task/thread/lane/epoch records, visible journal/checkpoints, drafts, grants, installation and usage records. Never share its file across servers. |
| Native terminals | tmux on a dedicated Backplane socket as baseline | Compare Herdr on identical real-session lifecycle cases; select one launch backend. |
| Portable harness | Pi as first structured-continuity spike; OpenCode next candidate | Keep native CLIs independent; prove model change, queue/cancel, history, usage and isolation per supported mode. |
| Connection | Private local socket; authenticated multiplexed federation protocol | One entry routes conversations, commands and terminal streams to owners; preserve original caller scope. |

This is a proposed starting stack rather than a claim of measured superiority. Bubble Tea documents a Go framework with an event-driven model; tmux documents control commands and asynchronous terminal notifications. These make them practical spike candidates. [Bubble Tea](https://github.com/charmbracelet/bubbletea), [tmux control mode](https://github.com/tmux/tmux/wiki/Control-Mode)

Select exactly one terminal backend for the first release. Herdr wins only if the comparative spike demonstrates faithful attachment, reliable identity, acceptable maintenance, and the recovery behavior we need. Its documentation distinguishes detach persistence, saved layout, screen replay, and native conversation resume; it does not establish arbitrary process survival after server restart. Test recovery with no human client attached. [Herdr session state and restore](https://herdr.dev/docs/session-state/)

Separate task/journal/transition coordination, terminal backends, harness semantics, provider/account metering, skill deployment, federation routing, readiness/repair, and client projections. Pane/process/native IDs remain adapter references. Product task/thread/session/lane IDs stay stable while execution epochs change. Use bounded provider-specific operations and argument-array process invocation.

## Setup and daily commands

Use bp as a working command name until distribution/naming checks. bp opens the remembered local or federated switcher and starts the local user service when appropriate. Setup detects CLIs, previews connection-skill and selected kit installation, and reuses existing configuration/login. It explains reloads/collisions without requiring profiles, billing import, or a resource graph. Join is initial server enrollment; connect selects an enrolled entry. Ordinary use needs neither a per-session connection nor topology administration.

```text
bp setup              Preview/apply selected CLI skills and integration
bp                    Open the remembered federation's switcher
bp run <cli/harness>   Start a supported managed session
bp join <ssh-host>     Enroll an execution server with one scope review
bp connect <entry>     Connect once to an enrolled federation entry
bp link               Link exact current context through trusted evidence
bp link status        Show binding, target, mode and readiness
bp link off           Disconnect declared scope and future managed dispatch
bp continue           Choose a ready target for the selected lane/task
bp subscriptions      Inspect plans, pool eligibility and native quota
bp usage              Inspect scoped usage/model totals and cost coverage
bp skills             Inspect/apply selected skills
bp doctor             Diagnose selected work or installation
```

Inside Claude Code and Grok Build, the intended entry point is /backplane with status and off arguments. In Codex, start with $backplane or selection through /skills. Preserve Claude's own /rc. A custom slash command in every provider is a convenience goal, not a capability to assume. [Claude skills](https://code.claude.com/docs/en/skills), [Grok skills](https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/08-skills.md), [OpenAI skills documentation](https://learn.chatgpt.com/docs/build-skills)

The skill translates a small allowlisted command grammar into the helper protocol. A skill invocation may involve the model and its tool permissions; it is not guaranteed to be an immediate native RPC. The helper enforces identity and grants, then produces the authoritative connection receipt. Adapters can offer a more direct native command when documented extension support makes that possible.

The helper returns a short authoritative receipt: federation, execution server, task/shared thread, session/native identity, model/login evidence, workspace, observed/bridge/managed mode, and next action. Repetition returns status. On remote hosts it reuses local server enrollment and routes through the same federation. Ambiguity opens a focused selector or reload instruction; it never guesses from the newest transcript or matching directory.

## Skill preload and tool readiness

Maintain one pinned source catalogue and generate only the provider mappings the user selected. Share compatible discovery locations where their precedence is verified; otherwise install provider-specific owned copies. Each deployment records source, revision or content digest, target scope, previous owned revision, actual result, and reload requirement.

Preloading has two parts. Installation makes selected skills available in supported CLI catalogues. Loading puts specific instructions into the current model context. Install the user's selected kit broadly; load the minimal connection instruction when invoked and task skills when relevant or through a tested managed-start mechanism. Do not fill every conversation with the entire catalogue.

Configure the connection skill for explicit user invocation where the provider supports it. The current documented controls include disable-model-invocation for Claude/Grok and allow_implicit_invocation for Codex skill policy. Verify each generated configuration against the supported version. Installed, discovered, loaded, available, and unknown remain different inspection states. [Claude skill controls](https://code.claude.com/docs/en/skills), [Grok skill controls](https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/08-skills.md), [OpenAI skill policy](https://learn.chatgpt.com/docs/build-skills)

Preview additions and collisions once. Preserve existing custom skills, hooks, plugins, and configuration precedence. Never replace an unowned file merely because its name matches. Apply repeatably, report each provider's result independently, and roll back owned changes without erasing later user edits. A new CLI can receive the existing selected kit through a narrow apply operation.

Readiness checks inspect the exact workspace, installed CLI/version, login evidence where observable, selected skills, required tool configuration, and supported resource access. Configuration presence does not prove a running process loaded a tool. Show a short repair for a required dependency, while optional outages leave unrelated work usable. Credentials stay in their native stores; kits and exports contain references rather than tokens.

## Linking and terminal ownership

| Mode | What the user gets | Required evidence |
| --- | --- | --- |
| Observed | Exact registered identity and the activity/history the adapter can actually observe | Trusted lifecycle or runtime context correlated with the current process; no invented terminal controls. |
| Bridge | Named provider-native events and controls | A tested structured provider integration with explicit capabilities. |
| Managed | Native terminal attachment and supported execution lifecycle | A terminal launched or adopted through a proven backend path. |

Keep task/shared thread, participant lane, native conversation, execution epoch, and process lifetime separate. A restart/resume or target-binding change advances the epoch; a supported in-place model change can retain its native thread/process. Verified host-boot/process-start evidence prevents reused PIDs from becoming old runs. Concurrent task lanes have independent input/transition ownership.

The helper trusts managed-launch context or a tested provider integration correlated with live runtime evidence. Model text, a supplied thread ID, an account alias, and a working directory alone cannot establish identity. Shell fallback bp link must also obtain verified context or request selection. Bare invocation in an unrelated shell cannot identify a model conversation.

Registration does not acquire an arbitrary existing PTY. If a linked terminal cannot be attached, show its actual mode and offer an explicit managed launch or supported native resume. Preserve the current conversation and native setup where that path supports it; report any manual step. No invisible restart or account switch.

Link off disables the declared sharing scope and new managed dispatch for that binding. Already-submitted turns report their actual state. Stop, logout, archive, skill removal, and history deletion remain separate operations.

## TUI interaction contract

Use one default home: the switcher across authorized servers, scoped to the remembered company/product group or All work. Companies can collect products and their projects; standalone and flat groups work without hierarchy. A group can span several servers/providers. Shared threads, captains, accounts/usage, harnesses, skills and resources open contextually or through Ctrl-P. No new dashboard or mandatory organisation setup.

```text
BACKPLANE · personal federation              1 here · 3 total
Company / product: Acme / Payments            g groups
Captain: Claude · work · studio               Advice
Project / work       Current target                    State
API
> Fix checkout       Codex · work · studio             Working
Website
  Review copy        Claude · personal · laptop        Needs reply

Fix checkout · thread #checkout · /work/api · managed
Enter attach   p message   r repair   Ctrl-P commands   ? help
```

Browse with arrows or j/k, slash to search the current company/product group, g for the group picker, Enter to attach/open actual available actions, p to compose, r to repair, and Ctrl-P for commands. The picker shows recent/favourite companies and products. Choosing a company shows its permitted product work together; choosing a product narrows the same list. All work explicitly expands search to the authorized federation, including ungrouped sessions.

Drive remains the real full-screen native terminal. Provisionally reserve Ctrl-G: s switcher, g previous terminal, c company/product group picker, b previous group, d detach, doubled prefix literal Ctrl-G. Validate collisions per supported CLI/terminal and expose a configurable alternative; all other native keys pass through. Switching groups changes navigation without stopping workers or broadcasting input.

Persist group membership and remembered place: selected project/terminal/thread, expanded rows, filter, available scroll/read anchors and supported terminal arrangement. Drafts stay keyed to stable thread/recipient. Restore through reconnect and target changes; show the replacement target before sending a saved draft. Background events update local/global attention without moving focus. Global counts deduplicate sessions in overlapping groups; next-needs-me-here and next-anywhere stay distinct.

### Company and product collections

Create a named group by selecting existing projects, with optional session/thread pins and a company-to-product parent. Membership uses stable references, can overlap, and follows work across hosts/models/harnesses. Reuse verified project mappings for newly linked sessions. Group metadata is user-owned navigation; it does not change account/resource permissions. Removing or archiving a group retains its workers/history.

### Group and personal captains

Assign captain for the current company/product or Personal captain for all authorized sessions. Pick a ready model/account/subscription/harness/server and one Observe and advise or Coordinate within limits policy. Each assignment creates or selects a dedicated managed captain lane/thread; it never repurposes a busy worker silently. Preflight scoped event/read tools and supported structured dispatch, showing a focused repair when unavailable.

Advice summarizes progress, questions, blockers, auth/quota/resource incidents, possible conflicts and supported continuation options with source references, coverage and freshness. The personal captain merges authorized group reports plus uncovered work. Ask/read/pause actions remain in the picker and the captain's TUI/Mattermost/IRC thread. Coordination grants name permitted structured actions, targets/resources and budgets; human instructions and native approvals retain precedence.

Observe through journal subscriptions and coalesced meaningful events, with zero model turns during unchanged idle watching. Persist event cursors/reports/proposals transactionally and bind coordination to the originating request, target epoch and revision. Resolve scope after group changes/revocation; prevent recursive captain feedback and conflicting duplicate dispatch. Reuse the normal outbox, input ownership and continuation protocol. Show captain usage separately and inside totals. Pause stops new captain turns/coordination at a supported boundary while worker sessions continue.

## Accounts authentication and repair

Import the current native credential realm first. Support several account aliases for navigation, but claim independent logins only where the provider's tested auth/configuration mechanism isolates them. Unknown effective identity stays unknown. The daemon stores nonsecret realm references and verified identity evidence.

Group expiry by the verified host-local credential realm. Six dependent sessions should produce one repair with the affected list, the native sign-in action, and a final identity check. Preserve another realm's healthy sessions. Changed identity requires an explicit resolution before queued work proceeds.

Distinguish expired login, revoked access, wrong account, quota exhaustion, host loss, and expired tool/resource access. Repair only the failed binding. Reuse valid grants; broader access and a move to paid API usage are explicit choices. Native vendor authentication may open its own browser flow without creating a product web interface.

## Subscription pools and usage

Model subscriptions separately from host-local credentials and harnesses. A pool selects among authorized account/entitlement bindings; it does not create unlimited merged quota. Record plan/owner, status, cadence/currency, recurring commitment, renewal/management source, entitlement, and freshness. User-entered and unknown fields remain qualified. Pool participation can pause without logging out or cancelling the plan; direct billing changes require supported explicit integration.

Preserve quota bucket scopes/units/windows/resets and deduplicate shared account/organisation capacity across servers. One pool authority coordinates managed reservations; native external usage remains outside that enforcement. Keep the current target unless a chosen action or bounded standing policy permits a swap. Verify isolation rather than changing a shared global login. Subscription-to-paid-API fallback requires explicit authorization and limits.

Meter one canonical record per observed provider request/attempt, enriched by streams/transcripts/replicas. Retain logical request, retry/child relation, native turn, actual/requested model, epoch, account, harness, host, source and coverage. Usage answers requests/calls/turns, retries/subagents, distinct models used, active sessions by model and native token dimensions. Keep subscription commitments, actual charges, credits and estimates separate; unknown coverage is never zero and currencies are not silently combined.

## Durable continuation

Own task/shared-thread history, visible journal/checkpoints and the action outbox. Keep harness-native conversations intact. First prove native model change and an isolated account/subscription transition; next add cross-harness/host context handoff. A new native conversation can continue the same task/thread. Checkpoints preserve objectives, corrections, decisions, relevant complete tool pairs, unknown effects, workspace revision/dirty references, artifacts, skills/tools, grants and declared omissions.

One durable transition controller per lane freezes dispatch/queued prompts, proves a supported source boundary or effective fence, and reconciles effects. Persist checkpoint/transition identity; prepare the ready destination without execution; verify actual model/account/resources/workspace/cost; commit the new epoch; activate through deduplicated generation-bound dispatch. Lost activation acknowledgements require reconciliation. Old approvals/commands become stale. Rollback first quiesces the replacement and creates another epoch.

Default repair is Sign in, Continue elsewhere, or Attach terminal. An optional Keep going within my limits policy names eligible targets, resource grants and spend bounds. It may assign the next safe step, never blindly replay a whole agent turn. Possibly active source execution, uncertain writes, wrong identity, critical missing context, or incompatible tools/grants stops automatic replacement while preserving the task.

Pi provides a concrete first RPC/session reference; prompt acceptance is asynchronous and queue/cancel behavior needs conformance tests. Keep RPC and interactive-terminal modes distinct. OpenCode is a second candidate; ACP helps negotiated session integration. LiteLLM can serve API-mode request routing, never whole-turn continuation or CLI subscription management. Pin actual versions. [Pi RPC](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc-commands.md), [Pi sessions](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/session-format.md), [OpenCode server](https://opencode.ai/docs/server/), [ACP](https://agentclientprotocol.com/protocol/v2/overview), [LiteLLM routing](https://docs.litellm.ai/docs/routing)

## Durable threads input and recovery

The owning daemon persists task/thread/lane/epoch identities, per-thread ordered events, checkpoints, attention and operation state. Terminal backends own processes/PTYS. A daemon restart reconciles surviving work; it does not blindly restart it. Retention is explicit, and raw terminal output remains private diagnostic material rather than a complete semantic transcript.

Store a stable operation ID before attempting submission. Record persisted, attempted, provider-accepted where observable, completed, failed, and outcome-uncertain states. Chat receipt and agent acceptance are separate facts. A lost acknowledgement after possible submission requires reconciliation; it does not cause blind prompt or keystroke replay.

One managed path owns input and terminal sizing for the current run. Native Drive holds that slot; other managed clients observe. Structured dispatch waits while the human is typing or input state is unknown. Reconnection starts as observation and reacquires control explicitly. Suspend managed dispatch when an unmanaged writable attachment is detected; same-user external terminal routes remain outside universal enforcement. Daemon loss suspends automated dispatch. After restart, reconcile actual writable clients before admitting another controller; a database lease alone cannot fence an existing terminal attachment.

Deliver prompts through provider adapters that establish a safe turn boundary and supported submission semantics. Do not use tmux send-keys as a universal chat protocol. An unsupported addressed message can be retained with Attach to deliver as its next action, without being called agent-delivered.

After host reboot, restore the registry and offer supported native resume as a new run generation. Do not promise preservation of arbitrary processes or automatic continuation of interrupted external mutations. Approvals from an earlier run become stale. Missing workspaces and inaccessible realms remain visible repair states.

## External clients and federation

Canonical communication threads have stable IDs, explicit participants/recipients, source-client message IDs, causal references, and owning-instance event order. Keep terminal observations distinct from authored conversation messages. These contracts begin in the alpha even though external bridges follow.

Build Mattermost as the first external bidirectional bridge, followed by the initial IRC path. Match external identities to scoped product principals; visible display names are insufficient. History replay, bot echoes, and model-generated command text cannot submit another turn. Conflicting replies resolve against the current request and run. Native approvals remain native until an adapter proves safe request-specific remote approval.

Choose the initial IRC path after a real-client spike: an IRC-compatible gateway offers direct BNC-style use; a bot bridge into an existing IRC network offers a different integration. Keep both behind the same routing API and name the supported path clearly.

Federated execution servers keep their own terminals, native threads, environments, login realms and databases. Any authorized entry gateway exposes one permitted inventory and routes scoped messages, controls, and terminal streams to owners. bp join reuses an SSH alias and reviews verified instance identity plus grants once. Existing remote work imports at its execution host through trusted registration; observation does not seize a PTY or prove persistence. Separate owner-managed membership from independent directional peer grants.

Gateway reconnect resumes viewer-scoped upstream cursors through another trusted entry, with deduplication and grant revalidation. Task/thread home remains authoritative; while unreachable, show queued for home rather than accepted. Reachable native streams and permitted stale views can remain useful. Local authorized work can continue during a partition, but replacement needs source quiescence/fencing and shared reservations need their authority or bounded allocation. Execution movement keeps home authority initially; live migration and quorum-backed authority failover stay separate.

## Implementation sequence

| Slice | Deliverable | Evidence before expanding |
| --- | --- | --- |
| 1 Feasibility | Two real CLIs, preload/link for three, tmux/Herdr comparison, Pi continuity spike, two-server/one-entry prototype | Exact context/account attribution, native keys, queues, model-change semantics, isolation and routed attachment per version. |
| 2 Terminal alpha | Setup, company/product groups, remembered place, managed attach/detach, durable registry, skills and doctor | Two providers/workspaces; client/daemon recovery; preserved configuration; stale identity/input rejected. |
| 3 Capacity and continuity | Grouped realm/resource repair, pools, metering, checkpoints/outbox and bounded model/account continuation | Actual target verification, same task/thread, quota freshness, shared buckets, correct counts, unknown effects and no duplicate activation. |
| 4 Bouncer MVP | One-entry access to two servers, join/import, group/personal advice captains, Mattermost/IRC and lifecycle tools | Correct addressed threads; captain coverage/privacy/usage; no echoes or idle model activity; gateway recovery. |
| 5 Federated beta | Remote resources/placement/revocation, scoped captain coordination, cross-harness/host continuation | Partitions, task-home loss, interrupted transfers, source preservation, local credentials and per-lane fencing. |
| 6 Personal 1.0 | Stable declared support matrix and quiet daily operation | Independent-user simplicity/retention, rollback/export and no unresolved core correctness failures. |

Each slice has a runnable real user journey and recorded failures. Publish useful terminal alpha while continuing the early federation prototype; one-connection server access belongs in the Bouncer MVP. Do not set a calendar launch date until feasibility establishes adapter/maintainer effort.

## Acceptance and simplicity gates

1. Preload selected skills into clean and existing supported setups, discover the correct native invocation, preserve custom assets, repeat without duplication, and remove only unchanged owned installation state.

2. Link two same-provider conversations in the same workspace to their correct run and credential realm. Reject forged IDs, stale process evidence, and ambiguous context. Repeated link and later native resume retain one stable binding/thread.

3. Create a company with two products and several projects across two servers. Locate work among ten sessions, switch groups/products and native terminals, detach/reconnect, and return with available anchors and drafts intact. Retain membership after a model/host change; verify scoped/global search and deduplicated attention. No event steals focus and deleting a group leaves workers running.

4. Kill the TUI and then the daemon while a native session works. Reconcile the surviving terminal and report any event gap. Reboot separately, exercising supported resume and visible failure; do not count screen replay as live process recovery.

5. Expire one realm shared by six sessions and repair once natively. Verify identity, preserve another isolated realm, and distinguish resource expiry from quota. Continue on another authorized subscription under the chosen policy, with the actual target and billing mode verified.

6. Race two managed controllers and send a chat message during partial native input. Deliver only through the current allowed path. Lose an acknowledgement after potential submission; reconcile without a duplicated turn or external write.

7. Continue one addressed thread through TUI, Mattermost, and the supported IRC path. Replay history and bot echoes; reject stale request replies and unintended recipients.

8. Join two real servers, import an exact remote session, and access their permitted work through one entry/client connection. Reconnect through another entry with correct replay; revoke and partition grants. Preserve metadata privacy, reject transitive access, and distinguish gateway loss from task-home unavailability.

9. Keep one task/shared thread through supported in-place model change, isolated account/subscription replacement, cross-harness handoff and host move. Record native lineage, actual target, workspace/tool readiness, carried/omitted context and stale approvals; let an unrelated task lane continue.

10. Crash before/after target activation, lose acknowledgements, retain queued prompts, pause the source, and leave a tool result unknown. Reconcile one writer per lane, prevent old-source resume, and never repeat an uncertain mutation.

11. Count stream/transcript/peer copies once, shared quota once, and distinguish subscription commitment, paid charge and estimate. A main call A, retry A and child B produces one logical request, three observed provider calls and two actual models. Test unknown coverage, token inclusion, currencies and usage-detail grants.

12. Assign distinct models to group and personal captains. Replay an incident across overlapping groups, crash during reporting, reassign/revoke a session, and race conflicting instructions. Produce one scoped, source-linked report/accepted action per request revision, expose stale/uncovered state, reject self-triggered loops, account for captain usage, and verify zero model activity while idle. Pause a captain without stopping workers.

Pilot targets apply to six independent users with supported CLIs already installed and authenticated: five of six reach a useful session within two minutes; median locate-and-open time stays under five seconds among ten sessions; users understand the next action within ten further seconds. Grouped repair takes no more than three product actions excluding native sign-in. These are release targets to measure, not achieved performance claims.

Prescribed tests must produce no wrong-workspace/account/model dispatch, stale approval execution, lost draft, stolen focus, duplicated mutation or inflated metering. Record CLI/harness, adapter, backend, auth/entitlement mode, host, protocol version and actual result. Unsupported and not-run cases stay outside the claimed matrix.

## First implementation handoff

Begin in a dedicated product repository. Check in schemas, adapter conformance fixtures and release matrix beside the CLI/TUI. First prove launch-link-attach-detach-reconnect with two actual CLIs, then one client reaching two servers and a supported model change retaining the same task/thread. Those journeys choose the backend and narrow supported modes. They are engineering evidence, not a visual prototype or a new web interface.


# Codex primary common-Service integration and native transport qualification

Source base: `71d831f05405b68fc80cb38132b321f3ba1e6cc2`.
Source implementation commit: `004c26441d85f0d301c21ef34b919f4102cafcb2`.
Branch: `codex/native-controller-features`. The release checkout was not edited.
Environment: macOS 26.7.1 arm64, Go 1.26.6, installed native Codex 0.159.0.
Native executable SHA-256:
`e89718aa1969bfc4a471277bdc4679a3a3529293de0a309909822dfd67ddb77a`.

## Implemented boundary

`Service.PrepareCodexPrimary` records an explicit host-local selection of an
existing native thread, workspace and local realm revision in the common
SQLite operation journal before a process effect. Repeated selection keeps the
original stable target. Another operation cannot reserve the same native thread,
even under a different realm label; replacement is a future explicit transition.

`Service.NewCodexPrimary` composes the experimental controller with a concrete
common-Service lifecycle gate. It revalidates the durable target, host,
execution/generation, workspace, thread, realm and operation while holding the
same mutex used by terminal and broker admission. Reservations participate in
`CodexBound` before a primary exists, so terminal attach/stop/store-acquire and
ordinary terminal reconciliation cannot take over a reserved primary.

Stop rejects active grants (including observers), unresolved effects, callbacks
and pending callback registration. Its durable fence is committed before a
supervisor effect. A final callback-registration barrier covers bounded process
launch/stop, so a callback announced during the native quiescence query cannot
be overtaken. A retry rechecks durable callbacks. The stop operation remains a
fence even if a mutable inventory state changes or the daemon restarts. Exact
process-death reconciliation settles the stop in the common journal without
replaying it. Controller and Service lock ordering remains Service before journal.

Fresh primary binding publication creates no writer grant and advertises no
native projection, product semantic prompt, approval, model or account capability.
Repeated ready publication does not emit idle events. Realm selection is not
proof of account identity. The new surfaces are host-internal; no HTTP, CLI,
protocol schema, database schema version or default product wiring was added.

## Real native evidence and qualification limits

Official documentation distinguishes Unix control sockets from TCP WebSocket
listeners and documents bearer-token auth on WebSocket listeners:
[Codex App Server protocol](https://learn.chatgpt.com/docs/app-server).
The exact installed CLI's `app-server --help` and `generate-ts --experimental`
were also inspected. Generated schemas cover initialize/native home, exact-ID
resume, loaded-list, thread/read, queue/list and backgroundTerminals/list. The
implementation does not infer semantics from account/model text or a terminal.

The probes use newly created private HOME, CODEX_HOME and workspace directories,
no inherited credentials or configuration, disabled apps/plugins, and an unusable
loopback model-provider endpoint. Only the exact owned child is stopped and only
its observed endpoint inode is removed. No model turn, native Enter, user
conversation, customer channel, login, billing or remote publication was used.

| Check | Observed result | Consequence |
| --- | --- | --- |
| Unix listener with explicit capability-token flags; intentionally wrong Bearer token | Upgrade accepted. An initial assertion requiring rejection failed. | Unix token authentication is **not qualified**. Filesystem ownership and PID/boot/start checks do not substitute for token enforcement. |
| Guarded Unix adapter | Refused the listener before initialize because its rejection probe was accepted. | Constructor readiness and `Resume` refuse; no product primary is launched through this adapter. |
| Owned TCP listener bound only to `127.0.0.1` with the same token flags | Wrong token returned HTTP 401 before initialize; exact token accepted. | TCP token enforcement demonstrated for this exact binary, without claiming TCP peer PID or loaded-image identity. |
| TCP initialization | Exact native-home response matched the isolated home. | Native-home check demonstrated; no account identity inferred. |
| Empty isolated app-server, then an owned persisted thread created without a turn | Initially zero loaded threads; created thread was the only loaded thread, idle, and exact ID/workspace matched thread/read. | Read/loaded-list schema behavior demonstrated for the isolated zero-turn thread. No resume semantics demonstrated. |
| Exact owned persisted thread queue/background lists | Both empty, no pagination cursor. | Query behavior demonstrated for this combination only. |
| Ephemeral thread queue | Native error -32600: ephemeral threads do not support queued submissions. | Ephemeral queue capability unavailable; never interpret this error as an empty queue. |
| macOS loaded executable digest | No implementation that proves loaded-image bytes. | Full primary readiness unavailable on macOS. A prelaunch path hash is not that proof. |
| Linux `/proc/PID/exe` identity verifier | Source and cross-compilation pass; native runtime test not run here. | Linux runtime qualification is missing. |

`OwnedUpstream` is deliberately a guarded, incomplete inspection adapter. It
never resumes a thread, answers a callback or sends a model turn. Its constructor
readiness blocks common-Service launcher creation. Unknown callbacks,
notifications, malformed/duplicate JSON, mismatched response IDs, changed socket
identity, foreign peer/home and missing queue evidence fail closed. The TCP
qualification test is not production TCP transport wiring. Native resume,
durable callback recovery for the owned adapter, native projection, account
reads, semantic dispatch, model/approval turns and end-to-end primary lifetime
still need separate qualification.

## Validation

Commands run against the source implementation commit, before this evidence-only
commit:

- `go test -race -count=1 ./internal/codexcontroller ./internal/daemon ./internal/store`
  passed. Includes 14 new Service tests and 4 new default transport fixture tests.
- `go vet ./internal/codexcontroller ./internal/daemon ./internal/store` passed.
- `BP_OWNED_CODEX_PROBE=1 BP_CODEX_NATIVE_BINARY=<pinned native executable> go test -race ./internal/codexcontroller -run TestOwnedNative -count=1 -v`
  passed both opt-in probes with zero model turns. Final native receipt reports
  Unix wrong-token upgrade accepted/adapter refused, TCP wrong-token 401/exact
  token accepted, native home verified, unique owned persisted thread idle, exact
  thread/workspace verified and queue/background empty.
- `GOOS=linux GOARCH=amd64 go test -c -o <isolated scratch output> ./internal/codexcontroller`
  passed. This is compilation, not Linux runtime evidence.
- `git diff --check` passed.

Adverse tests cover forged authority fields, duplicate thread/realm reservations,
terminal and direct-store fallback denial, common-Service serialization, pending
callbacks, callbacks arriving during quiescence, late durable callbacks on retry,
active grants, lost resume/stop acknowledgement, daemon/journal restart, permanent
stop fencing, observed stop settlement without replay, idle event deduplication,
foreign peer/lifetime/home, wrong token, wrong response ID, duplicate JSON,
unknown callback, private socket mode/alias identity, and disabled real-adapter
construction before supervisor launch. Fixtures remain explicitly separate from
real native evidence.

Tracking: Keelmux Plane #5 In Progress acceptance comment
`e5d38cfe-3b46-48b1-ae90-4d43459449cf` was posted by the captain with HTTP 201 and
read back with HTTP 200 before implementation. Completion tracking is the
captain's integration responsibility; this document does not claim it was posted.

# Codex same-thread semantic feasibility

Actual Codex CLI 0.159.0 joined the exact native thread already accessed by a second initialized connection in an owned app-server. Its `thread/resume` acknowledgement named the same native UUID, with idle status and zero turns. This is a zero-turn compatibility result, not an implemented backplane semantic adapter. [Sanitized machine receipt](../evidence/codex-semantic-2026-10-10.json).

The experiment used private mode-0600 Unix WebSockets, an owned controlling PTY, isolated native homes and an empty disposable workspace. A no-auth custom provider pointed to loopback port 1, and the proxy refused inference and authentication mutation. No credentials were copied, existing login changed, model turn submitted, program upgraded or external chat message sent. Exact owned children were stopped and verified gone; graceful native CLI exit was not established.

The real native CLI supports `--remote unix://PATH resume THREAD_ID`. Captured requests included initialization, account/config metadata, native thread resume/read and paginated item/turn lists. The successful native resume acknowledgement was recorded through the proxy. A returned fresh thread ID was not immediately resumable: separate direct probes received `no rollout found` before metadata became visible. An owned server restart later preserved the idle thread metadata. Active turns, nonempty history and reboot remain untested.

The generated installed API contains separate thread, turn, item and callback identities. It exposes no exclusive writer lease. A second initialized connection successfully changed thread metadata, and both connections observed the resulting event. `canAcceptDirectInput=true` therefore does not establish single-writer ownership. The official [app-server documentation](https://learn.chatgpt.com/docs/app-server) supplies the API context; installed schema and these observations qualify this exact version.

## Next implementation boundary

A private home broker must sit between the authoritative app-server and both native CLI and semantic clients. Its upstream socket stays private. Each mutation and approval response must bind the originating principal to the current exact backplane session, execution generation and cooperating writer lease. Observer metadata and events must remain scoped to the granted native thread. Direct same-user access remains outside the cooperating-client guarantee.

The adapter should persist message intent before forwarding, including the operation, origin, payload hash and native client-message correlation ID. A native RPC acknowledgement proves server acknowledgement; provider acceptance and completed work are separate observations. Installed correlation fields do not prove deduplication. After a lost turn acknowledgement, recover exact history/items and reconcile the original intent; ambiguous absence must not trigger a blind retry.

Approvals require the exact native callback, thread/turn/item context, originating execution generation and live writer authority. Native authentication stays at the home. Detaching a client must preserve the authoritative app-server; its lifetime cannot depend on whichever terminal is foreground.

Before semantic capability can be advertised, prove a real authorized turn through native CLI and semantic access on the same thread, writer fencing, exact approval handover, nonempty history, dropped acknowledgement without duplicate work and active-turn recovery. Mattermost/IRC delivery and remote enrollment remain later independent gates. The current terminal-only capabilities stay unchanged.

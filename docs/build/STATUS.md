# Implementation status

Updated: 2026-10-10, local core integrated; independent acceptance in progress.

The runnable core includes distinct stable identities and execution generations, a private Unix-socket daemon, serialized SQLite registry/journal, durable operation and lease identity, isolated tmux lifecycle, inherited-nonce linking, and a Bubble Tea switcher with remembered selection/groups/drafts. Missing runtimes are retained as missing; they are not automatically relaunched.

Source `b962535ec785db1243b032d573e43dc1dafc73d9` passed `make build check` on macOS arm64 with Go 1.26.6, tmux 3.7b, Codex 0.159.0, and Claude 2.1.296. Both real programs were entered from the TUI, received unsubmitted Unicode drafts, detached to the same selection, and survived a daemon restart with exact execution/runtime identity. The restart experiment exposed a leaked lock descriptor; the fix and an actual surviving-child regression are included in that SHA. [Core evidence](../evidence/2026-10-10-core-integration.md).

`4bf0162` adds a passing daemon race test for a delayed old stop after a synthetic replacement execution. It proves stale command rejection at that boundary, not a native continuation feature.

Independent acceptance is checking native keys/resize, TUI and daemon SIGKILL, dropped responses, writer competition, private nonce rejection, ten-session switching, and journal recovery. Its exact-SHA report will qualify real-provider and fixture evidence separately. Full terminal-alpha acceptance is still pending that report and skill-preload integration.

Active work: owned provider discovery/skill installation; scoped native terminal transport through one gateway. A scoped inventory fixture is integrated, but no native federation attachment or independent second-server proof is certified. The real SSH host/workspace selection remains pending.

Planned/not run: authenticated model-turn evidence, verified account/model/native-thread attribution, real second-server acceptance, semantic Mattermost/IRC delivery, shared repairs/meters, subscription routing, native continuity, and optional captains. No public release or deployment has occurred. The repository remains private.

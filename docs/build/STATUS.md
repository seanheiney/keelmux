# Implementation status

Updated: 2026-10-10, independent local core evidence saved; private entry hardening in progress.

The runnable core includes distinct stable identities and execution generations, a private Unix-socket daemon, serialized SQLite registry/journal, durable operation and lease identity, isolated tmux lifecycle, inherited-nonce linking, and a Bubble Tea switcher with remembered selection/groups/drafts. Missing runtimes are retained as missing; they are not automatically relaunched.

Source `b962535ec785db1243b032d573e43dc1dafc73d9` passed `make build check` on macOS arm64 with Go 1.26.6, tmux 3.7b, Codex 0.159.0, and Claude 2.1.296. Both real programs were entered from the TUI, received unsubmitted Unicode drafts, detached to the same selection, and survived a daemon restart with exact execution/runtime identity. The restart experiment exposed a leaked lock descriptor; the fix and an actual surviving-child regression are included in that SHA. [Core evidence](../evidence/2026-10-10-core-integration.md).

`4bf0162` adds a passing daemon race test for a delayed old stop after a synthetic replacement execution. It proves stale command rejection at that boundary, not a native continuation feature.

Independent acceptance of implementation `4bf01620db4fad2994d245212a4fab9c05571c8f` passed eight groups covering native keys/resize, TUI and daemon SIGKILL, dropped launch/attach/stop responses, writer competition, nonce rejection, ten-session switching, missing-runtime retention and journal recovery. Two real programs and eight explicit fixture sessions were used. No model turn was submitted, and native account/model/thread attribution remains unknown. [Independent core evidence](../evidence/2026-10-10-independent-native-core.md). This receipt does not certify later gateway or setup changes.

Owned provider discovery, selected user/project skill installation and crash-safe removal are integrated. Removal retains the intact bundle and independent receipts in a private archive; there is no automatic purge. Native Codex catalogue discovery and Claude's `/backplane` selector discovery have been observed without skill invocation or model turns. [Claude discovery and removal evidence](../evidence/2026-10-10-claude-project-discovery.md).

The executable private gateway and client companion are integrated. Manual native attachment carried unsubmitted Unicode and terminal dimensions through two local daemon homes, but Claude detach left the companion waiting. Independent review reproduced a lost-home-acknowledgement ownership gap across entry restart, revocation projection leakage and blocked display cancellation. Those defects are being repaired before gateway acceptance or private push of this milestone. An independent two-home executable fixture and latest-source native rerun are active. No independent second-server proof is certified; the real SSH host/workspace selection remains pending.

Planned/not run: authenticated model-turn evidence, verified account/model/native-thread attribution, real second-server acceptance, semantic Mattermost/IRC delivery, shared repairs/meters, subscription routing, native continuity, and optional captains. No public release or deployment has occurred. The repository remains private.

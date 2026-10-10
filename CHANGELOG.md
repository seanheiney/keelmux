# Changelog

Release notes describe implemented behavior and the evidence for that release. The full [roadmap](docs/user/roadmap.md) remains separate.

## Unreleased

- Product name selected as **Keelmux**; repository reserved at `seanheiney/keelmux` and still private while publication checks finish.
- User documentation for terminal workflows, owned skills, private state, recovery, backup and the tested support matrix.
- Keelmux command names, compatible `bp` aliases, version metadata, state selection and a per-user release installer are being integrated separately. Their final behavior needs validation on the release source.

## 0.1.0 — Draft release notes

**Status:** not a published release. Populate the final tag, source commit, asset checksums and installation/runtime receipts before publishing these notes.

The intended first release provides a local terminal switcher over real Codex and Claude Code CLIs: launch, list, inspect, attach, detach, stop and reconcile owned executions; workspace groups and local drafts; explicit connection skills; and durable operation records that retain uncertain outcomes without blind replay.

The private entry path is experimental and has evidence for explicitly configured local homes on one machine. It does not establish remote enrollment or multi-server operation. The Codex broker's scoped zero-turn controller is internal experimental work, with no shipped product launcher or general structured prompt/approval support at this documentation's base source.

Current historical native runtime evidence is macOS arm64 only. Linux cross-compilation does not establish Linux runtime support. Provider authentication stays native; verified account/model routing, cross-server federation, shared chat and continuity after provider exit or host reboot remain unfinished. See the [support matrix](docs/user/support-matrix.md) for exact historical source receipts and limitations.

The final release must describe installation destinations, upgrade/schema compatibility, daemon restart requirements and state-preserving uninstall. It must not inherit an earlier source's runtime receipt as proof of the new tag.

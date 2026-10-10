# Changelog

Release notes describe implemented behavior and exact evidence. The [roadmap](docs/user/roadmap.md) records the broader product vision separately.

## 0.1.0

- **Keelmux:** native terminal switching for Codex and Claude Code, with canonical commands and compatible `bp`, `bpd` and `bpg` aliases.
- Owned launch/link, inspect, attach, detach, stop and reconcile; optional groups, remembered navigation and unsent local drafts.
- Durable operation receipts, exact execution generations and one cooperating managed writer. Uncertain effects are retained for inspection rather than blindly replayed.
- Per-user one-line installer, checksummed four-target archives, per-file manifests, atomic version selection, collision refusal and state-preserving uninstall.
- Read-only build metadata; fresh-state selection with in-place legacy-state reuse and unsafe-state refusal.
- Exact-instance daemon status/stop/restart, surviving-terminal reconciliation and private daemon-log validation.
- Owned skill preview, selected application and matching-bundle archive removal.
- User, operator, security, contributor and support documentation.

The [release](https://github.com/seanheiney/keelmux/releases/tag/v0.1.0) records its exact commit, asset digests and final installation/runtime receipts. Earlier source receipts remain historical qualifications, not substitutes for the tagged artifact's acceptance.

Native runtime acceptance covers the declared macOS arm64 Codex/Claude versions. Linux terminal-backend tests use explicit fixtures; cross-compiled architectures and native Linux providers remain separately qualified in the [support matrix](docs/user/support-matrix.md). Authentication stays native. No model turn was submitted without separate authorization.

The private entry has local same-computer, two-home evidence. Independent-server enrollment is unfinished. The Codex broker is an internal zero-turn experiment without a product launcher or general structured prompt/approval capability. Shared chat, account/model/usage routing, provider-exit/reboot continuity and optional captains remain on the roadmap.

The installer retains user state, logins and skills. Upgrades preserve the selected state directory; restart the owned daemon to load the installed version. Rollback changes application files and requires a schema-compatible state or offline backup. No automatic database downgrade occurs. The existing MIT license applies.

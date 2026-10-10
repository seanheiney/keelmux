# Keelmux release contract

Owner authorization: on 2026-10-10 the owner requested increased effort and speed, a functional product, a slick one-line installer, enterprise-class documentation and publication on seanheiney GitHub after selecting a distinctive name. `seanheiney/keelmux` is reserved. Public release remains gated by exact artifact acceptance; native authenticated model turns and a real SSH host are separate pending boundaries.

## Product and compatibility

The first public package exposes the functioning local terminal bouncer. It does not advertise the internal Codex zero-turn broker as interactive product control, remote enrollment, Mattermost/IRC, continuation, routing meters or captains. Retain the full roadmap. MIT remains the repository's explicit license; an earlier specification proposal is not a relicensing action.

Canonical binaries: `keelmux`, `keelmuxd`, `keelmux-entry`, built from `cmd/bp`, `cmd/bpd`, `cmd/bpg`. Compatibility symlinks: `bp`, `bpd`, `bpg`. Module: `github.com/seanheiney/keelmux`. Keep `bp/1`, stable identities, native session markers, BP launch correlation and owned `backplane-connection/v1` skill bytes/source/revision unchanged. Never rename a live tmux session or owned skill ledger merely for branding.

State resolution: explicit `--state`, `KEELMUX_STATE_DIR`, `BP_STATE_DIR`, then an existing `~/.local/state/agent-backplane` path; otherwise `~/.local/state/keelmux`. Prior state is reused in place and validated as owned/private/real. Invalid prior state refuses; it cannot create replacement authority. No implicit database/path migration.

## Artifacts and installation

`scripts/build-release.sh VERSION OUTDIR [os/arch...]` uses a `vX.Y.Z` version. Archive names: `keelmux_v0.1.0_darwin_arm64.tar.gz`; flat members are the three canonical binaries, three exact relative compatibility symlinks, LICENSE, README.md and RELEASE.json. SHA256SUMS describes complete archives. Production requires clean committed source; explicit development fixtures are qualified separately. Deterministic archives use SOURCE_DATE_EPOCH.

RELEASE.json schema 1 contains product, version, full commit, built_at, os, arch, go_version, source_clean/development_test qualification, canonical/doc file digests, and exact alias targets. `internal/buildinfo.Version`, `Commit`, `BuiltAt` linker strings populate all executables. `keelmux version --json` is read-only and includes name/version/commit/built_at/protocol/go_version/os/arch.

Standalone POSIX install.sh supports `--version`, `--prefix ABS`, `--bin-dir ABS`, `--uninstall`, `--help`. Environment: KEELMUX_VERSION, KEELMUX_INSTALL_DIR, KEELMUX_BIN_DIR. Default private owned installation is ~/.local/share/keelmux with version directories and atomic current; default public command directory ~/.local/bin. Refuse root, unsupported targets, malformed archives, integrity/download errors and unowned collisions. Never edit profiles, install dependencies, replace native CLIs or copy authentication. Preserve previous owned versions for rollback. Uninstall only exact owned links/code; retain state, native processes/auth/config and skill archives.

Test asset override: KEELMUX_DOWNLOAD_BASE_URL; HTTP requires explicit KEELMUX_ALLOW_TEST_HTTP=1 and a loopback target. Production uses HTTPS. Testing never writes the user's actual HOME or default state.

## Work ownership and gates

Captain: manifests, module/CLI/default-state integration, README, version metadata, contracts, source review, exact evidence, publication. Release engineer: install.sh, scripts, CI and installer-unit controls. Docs worker: docs/user, docs index, security/support/contribution/changelog. Independent validator: tests/release_acceptance.py and artifact receipts. Linux worker: isolated container runtime fixtures and qualified target receipts. Separate branches/worktrees; no shared source edits.

Before publication: source build/race/vet, independent installer failures/collisions/concurrency/uninstall, cold canonical/alias startup and exact default-state/legacy authority preservation, packaged native macOS two-provider lifecycle, artifact version/digest/source provenance, documentation command/link checks and full history credential scan. Build-only targets stay distinct from actual provider runtime proof. Hosted CI and release assets/attestations must be verified directly after publication.

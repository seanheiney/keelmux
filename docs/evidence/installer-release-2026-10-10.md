# Keelmux installer and packaging — 2026-10-10

Clean source: 0be153b2ae2fc2ad0d47120287bcdce2f8f3cc44. Candidate version: v0.1.0; Go 1.26.6; macOS 26.7.1 arm64. Exact source/archive digests and commands are in [the receipt](installer-release-2026-10-10.json). No release was uploaded or published.

The standalone POSIX installer verifies the selected SHA256SUMS archive, exact nine flat entries, regular files and three relative aliases, finite duplicate-safe RELEASE.json provenance and every manifest file digest. The loaded canonical CLI must report the exact version, full commit and bp/1. HTTPS is the production source; HTTP/platform overrides require explicitly enabled loopback tests.

Installation uses a private owned prefix, prefix/bin locks, an explicit six-link ownership receipt, immutable version directories and an atomic current symlink. Fresh installs refuse preexisting prospective links even when their destination matches. Failure preserves the prior current/code; concurrent initial losers cannot remove another install's scaffold. Uninstall removes exact recorded links and unchanged recorded code, retaining modified/unrelated state, native auth and skills. It never edits profiles or installs tmux/native CLIs.

All source race/vet checks and 14 focused installer/packer controls passed. Four actual target archives built and their manifest/hash/alias entries matched. The actual Darwin arm64 archive passed temporary-home install, loaded version JSON, same-version reinstall and uninstall while retaining auth/state sentinels. A second same-epoch Darwin arm64 build was byte-identical. These checks execute no providers or model turns.

The checked-in CI runs source race/vet, installer controls, host smoke and four cross-build targets. Version tags prepare an attested **draft** for captain review. Action pins were resolved from official GitHub repositories; attestation permissions follow [GitHub's documentation](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations). Local Ruby YAML parsing passed. Hosted CI, attestations, release publication and the public one-line installer remain separate pending gates.

The independent release validator and Linux runtime worker received the exact archives. Their receipts govern those claims; this evidence does not assert non-arm64 macOS runtime, remote hosts, provider work or a product Codex broker launcher.

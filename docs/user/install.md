# Installation

Keelmux is a per-user application. Bring your selected provider's native CLI and login. Keelmux 0.2 includes native supervision and requires no tmux; historical v0.1.0 requires tmux. It needs no product account or root service.

## One-line installation

```sh
curl -fsSL https://raw.githubusercontent.com/seanheiney/keelmux/main/install.sh | sh
```

The installer selects your OS/architecture, downloads a published release over HTTPS, verifies its checksum and manifest, and installs into `~/.local/share/keelmux`. Commands live in `~/.local/bin`. It refuses root, unsupported assets, unsafe directories and unowned command collisions. It never edits your shell profile, installs tmux/providers, changes native authentication or installs skills.

If needed, use the PATH command printed by the installer:

```sh
export PATH="$HOME/.local/bin:$PATH"
keelmux version --json
keelmux doctor
```

The 0.2 `doctor` checks the bundled supervisor and at least one launchable native provider. The historical v0.1.0 doctor checks tmux. It can start the private daemon; it does not submit a model turn or sign in. See the [support matrix](support-matrix.md) for tested combinations.

## Pin and review

For managed machines, download and review the script before running it, and select an exact release:

```sh
curl -fsSL https://raw.githubusercontent.com/seanheiney/keelmux/v0.2.1/install.sh -o keelmux-install.sh
# Review keelmux-install.sh using your normal software-review process.
sh keelmux-install.sh --version v0.2.1 \
  --prefix "$HOME/.local/share/keelmux" --bin-dir "$HOME/.local/bin"
```

The script supports `--version vX.Y.Z`, `--prefix ABSOLUTE_PATH`, `--bin-dir ABSOLUTE_PATH`, `--uninstall` and `--help`. Environment equivalents are `KEELMUX_VERSION`, `KEELMUX_INSTALL_DIR` and `KEELMUX_BIN_DIR`. Custom directories must be owned by you and satisfy the script's permission checks. Keep the installation prefix private; command links can live in your owned bin directory.

Version directories remain separate. The atomic `current` link selects the installed release. A failed download, checksum, manifest or collision check leaves the existing installation in place. There is no force-overwrite mode.

## Release verification

[Release assets](https://github.com/seanheiney/keelmux/releases) include four archives and `SHA256SUMS`. Each archive includes `RELEASE.json` with its exact source commit, toolchain, target and per-file hashes. The installer verifies archive and content hashes before switching versions. Checksums delivered with the archive detect corruption; they are not an independent publisher signature.

Where the release has GitHub provenance attestations, verify a downloaded asset with the GitHub CLI before your organization's installation workflow:

```sh
gh attestation verify keelmux_v0.2.1_darwin_arm64.tar.gz \
  --repo seanheiney/keelmux \
  --signer-workflow seanheiney/keelmux/.github/workflows/release.yml \
  --source-ref refs/tags/v0.2.1 --deny-self-hosted-runners
```

Choose the filename matching your target. Compare the attested source digest with the release's recorded commit. The one-line installer checks HTTPS downloads, checksums and manifests; it does not invoke GitHub attestation verification for you.

## Upgrade and rollback

Keep a consistent [state backup](operations.md#back-up-state), read the release's schema notes and preserve the same state path. Run the reviewed installer with the new exact `--version`. Reinstalling the same version verifies its owned bytes. Selecting an earlier compatible version changes application files; it does not downgrade a database.

An already-running daemon continues using its loaded version. Detach active managed clients, then use `keelmux daemon status` and `keelmux daemon restart` to load the installed daemon. This preserves surviving native terminals and reconciles their original identity. An old daemon without the lifecycle endpoint is refused; follow [daemon operation](operations.md#operate-the-daemon) rather than launching another authority.

For rollback, run the same installer with the previous compatible `--version`, then verify loaded daemon metadata. Restore a compatible offline snapshot if the older binary cannot read the newer schema. There is no automatic schema downgrade or state merge.

## Uninstall

```sh
sh keelmux-install.sh --uninstall \
  --prefix "$HOME/.local/share/keelmux" --bin-dir "$HOME/.local/bin"
```

Uninstall removes only unchanged installer-owned application files and matching links. It retains registry, drafts, native histories, credentials, skills and unrelated files. Modified owned files cause refusal. Skill removal is a separate [owned operation](skills.md#remove-a-skill); state deletion is separate and has no automatic purge. Stop the owned daemon through its lifecycle and plan reconnection before removing commands while native work is running.

## Build from source

Install the Go version declared in `go.mod` and your selected native CLI through their normal workflows:

```sh
git clone https://github.com/seanheiney/keelmux.git
cd keelmux
make build
make check
./bin/keelmux version --json
```

Keep `keelmux`, `keelmuxd`, `keelmux-entry` and `keelmux-supervisor` beside each other. The native runtime discovers its matching supervisor from that installed directory. The build also creates `bp`, `bpd` and `bpg` compatibility links. Cross-compilation establishes a build; it does not establish native provider runtime support.

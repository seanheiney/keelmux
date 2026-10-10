# Installation

Keelmux is a per-user application. It needs no product account or root service. You bring tmux and your selected provider's native CLI and login.

The one-line installer is the release entry point:

```sh
curl -fsSL https://raw.githubusercontent.com/seanheiney/keelmux/main/install.sh | sh
```

**Release integration pending:** this URL and installer contract are planned at the documentation's base source. Do not treat this command as an available release until the repository contains the installer, a published release and passing installation receipts. A source build is available now.

## Build the current source

Install the Go version declared in `go.mod`, tmux and the selected native CLI through their normal installation methods. From the repository:

```sh
make build
make check
./bin/bp version
./bin/bp providers
./bin/bp doctor
```

The pre-rename build produces `bp`, `bpd` and `bpg` in `bin/`. Keep the companion executables beside the CLI: the client resolves its daemon there, and the private entry uses a client companion. The release packaging must provide `keelmux` as the primary command and retain the legacy `bp` alias.

## Installer contract

The release installer must identify the OS and architecture, resolve an exact release, download its matching archive over HTTPS, verify the archive against release checksums and install only its owned files. The normal destination is a writable per-user directory. A failed checksum, unsupported asset or unowned collision stops installation without replacing the existing program.

The selected installer contract supports `--version vX.Y.Z`, `--prefix ABSOLUTE_PATH`, `--bin-dir ABSOLUTE_PATH`, `--uninstall` and `--help`. It also accepts `KEELMUX_VERSION`, `KEELMUX_INSTALL_DIR` and `KEELMUX_BIN_DIR`. Version directories remain separate and an atomic `current` link selects the installed release. These options require implementation and installation evidence before release.

For a pinned release, the planned command is:

```sh
curl -fsSL https://raw.githubusercontent.com/seanheiney/keelmux/main/install.sh | \
  sh -s -- --version v0.1.0 --prefix "$HOME/.local/share/keelmux" --bin-dir "$HOME/.local/bin"
```

The installer must not change provider authentication, silently upgrade CLIs, install skills, edit shell configuration or enable remote exposure. It prints the exact PATH step and `keelmux setup` command needed next. An unowned executable or symlink collision is preserved; there is no force-overwrite mode.

The supported release platforms must match runtime evidence. Cross-compilation alone does not establish Linux support. A successful install proves application placement and its recorded smoke checks, not native account access.

## Upgrade and removal

Keep a consistent [state backup](operations.md#back-up-state) and the previous release before upgrading. Preserve the same explicit state path. Database format compatibility and any required daemon restart belong in that release's notes.

Uninstall removes only unchanged installer-owned application files. It retains registry, drafts, native histories, credentials and installed skills. Skill removal is a separate [owned operation](skills.md#remove-a-skill). State deletion is separate from uninstall and has no automatic purge in the current source.

For managed machines, review the pinned installer source and install a pinned release using the documented destination option once available. A shell pipeline is a convenience, not a replacement for your organization's software review.

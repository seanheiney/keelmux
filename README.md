# Keelmux

**Keep your agents running. Keep your place.**

Keelmux is a terminal bouncer for real Codex and Claude Code sessions. Launch work on your machine, leave the terminal, and return to the same running process from a fast session switcher. Your native CLI keeps its login, workspace, permissions and conversation.

```text
keelmux run codex       →  work keeps running
keelmux                 →  find it, open it, continue
Ctrl-G d                →  return to your switcher
```

[Get started](docs/user/quickstart.md) · [Documentation](docs/README.md) · [CLI reference](docs/user/cli.md) · [Recovery](docs/user/recovery.md) · [Security](SECURITY.md)

## Install

With tmux and your chosen native CLI installed:

```sh
curl -fsSL https://raw.githubusercontent.com/seanheiney/keelmux/main/install.sh | sh
```

The installer verifies the release checksum, installs into your own directory, and switches versions atomically. It leaves native logins, running sessions, shell configuration and user data intact. It prints the PATH command if `~/.local/bin` is not already available. [Pinned, manual and verified installation](docs/user/install.md).

## Start working

```sh
keelmux doctor
keelmux run --workspace /path/to/project codex
keelmux run --workspace /path/to/another/project claude
keelmux
```

Use **arrows** or **j/k** to select work, **Enter** to open its native terminal, **/** to find a session, and **v** to view without taking input control. **Ctrl-G d** returns to the switcher. Selection, optional project groups and local drafts are remembered.

The daemon starts automatically. Closing the switcher keeps native processes running. A daemon restart reconciles surviving sessions; missing processes remain visible rather than silently launching replacements. A machine reboot is a different event and cannot preserve running processes.

## What you get

- **Real native terminals.** Unicode, resize, alternate screens and native keyboard behavior pass through the owned tmux session.
- **Durable control.** Stable work identity, execution generations, exact operation receipts and one cooperating managed writer keep reconnect and uncertain outcomes explicit.
- **A focused switcher.** Find work quickly; groups are optional, and drafts stay local until you use the native CLI.
- **Owned skill setup.** Preview provider-specific connection skills, apply only selected bundles, and archive matching owned bundles on removal.
- **Private local control.** User-owned state and Unix sockets; credentials remain with the native provider.

The current native runtime evidence covers **macOS arm64, Codex 0.159.0 and Claude Code 2.1.296**. Linux and other architectures need their own runtime acceptance; see the [support matrix](SUPPORT.md) for exact release artifacts and qualifications.

A private entry can expose explicitly permitted sessions from multiple execution homes. Its current proof uses two homes on one computer. Independent remote enrollment, semantic Mattermost/IRC delivery, task continuation, usage routing and captains remain on the [roadmap](docs/user/roadmap.md). The experimental Codex broker has controlled zero-turn evidence; it is not an interactive product command or advertised semantic capability.

## Operate with confidence

| Need | Guide |
| --- | --- |
| First session and daily navigation | [Quickstart](docs/user/quickstart.md), [TUI](docs/user/tui.md) |
| Install, upgrade, rollback or uninstall | [Installation](docs/user/install.md) |
| Provider readiness and connection skills | [Providers](docs/user/providers.md), [Setup](docs/user/setup.md) |
| Backups, permissions and daemon operation | [Operations](docs/user/operations.md) |
| Lost replies, crashed clients or missing sessions | [Recovery](docs/user/recovery.md), [Troubleshooting](docs/user/troubleshooting.md) |
| Trust boundaries and private reporting | [Security](SECURITY.md), [Architecture](docs/user/architecture.md) |
| Supported combinations and known gaps | [Support](SUPPORT.md), [Release evidence](docs/build/STATUS.md) |

Keelmux enforces one input owner among cooperating managed clients. The same OS user can access their own terminals directly; this is not an OS sandbox. A successful terminal attachment does not establish a model's entitlement, account identity, quota or accepted turn.

## Existing Backplane installations

`bp`, `bpd` and `bpg` remain compatibility aliases. Existing private `~/.local/state/agent-backplane` state is reused in place; no live socket, session or registry is renamed. A fresh installation uses `~/.local/state/keelmux`. Explicit `--state`, then `KEELMUX_STATE_DIR`, then `BP_STATE_DIR` take precedence. Unsafe prior state is refused, preserving its bytes. The current owned `$backplane` and `/backplane` skill names remain compatible. [Migration guide](docs/user/migration.md).

## Build and contribute

```sh
git clone https://github.com/seanheiney/keelmux.git
cd keelmux
make build
make check
./bin/keelmux version --json
```

Go 1.26+, SQLite, Bubble Tea and pinned Go dependencies; tmux is required at runtime. [Contributing](CONTRIBUTING.md) explains the contracts and isolated tests. The [full specification](docs/product/PRODUCT_SPEC.md) and [build plan](docs/build/BUILD_PLAN.md) retain the longer-term product vision.

[MIT](LICENSE). No telemetry service or model API key is required by the local switcher.

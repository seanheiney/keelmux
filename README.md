# Agent Backplane

A terminal-first, user-owned bouncer for agent work: keep real model CLIs running on your machines, reach permitted sessions through one connection, and continue work across terminals and supported chat clients.

The local terminal core and private entry experiment are runnable on the tested macOS arm64 setup. Independent acceptance entered real Codex and Claude programs locally and through one entry, detached and reconnected, and checked adverse lifecycle states. Owned skill setup, acquisition recovery and bounded partial-home inventory are integrated. See [current evidence and gaps](docs/build/STATUS.md).

## Product contract

- Real native CLI sessions are the center. The main interface is a TUI; a future dedicated graphical client must be native.
- Company/product groups organize stable work and remember your place.
- Vendor logins and native conversations remain authoritative. Unknown account/model identity remains unknown.
- Threads persist across supported execution changes; native resume and context handoff are distinct.
- Execution servers retain their terminals, environments, resources, and credentials. One authorized entry connection routes to the owners.
- Skills, tools, account readiness, shared repairs, usage, and optional captains support this workflow.
- Herdr is optional and must earn adoption through actual lifecycle evidence.

## Build status and ownership

The build captain is GPT-6.1 Sol at ultra effort. It owns task assignment, shared contracts, integration, evidence, and honest completion reporting. See [the build queue](docs/build/TASKS.md) and [captain instructions](docs/build/CAPTAIN.md).

Start with two real CLIs and launch → link → attach → detach → reconnect, then demonstrate one client reaching two execution servers. No fake sessions or web dashboard substitute for these journeys.

## Documents

- [Full product specification](docs/product/PRODUCT_SPEC.md)
- [Reviewed first-release build plan](docs/build/BUILD_PLAN.md)
- [Parallel build operating plan](docs/build/CAPTAIN.md)
- [Implementation acceptance](docs/build/ACCEPTANCE.md)
- [Independent runtime and TUI review](docs/research/BOOTSTRAP_REVIEW.md)

## Development

The implementation uses Go, a per-user daemon, SQLite, tmux on a dedicated socket, and Bubble Tea. Dependencies are pinned in `go.mod` and `go.sum`.

```sh
make build
make check
./bin/bp doctor
./bin/bp run --workspace /path/to/project codex
./bin/bp run --workspace /path/to/project claude
./bin/bp
```

Use arrows or `j`/`k` to select work, `/` to search, Enter to open its native terminal, and `v` for a read-only attachment. In the native terminal, **Ctrl-G d** returns to the switcher; **Ctrl-G Ctrl-G** sends a literal Ctrl-G. `g` cycles optional local group views; `p` edits a local draft without sending it to a model.

`bp --state DIR` selects an isolated private state directory. The default is `~/.local/state/agent-backplane`. Run in a terminal with a valid `TERM` such as `xterm-256color`. The daemon starts automatically and keeps native sessions running when the TUI exits. `bp list`, `bp inspect SESSION`, and `bp reconcile` expose current evidence. Launch and stop retries must preserve the printed operation ID.

The current tmux backend supports cooperating managed writers. The same OS user can reach its own tmux socket directly. Semantic chat input, native resume, model/account attribution, and cross-host execution movement are not yet implemented.

`bp providers` reports qualified native installation/login evidence. `bp setup` previews an owned bundle without installing it; `bp setup --scope project --root /path/to/project --providers codex,claude --apply` installs only the selected bundle after checking for collisions. `bp skills remove` archives the matching owned bundle and retains its bytes. These actions preserve existing native skills and settings. See [setup and private entry usage](docs/build/RUNNING.md).

Run provider processes using existing authorized native logins. Automated fixtures must use isolated temporary state and a dedicated test tmux socket. Authenticated model turns, live remote resources, and external mutations require separate evidence and are never implied by fixture success.

## License

MIT. The initial GitHub repository is private while bootstrap and credential hygiene are verified. A public open-source release is a separate delivery milestone.

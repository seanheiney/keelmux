# Agent Backplane

A terminal-first, user-owned bouncer for agent work: keep real model CLIs running on your machines, reach permitted sessions through one connection, and continue work across terminals and supported chat clients.

This is a new product repository. Implementation is beginning; no terminal alpha, federation, chat delivery, account failover, or captain automation is certified yet.

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

The initial stack under evaluation is Go, a per-user daemon, SQLite, tmux on a dedicated socket, and Bubble Tea. The captain must record dependency versions and establish reproducible build, test, and installation commands in the first implementation milestone.

Run provider processes using existing authorized native logins. Automated fixtures must use isolated temporary state and a dedicated test tmux socket. Authenticated model turns, live remote resources, and external mutations require separate evidence and are never implied by fixture success.

## License

MIT. The initial GitHub repository is private while bootstrap and credential hygiene are verified. A public open-source release is a separate delivery milestone.

# Support

Start with [Quickstart](docs/user/quickstart.md), [Troubleshooting](docs/user/troubleshooting.md) and the [support matrix](docs/user/support-matrix.md). Those pages distinguish tested behavior from the roadmap.

Use [seanheiney/keelmux issues](https://github.com/seanheiney/keelmux/issues) for reproducible bugs and feature requests. This is a community project without a paid support SLA.

## Useful bug reports

Include:

- Keelmux version and source commit, installation method, OS and architecture.
- tmux and native CLI versions, with account details omitted.
- The command or switcher action, expected result and observed result.
- Whether the client, daemon, native CLI or host restarted.
- A minimal reproduction using a temporary repository and synthetic data.
- Relevant error text and the affected operation's state, with private paths and identifiers redacted.

`keelmux version`, `keelmux providers`, `keelmux doctor` and `keelmux inspect SESSION` provide useful local context. Review every output before sharing it. A successful doctor command is not proof of native account access or every recovery guarantee.

Do not attach a complete state directory, SQLite database, provider home, terminal transcript, authentication file or environment dump. Maintainers may request a smaller sanitized excerpt. Follow [Security](SECURITY.md) for sensitive reports.

## Ownership of failures

Keelmux owns session inventory, switching, its private terminal backend, operation bookkeeping and input ownership. Providers own their native login, models, network service and permission behavior. Test a provider problem in its native CLI before filing it as a Keelmux defect, while preserving any uncertain Keelmux operation rather than relaunching it blindly.

An unsupported platform, unverified provider or unfinished feature should be reported as a request with a concrete desired workflow. Do not treat a proposed command in the product specification as an implemented interface.

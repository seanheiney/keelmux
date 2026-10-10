# Security

Keelmux controls real native agent terminals. Use it only with accounts, repositories and commands you authorize. A writable attachment can send input to the native CLI; that CLI controls model requests, tool execution and permission prompts.

## Trust boundary

The current product is a local, single-principal application. Private state directories and sockets protect access from other OS users. The daemon keeps durable operation records and checks execution identity before controlling its terminal backend. A cooperating managed execution has one input writer. These controls are not an OS sandbox and do not restrict code, tools or filesystem access inside the native CLI.

The optional private entry connects explicitly configured local homes for the same principal. Its current evidence does not establish multi-user tenancy, remote enrollment, cross-server trust, public network exposure or enterprise compliance. See the [support matrix](docs/user/support-matrix.md).

Keelmux uses your selected provider's existing native authentication. It does not promise which account or model is active when the provider cannot prove that information. Connection skills contain owned instructions and a helper, not copied provider credentials. Do not add credentials to skill files or entry configuration.

## Protect local state

Keep state in an owned real directory with mode `0700`; private socket and record checks fail closed. Do not weaken permissions to work around a refusal, share one registry between OS users, or move a live authority database to another host. The selected state path is part of the application's authority.

Registry records, terminal history, daemon logs, drafts, provider homes and backups may disclose private work. Runtime receipts can include launch correlation. Review and redact data before sharing it. Avoid posting raw database files, receipt directories, sockets, environment dumps, native authentication files or transcripts.

Use the [documented backup and recovery procedure](docs/user/operations.md). A saved PID or socket path alone does not prove a live process belongs to Keelmux. An uncertain operation must not be replayed automatically.

## Channel bindings

A channel binding authorizes scoped chat observation and explicit authored replies. It grants remote senders no terminal, model-turn, native approval or execution access. Sender allowlists use verified platform identities; display names and IRC nicknames do not establish authority. Each binding pins its endpoint, realm, authenticated bot/account and destination. A changed scope requires a new binding.

Use a dedicated bot with access limited to the intended destination. Store its credential in an owned private regular file at an absolute path. Symlinks, FIFOs, public files and oversized secrets are refused without changing their permissions. Keep credential bytes out of shell arguments and reports. Local registry backups include private message text, immutable routing and credential file references; they do not include the credential bytes.

Mattermost/Slack intake keeps the first observed authored note, with bounded history since activation. Remote edits and deletions are not projected. IRC requires authenticated services accounts and correlated server echo; disconnected history remains a coverage gap. None of these paths is a compliance archive, reader receipt or exactly-once external delivery claim. Lost acknowledgements remain uncertain until matching existing evidence is found through read-only reconciliation.

## Report a vulnerability

Do not publish credentials, exploit details or private work in a public issue. Use [GitHub private vulnerability reporting](https://github.com/seanheiney/keelmux/security/advisories/new). If GitHub does not offer that private form, withhold sensitive details; do not substitute a public issue.

Include the affected version and source commit, OS and architecture, a minimal reproduction, the violated trust boundary and the expected behavior. Use synthetic paths and data. Maintainers will assess the report; the project currently makes no response-time or compliance certification commitments.

## Release handling

[Release assets](https://github.com/seanheiney/keelmux/releases) identify an exact source commit and matching checksums. A checksum verifies that the downloaded archive matches the published release manifest; it is not an independent signature or an audit of its contents. Keep third-party native CLIs updated through their own supported channels.

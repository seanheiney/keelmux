# Independent native acceptance

`native_acceptance.py` is an opt-in macOS integration harness, separate from `go test`. It launches the installed native Codex and Claude programs, opens the actual `bp` TUI through a PTY, and uses a dedicated private Unix socket and tmux server. It never submits a model prompt.

```sh
make build
python3 tests/native_acceptance.py --report docs/evidence/native-acceptance-2026-10-10.json
make check
```

The harness records the tested implementation SHA, its own SHA-256, installed versions and each result. Only the harness/evidence paths may be dirty during the run. It cleans its tracked daemon, PTYs and dedicated tmux server and removes its owned `/tmp/bp-a-*` directory. It never connects to the default tmux server or changes native logins, billing or installed versions.

Native proofs use a repository already trusted by the vendor CLIs: the primary checkout by default, or `--native-workspace PATH`. Fixture workspaces remain disposable. The harness never accepts a new trust prompt. Codex receives process-only `check_for_update_on_startup=false` and a read-only sandbox; it never selects an upgrade. The test waits for a native composer before typing Unicode. Escape in a native setup/update selector is a separate behavior and can end that native invocation; it is not terminal persistence evidence. On Codex 0.159.0, a process-only project trust override did not satisfy the new-folder guard, so it is not claimed as a supported trust path.

The exact link helper runs in inherited managed context before `exec` of the actual vendor program. The private link nonce is never printed or included in the report. This proves owned-launch correlation, not native skill invocation, effective account/model, native thread attribution, or an authenticated model turn.

Dropped-response tests wait until the server produces a response and discard it unread, then retry the same durable operation. They prove recovery from loss of a completed acknowledgement. Crash during an uncertain backend effect remains a separate source fixture gate.

Actual vendor runs cover native launch, link, TUI attach/detach/reconnect, Unicode visibility, resize, TUI SIGKILL recovery, daemon SIGKILL recovery and native interrupt behavior. Interruption is tested after uninterrupted recovery because a vendor CLI may legitimately exit on Ctrl-C. Two-client grant races, forged releases, read-only attachment and durable journal checks exercise the real daemon API.

The raw Unicode/Escape/Ctrl-C/literal-prefix oracle and eight auxiliary sessions are explicitly fixtures. The ten-session scenario contains two native programs and eight fixture processes; it is not a ten-agent model-work or independent-user speed claim. Company/product selection and a local draft are restored through TUI restart. Native skill preload, verified account/model, authenticated turns, host reboot, remote servers, continuity, chat delivery and accessibility/simplicity pilots remain separate gates. Passing this harness alone does not certify terminal alpha.

`claude_discovery.py` is a separate narrow native selector check. It requires a separately authorized project installation of the Claude connection bundle in an already trusted repository. It does not install or remove skills itself. For example, after the explicit project preview and apply steps:

```sh
python3 tests/claude_discovery.py --native-workspace /path/to/trusted/repository --report docs/evidence/claude-discovery.json
```

The harness waits for Claude's native composer, types `/backplane`, and requires the native menu's skill description. It leaves the command unsubmitted. It then observes `/rc` autocomplete without submitting or opening Remote Control. Installed native code containing the `remote-control` command's `rc` alias is recorded separately from runtime eligibility and invocation. A menu row proves discovery; it does not prove that native skill instructions were loaded or executed. The exact link uses the inherited helper before native exec. Safe project-bundle removal remains a separate explicit step using the owned installation ledger.

`gateway_acceptance.py` exercises the executable `bpg` entry, two separate `bpd` home processes, and real `bp` TUI/`drive` attachments. It owns all state, fixtures and sockets. Both homes and the gateway run on one computer; the raw fixture panes are not vendor model work or independent remote-host proof.

```sh
make build
python3 tests/gateway_acceptance.py --report docs/evidence/gateway-acceptance.json
```

Checks cover scoped unified inventory, scrubbed home locators, home-only management, routed Unicode/control bytes and resize, read-only attachment, writer contention, entry SIGKILL with a saved lease, startup configuration revocation with exact receipt release, an idle unavailable home with usable surviving work, and attached-home failure followed by exact repair. Configuration changes take effect on explicit entry restart; this is not a live-reload claim. Static fixture output and continuous PTY consumption do not establish behavior under display backpressure or continuous native rendering. The initial six-group receipt is explicitly qualified because independent review found additional authority and transport failures outside those checks.

`gateway_authority.py` is an adverse executable restart fixture. A private counted proxy lets the home complete an attachment and then discards the response. After entry restart with a different principal, replaying public operation/client IDs must be refused before another home attachment call. Restoring the original principal must recover the exact original grant. A separate actual `bp drive` path opens without prior entry `/attach`; its principal claim must survive entry restart too. No private lease token is printed or stored in the receipt.

```sh
python3 tests/gateway_authority.py --report docs/evidence/gateway-authority.json
```

`--only uncertain` and `--only direct` isolate the two restart boundaries. Pre-fix receipts intentionally record both failing reproductions; use a repaired implementation for acceptance. This remains a same-computer policy/transport fixture.

`native_gateway.py` runs the installed native Codex and Claude programs through two executable homes and one executable entry. It waits for the actual native composer through the entry PTY before typing an unsubmitted Unicode draft, tests Escape and resize, detaches with Ctrl-G d back to the actual TUI, and reopens/reconnects to the same home execution. It continuously consumes terminal display output while waiting for detach cleanup.

```sh
python3 tests/native_gateway.py --native-workspace /path/to/already/trusted/repository --report docs/evidence/native-gateway.json
```

`--only codex` or `--only claude` isolates one terminal journey; both native homes still launch and link. Startup diagnostics retain only known phrase flags, line shapes and terminal query counts. They contain no raw native screen, launch nonce or credentials. Codex 0.159.0 was observed to use either `›` or `»` for its composer, so both are accepted. This is a readiness predicate for terminal input, not account/model attribution. No Enter is sent in native mode. These checks do not establish native skill invocation, an authenticated model turn, independent remote enrollment, entry crash recovery under native rendering, or display backpressure stress.

# Gemini, Grok and OpenCode native launch contracts

This source contribution adds discovery and interactive argv preparation for
Gemini, Grok and OpenCode. It is not a released build or a real model-turn
qualification. The base is `2e09b0ed11678c9248ab0168da1505cc9a9bafc5`; the
implementation and tests are committed together with this receipt.

## Observed native programs

On 2026-10-10, macOS 26.7.1 / arm64, each installed program was invoked once
with `--version` and once with `--help`, from an owned scratch directory with
closed stdin and a 15-second process-group deadline. Every probe exited 0;
none reached the deadline. Output capture was below 64 KiB. Go 1.26.6 was used
for the source checks. No interactive provider session was started.

| CLI | Observed version | Version output SHA-256 | Help output SHA-256 |
| --- | --- | --- | --- |
| Gemini | `0.52.0` | `f0ff15ea2ce1fbc899964a336ed594ad3d53eac3ae183a081b547998805ab138` | `b5c6e1af180f48adb3700982e7b06e905f29d2965f047eaeebd0b5c4f676b632` |
| Grok | `grok 1.0.50 (c58f321264ba) [stable]` | `0a2dae661ccdf4cd858dac39912a260a17ad481799f1cba5d9754fb3fb895fda` | `cda6873e2f90a7d77de94c2e3026794671fac04b2e40ac74e4d91f429f829403` |
| OpenCode | `1.18.21` | `4a830c412419a3308d095a64e4fd2504b26d1e975366e089d886c03d90fa016c` | `b594ad32674bf8008017157833ab87316170523ed23dbd10c37ae9931d2ba178` |

Digests cover stdout followed by stderr. OpenCode help used stderr; the other
five probes used stdout only. Raw help is retained privately; it is not copied
into this document. These are output receipts, not executable or loaded-image
identity proofs. The adapter trusts the locally selected executable on PATH.

Gemini's installed `gemini-NJ7GQJ2G.js` parser was also inspected. Its file
SHA-256 was `dd794a9f48ffa995516861de776d3c340d944dcd1e694c56cb07a2ee7f9cade1`.
The inspected options at lines 8027–8170 declare one value per model, prompt,
policy, extension, MCP-name and include-directory flag. Its resume option may
omit its value.

At lines 8224–8230, a positional query becomes `promptInteractive` only when
`isHeadlessMode()` is false, otherwise it becomes `prompt`. In the installed
`chunk-7LQRUKPT.js` at lines 278323–278335, that function selects headless mode
when `CI` or `GITHUB_ACTIONS` equals the exact string `true`, unless
`GEMINI_CLI_INTEGRATION_TEST` equals `true`. Non-TTY stdin/stdout also selects
headless mode. The adapter therefore rejects positional Gemini queries.
The explicit interactive prompt sets interactive configuration at line 8388;
non-TTY stdin is rejected for that option at lines 15981–15986.

## Available source contracts

`Discover` retains the existing first three records (Codex, Claude, Grok), then
adds Gemini and OpenCode. The new providers run only `--version`, never account
or model listing commands. Their authentication state, method and principal stay
unknown; semantic delivery stays false. The existing Codex and Claude contracts
are unchanged.

New-provider launch qualification requires the reviewed versions above. Grok
retains the build/channel in its normalized version evidence, without the
leading `grok ` prefix. A different recognized version remains visible as
installed but has `native_terminal=false`, with an explicit unavailable detail.
Malformed or failed version output stays unknown. No raw output enters errors.

`SupportedLaunch` checks arguments first, locates the executable, checks its
reported version, then returns `[absolute executable, ...original argv]`.
It never executes that interactive argv, invokes a shell, changes environment
or writes native configuration. No default model, approval override or initial
prompt is inserted. Explicit initial prompts are passed through only on
interactive entry points; none were submitted during this qualification.

| Provider | Reviewed interactive arguments |
| --- | --- |
| Gemini | `--model`/`-m`, `--prompt-interactive`/`-i`, `--resume`/`-r`, `--session-file`, `--session-id`, `--policy`, `--admin-policy`, `--allowed-mcp-server-names`, `--extensions`/`-e`, `--include-directories`, `--debug`/`-d`, `--sandbox`/`-s`, `--screen-reader`, and `--approval-mode` restricted to `default` or `plan`; no positional arguments |
| Grok | `--model`/`-m`, `--agent`, `--agents`, `--rules`/`--append-system-prompt`, `--system-prompt-override`/`--system-prompt`, `--tools`, `--disallowed-tools`, `--deny`/`--disallowedTools`, `--sandbox`, `--max-turns`, `--reasoning-effort`/`--effort`, `--resume`/`-r`, `--continue`/`-c`, `--fork-session`, `--session-id`/`-s`, `--debug`, `--debug-file`, `--disable-web-search`, `--fullscreen`, `--minimal`, `--no-alt-screen`, `--no-plan`, `--no-subagents`, and `--permission-mode` restricted to `default` or `plan`; at most one positional prompt |
| OpenCode | `--model`/`-m`, `--session`/`-s`, `--continue`/`-c`, `--fork`, `--prompt`, `--agent`, `--pure`, `--print-logs`, `--log-level`, `--mini`, `--no-replay`, `--replay-limit`; no positional arguments |

Options use separate tokens or `--long=value`. Unknown options, abbreviations,
short clusters and attached short values are rejected. Required values cannot
be missing or empty. Duplicate model selectors are rejected. Management words
remain reserved even after `--`. An optional resume value matching a management word requires explicit
`--resume=value` to remove parser ambiguity.

Management, authentication, updates, headless/ACP execution, listings, trace or
session export, remote attachment, broad-listen/server flags, worktree creation,
code restoration, workspace redirection, workspace trust bypass and explicit approval bypass are
outside this managed-launch contract. Native configuration, policies and native
remembered grants remain authoritative; this adapter does not prove an effective
approval mode or override the user's existing native policy.

## OpenRouter through OpenCode

Prepare OpenCode with an explicit `--model openrouter/<model-id>`. Nested IDs
such as `openrouter/vendor/model:variant` are preserved. The adapter validates
nonempty slash-separated identifier components and a bounded ASCII identifier
alphabet; it does not check registry membership, credentials, billing, quota,
availability or an accepted model. Other native `provider/model` selections are
also allowed. Omitting the selector leaves native model selection in charge and
does not establish an OpenRouter route.

OpenRouter is a provider routed by OpenCode, not an invented `openrouter`
executable. Authentication and account stores remain native. The adapter does
not inspect or copy native account files, keys, credential environment values or
private model catalogs.

## Verification and limits

- `go test -race ./internal/adapters/providers`: PASS (7.662 seconds; 160 test
  and subtest passes, two explicit native receipts skipped). Tests use owned executable
  fixtures for argv preservation, pre-probe rejection, command/value boundaries,
  version/build/channel drift, unknown authentication, output redaction and
  cancellation. Both explicit native receipts are skipped in the ordinary suite.
- `go vet ./internal/adapters/providers`: PASS.
- `go test ./cmd/bp`: PASS, including the existing caller contract.
- `BP_NATIVE_PROVIDER_VERSION_PROBE=1 go test -count=1 -v
  ./internal/adapters/providers -run '^TestExpandedNativeVersionReceipt$'`: PASS
  with the three observed executables on PATH. This calls `SupportedLaunch`
  with no arguments, prepares one-element argv and runs only version probes.
- `git diff --check`: PASS.

No model turn, prompt submission, native Enter, login/account/billing change,
native configuration edit, live inference or credential copy occurred. Native
PTY attachment, interactive launch behavior, Unicode/resize/input fidelity,
loaded capabilities, resume results, effective permissions, accepted model,
account attribution and quota are unqualified for these three providers. Linux
native-provider execution was not tested. Shared CLI/profile/application wiring
and release qualification belong to the captain's integration gate.

Grok `--cwd` and OpenCode positional project paths are rejected. The caller's
managed workspace selection supplies the child working directory.

`ValidateManagedEnvironment(provider, lookup)` checks the exact Gemini
environment comparisons above. `SupportedLaunch` checks the preparing process's
environment too, before running a version probe. The caller must repeat this
check at the actual daemon launch boundary using the child's environment and
supply TTY stdin/stdout through its managed PTY. This contribution does not edit
that shared daemon boundary. The validator does not mutate environment values,
read unrelated keys or add the native integration-test exception itself. No
environment-based mode claim is made for the other providers.

## Primary references checked

- [Gemini command reference](https://geminicli.com/docs/cli/cli-reference/) and
  [configuration reference](https://geminicli.com/docs/reference/configuration/)
  distinguish the interactive entry point, initial prompts, headless mode and
  approval controls. Installed help/parser pins the supported option subset.
- [Grok CLI reference](https://docs.x.ai/build/cli/reference) and
  [permissions](https://docs.x.ai/build/features/permissions) document native
  entry points, rules, compatibility aliases and permission behavior. Installed
  help supplies the complete observed command inventory.
- [OpenCode CLI](https://opencode.ai/docs/cli/),
  [model selection](https://opencode.ai/docs/models/) and
  [OpenRouter provider](https://opencode.ai/docs/providers/#openrouter) document
  its TUI, explicit model routing and native provider authority. Installed help
  additionally names `providers` with `auth` as its alias.

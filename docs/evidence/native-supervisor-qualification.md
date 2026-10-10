# Native supervisor qualification — 2026-10-10

The strict, no-turn native provider run passed on source
`4c265651855478c16408173852beb2d2fbca0704`, using its source-built Darwin arm64
`keelmux`, `keelmuxd`, and `keelmux-supervisor` binaries (build timestamp
`2026-10-10T21:13:22Z`, Go `go1.26.6`). Installed providers were Codex CLI
`0.159.0` and Claude Code `2.1.296`. This qualifies these source binaries and
these two installed CLI versions on this machine. It does not qualify a release
archive, installer, another OS, or a model turn.

The exact metadata, binary SHA-256 values, child/helper PID/start identities,
reference digests, checks, and cleanup outcome are in
[native-supervisor-4c265651-darwin-arm64.json](native-supervisor-4c265651-darwin-arm64.json).
The recorded harness SHA-256 matches the checked-in harness bytes.

## Command

```sh
python3 tests/native_supervisor_acceptance.py \
  --bin-dir /Volumes/DataSSD1/agent-backplane/bin \
  --expected-source 4c265651855478c16408173852beb2d2fbca0704 \
  --workspace /Volumes/DataSSD1/agent-backplane \
  --report docs/evidence/native-supervisor-4c265651-darwin-arm64.json
```

The workspace was explicitly selected as an existing trusted workspace. The
harness froze the binaries into its own private temporary directory and used
only its own daemon state, sockets, processes, and terminal clients. It never
invoked tmux. Each returned execution reference identified the native backend.
Native login, account selection, trust, and configuration remained with each CLI;
the harness did not read/copy credentials or change those settings. Raw captures
and private diagnostic errors were retained only in private temporary storage.

## Observed checks

Both providers passed all of these checks through the actual product transport
and TUI:

- Composer rendered before any native input, including with a headless daemon
  that had inherited `TERM=dumb`. No outer client terminal query prefix appeared.
- An unsubmitted `native draft café λ 🙂` appeared in a fresh rendered-screen
  snapshot. Escape was sent without Enter; the same process remained live.
- Writer resize reached the actual provider PTY at 92 columns by 29 rows.
- A simultaneous read-only observer could neither insert the sentinel draft nor
  resize that PTY. Another managed writer request was refused with HTTP 409.
- Observer and writer `Ctrl-G d` detached with exit code zero. A fresh writer
  reconnected to the same execution and visible Unicode draft.
- Deliberate SIGKILL of the owned control daemon preserved the exact native child
  and supervisor reference, PID, and start identities. The replacement daemon
  had a different instance; a stop request using the old instance returned 409
  and left the replacement daemon intact.
- Repeating the same launch operation reconciled the original execution and did
  not create a second session or native process.
- After daemon death, the previous attachment grant remained held. A fresh
  writer was refused. The harness recovered the **same saved attachment
  operation/client**, obtained its exact existing token, and explicitly released
  that grant after observing no native clients. It then reattached successfully.
  This proves safe explicit ownership repair; it is not an automatic orphan
  grant expiration or a claim that every client can recover without saved IDs.
- The real TUI filtered to the provider, attached, detached to the inventory,
  restored the selected session, cleared its pending attachment, and exited
  cleanly. Enter was used only in verified TUI browse/search states with no
  native writer attached. Native Enter and submitted model turns both stayed zero.
- Final exact product stop requests ended all four recorded provider/helper
  lifetimes. The harness verified they were no longer live; cleanup had no
  issues and used no saved native PID signals.

## Defects and diagnostic attempts

Earlier `f039c676e30e930e1a8c70da0452f30c87d30330` attempts are retained as finite
JSON diagnostic receipts. They are not the final qualification result.

1. Codex stopped at its terminal confirmation because the headless daemon had
   inherited `TERM=dumb`. No prompt was answered. Adapter fix
   `9c5124a333c73f3ba204a0d95d396b9c3a4e176b` normalizes inherited empty/dumb TERM
   for PTY launches while preserving explicit launch TERM and foreground service
   environments. The captain integrated this as `5772ad2`.
2. An initial Claude check classified the outer client's startup OSC 11 / CPR
   probe as a supervisor leak. The probe actually came from Bubble Tea package
   initialization. The captain removed that initialization in `4c265651`.
   The final run did not enable the harness's diagnostic probe exception.
3. Early harness attempts did not continuously drain all attached terminals and
   could mistake an existing observer plus a new client's startup probe for a
   ready new client. The harness now drains each terminal continuously and waits
   for a new exact native helper PID/start pair plus an actual screen snapshot.
   Under that stricter readiness check, observer/writer detach passed.
4. A daemon-crash attempt initially tried a fresh writer before repairing the
   retained attachment grant. Its 409 was correct ownership preservation. The
   final harness proves reconciliation and exact release before reacquisition.

The intermediate Claude recovery diagnostic passed with an explicit old-client
query exception; only the final `4c265651` receipt passed strict qualification.

## Separate adapter verification

For native adapter source `9c5124a333c73f3ba204a0d95d396b9c3a4e176b`:

- `go test -race ./internal/adapters/nativepty ./cmd/keelmux-supervisor` passed.
- `go vet ./internal/adapters/nativepty ./cmd/keelmux-supervisor` passed.
- `GOOS=linux GOARCH=amd64 go build ./cmd/keelmux-supervisor` cross-compilation
  passed. This is not Linux runtime evidence.
- `python3 -m py_compile tests/native_supervisor_acceptance.py` passed.

The adapter tests include deterministic raw-key/query/snapshot/lifetime tests.
This provider run adds real no-turn CLI startup and integrated transport/TUI
proof; it does not replace those lower-level tests.

## Limits

No authenticated model turn, effective model/account verification, semantic
prompt delivery, provider skill invocation, host reboot/resume, remote server,
Linux native CLI runtime, or installed release artifact was exercised here.
Native CLI startup can perform its ordinary native initialization; no model
prompt or native Enter was submitted by the harness. No onboarding, permission,
login, upgrade, or billing prompt was answered.

The snapshot contract remains bounded current-screen reconstruction. This run
proves the observed composers and unsubmitted drafts through these transitions,
not every provider screen, inactive buffer, saved cursor stack, graphics protocol,
keyboard extension, terminal palette, or arbitrary terminal query family.

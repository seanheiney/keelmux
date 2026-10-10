# Native lifetime supervisor

The native backend has no tmux dependency. A detached `keelmux-supervisor serve`
process owns one foreground child, its PTY (for terminal launches), private Unix
socket, terminal state and bounded output ring. The daemon and attach clients can
exit independently. One helper per execution isolates terminal failure, avoids a
shared service startup/recovery race, and permits exact execution tombstones.

## Integration contract

- `New(privateStateDir)` or `NewWithOptions(dir, Options{HelperPath, HistoryBytes})`.
  Default helper lookup is a sibling `keelmux-supervisor`, then PATH. Inspection
  does not require the helper executable to remain installed.
- Existing `runtime.Backend` and `runtime.LinkVerifier` contracts are implemented.
  `LaunchProcess(ctx, domain.LaunchSpec)` provides the same identity and lifecycle
  for foreground services such as Ergo without a PTY. Services must not daemonize.
- `RuntimeRef.Backend` is `native`; `Session` is a random instance ID, `Socket` is
  the execution socket, `PID`/`ProcessStart` describe the direct child, and
  `BootID`, `ExecutionID`, `OperationID` identify its host/run/launch.
- `ClientAttached(ctx, ref, pid, readOnly)` verifies the actual attach process's
  kernel peer PID and process start. The attach helper enters raw mode before
  connecting, so this function is also the transport input-readiness boundary.
- `Open` exposes a connection-bound native stream for acceptance/integration;
  `AttachCommand` runs the installed helper with full execution/operation/instance
  identifiers. A read-only stream cannot write or resize. The supervisor admits
  one writer, fences an in-flight write before releasing ownership, and never
  restores or replays a disconnected writer's input.
- **`ErrUnavailable` means unknown execution outcome.** Missing helper/socket,
  unconfirmed launch intent and absent receipt are errors, never proof of exit.
  Callers must keep uncertain state and may not replace a possibly live service.
  `Alive=false` without error requires a confirmed terminal receipt or host reboot.

## Durable and private boundaries

The backend synchronizes and fsyncs a launch intent before spawning a helper.
The helper consumes that intent durably before starting the child. Repeating
execution/operation/content identity reconciles the existing instance only;
changed content, completed or uncertain attempts never relaunch. Receipts bind
requested IDs/generation/workspace/argv/environment, resolved executable,
effective-environment digest, a private auth nonce and inherited link token.
Argv and environment values travel over an inherited pipe and are not persisted.

Private state files use nonblocking no-follow opens before type validation,
owned 0600 files and a 0700 directory. FIFO, directory and symlink state paths
are rejected without waiting for a peer.
Connections validate boot identity, supervisor PID/start, Unix peer PID/UID,
socket type/mode and an exact authenticated runtime reference. Stop signals only
the supervisor's direct `os.Process` handle, never a saved PID or process group.
The helper sets umask 077 for its own child. Same-user host owners retain access
to their own state and processes; this is cooperating ownership, not isolation.

## Terminal coverage

Live terminal bytes preserve native input, Unicode, escape keys, bracketed paste,
resize and Ctrl-C. `Ctrl-G d` detaches; doubled Ctrl-G sends literal Ctrl-G.
The tracker uses the pinned `charmbracelet/x/vt` emulator with a bounded
streaming grapheme tail for decomposed accents, variation selectors and ZWJ emoji; reconnect restores the
visible active screen, Unicode cells, SGR styling, cursor, pending line wrap, vertical margins, origin mode and
selected input modes through a fresh generated snapshot. Historical bytes are
never replayed. Read-only attachment does not change the owned PTY dimensions.

The supervisor is the sole authority for supported terminal queries, including
CPR, status, primary/secondary device attributes, text dimensions, mode status,
keyboard flags, version, and default foreground/background/cursor colors. These
queries are stripped from client output and answered under the PTY write lock,
including before the first client attaches. Snapshots cannot emit query answers.
A standards-aware streaming parser normalizes C0-in-ESC and raw C1 framing while
preserving UTF-8 continuation bytes. All OSC/DCS/APC/PM/SOS controls are suppressed
from client output; clipboard,
hyperlink, title and graphics side effects are not replayed. Unknown query
capabilities are not invented.

Limits: the inactive screen buffer, application-specific scrollback, saved cursor
stacks, horizontal margins, pixel dimensions, graphics protocols, advanced
keyboard modes and dynamic palette restoration are not qualified. Visible screen
restoration is not full application-state recovery. Real provider CLIs need their
own startup and reconnect acceptance; deterministic shell tests do not establish
vendor/provider support or model acceptance. Host reboot or helper death cannot
preserve arbitrary processes or the helper's in-memory terminal state. Detached
children outside a foreground process's own lifetime are not a supported service.

## Resource and failure bounds

History defaults to 1 MiB, configurable from 4 KiB to 8 MiB. Terminal dimensions
are at most 512 rows/columns and 65,536 cells. Snapshots cap at 1 MiB, terminal
controls at 64 KiB, input chunks at 32 KiB, attachments at 32 and concurrent
connections at 64. Network/PTY writes time out after two seconds. Slow readers
are disconnected or receive an explicit history gap; they never block native
output draining indefinitely. Reconnection obtains a fresh screen snapshot.

A macOS arm64 local observation with an ordinary build measured approximately
13,552 KiB RSS for one idle PTY helper and 11,776 KiB for a process helper. These
are point observations, not a load guarantee. Race builds observed approximately
38,432/35,184 KiB. Per-execution overhead is an explicit tradeoff; large host-scale
loads need measurement before moving ownership into a shared service.

## Evidence commands

`go test -race -v ./internal/adapters/nativepty ./cmd/keelmux-supervisor` builds
and tests the real helper with race instrumentation. The suite covers durable
single-launch behavior, missing/foreign supervisor identity, exact stop/link
identity, private state and child umask, actual controller-process SIGKILL and
recovery, one writer/read-only rules, Unicode, resize, Ctrl-C, raw escape keys,
installed helper detach, bounded slow readers, screen/style/alternate-screen
snapshots, pre-attach CPR/DA readiness without model input, C0/C1 query and
clipboard-filter regressions, split combining/VS16/ZWJ sequences, pending wrap,
main-buffer active pen restoration and finite-time special-file rejection.

`go vet ./internal/adapters/nativepty ./cmd/keelmux-supervisor` checks both owned
packages. Linux compilation is a separate check; macOS execution does not qualify
Linux runtime behavior. The captain owns shared dependency manifests, daemon
wiring, installer, support matrix and final evidence record for integrated SHAs.

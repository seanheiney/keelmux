# Local core integration evidence

Date: 2026-10-10. Source: `b962535ec785db1243b032d573e43dc1dafc73d9`. macOS arm64; Go 1.26.6; tmux 3.7b; Codex 0.159.0; Claude 2.1.296. This is private local development evidence, not release or full acceptance.

## Automated checks

`make build check` passed: both binaries built; `go test -race ./...` and `go vet ./...` passed. Runtime tests use a dedicated tmux socket; store/daemon/TUI tests distinguish real tmux behavior from fixture backends and `/usr/bin/true` attachment fixtures.

`TestAuthorityCanRestartWhileLaunchedChildSurvives` starts a real `/bin/sleep` child, closes daemon storage authority, and reopens the same database while the child remains alive. It failed with the original raw file-open flags and passed under the race detector with `O_CLOEXEC`.

## Actual native observations

The captain launched actual installed Codex and Claude using `bp run` into an owned `/tmp/bp-captain-*` state and dedicated socket. With `TERM=xterm-256color`, both native interfaces opened from the TUI. An unsubmitted `π café ✓` draft appeared in each composer; Ctrl-C cleared it. Ctrl-G d returned to the same selected work and group. No model turn was submitted. Initial `TERM=dumb` attachment failed explicitly and released its lease.

The first daemon-restart check on `f878769` failed: the tmux server inherited the registry authority lock descriptor and blocked replacement daemon startup, while both native programs survived. The failed owned socket was stopped rather than bypassing the authority lock. Source `b962535` fixes the inheritance.

In a fresh owned `/tmp/bp-captain-fixed-*` state, both actual programs were launched again. The exact owned daemon received SIGTERM; the next `bp list` started its replacement. Both sessions remained running with identical stable session IDs, execution IDs/generations, and full runtime references (socket/session/pane/PID/boot/process-start/launch-operation). No duplicate launch occurred. The private raw inventory receipt is retained in ignored `work/captain-native-receipt.json` and contains no inherited link nonce.

## Scope and missing evidence

Account, actual model, and native provider-thread identity remain explicitly unknown in the registry. Native terminal banners were not used for attribution. Existing native configuration was retained; only trust of the owned proof workspace was accepted. No login, billing, or CLI upgrade occurred.

Independent acceptance supplies its own exact-SHA matrix for nonce linking, native resize/keys, SIGKILL recovery, dropped acknowledgements, writer competition, ten-session fixtures, read-only behavior, and tmux loss. Those results are not inferred from this captain receipt. Native skill loading, authenticated model turns, independent second-server transport, chat delivery, continuity, usage, and captain activity are not proven here.

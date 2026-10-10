# SIGINT lifecycle fixture investigation

The combined source race run `80474` failed the unchanged
`TestPTYLifecycleUnicodeResizeInterruptReconnect` on Darwin: after real Ctrl-C,
detach and reconnect, the original `/bin/sh` fixture printed
`REPLY:reconnecte\r\n` for the full `reconnected\n` input. The original three-second
deadline and complete-payload assertion are preserved.

The native PTY production and test source at that failure were identical to
frozen release base `71d831f05405b68fc80cb38132b321f3ba1e6cc2`.
The local `/bin/sh` identifies as Bash 3.2.57 on Darwin arm64.

## Controls before changing the fixture

- Unchanged lifecycle race test: first 12/12 passed, then 3/100 failed with the
  exact missing-final-`d` reply. A passing rerun did not clear the original failure.
- Independent direct `creack/pty` shell controls without the Keelmux supervisor,
  transport, output filter or detach: 200/200 no-SIGINT trials passed; the first
  signal batch failed 1/100 with no reply inside three seconds, and the second
  signal batch passed 100/100. The full 12-byte input write was checked.
- Independent shell control with `head` reading lines instead of the shell
  builtin: 100/100 signal and 100/100 no-signal trials passed. This diagnostic
  did not become the final fixture.
- 100 paired backend trials with a raw-line marker in the original shell and a
  finite Go child: original shell failed 5/100 (three exact truncated replies,
  two missing interrupt acknowledgements); Go child passed 100/100. For every
  truncated reply the shell's own raw-line marker already contained
  `reconnecte\n`, before the product's terminal tracker or output transport:

  ```text
  read "REPLY:reconnected": i/o timeout; output "REPLY:reconnecte\r\n"
  diagnostic shell read captured "héllo 世界 🛟\nsize\nreconnecte\n"
  ```

These controls isolate an interrupt/read problem in the original shell fixture.
The exact Bash/kernel internal mechanism is unqualified; no production input
loss or output-filter change is inferred or patched. The failing diagnostic
output remains in the owned local `work/pty-shell-go-controls.log`.

## Fixture correction

Only test source changes. A dedicated `TestMain` mode starts a Go child with real
canonical PTY termios, `ISIG`, `VINTR=3`, echo disabled, a real SIGINT handler,
Unicode line reads, actual window-size ioctl and a finite 45-second lifetime.
The original test retains Unicode reply, resize, real Ctrl-C acknowledgement,
detach/reconnect, exact runtime identity and complete reconnect-payload checks.
The existing three-second per-read deadline is unchanged. A no-signal lifecycle
control uses the same child and backend. Linux/Darwin termios helpers are test
files; no product dependency or production signal behavior changes.

## Validation after correction

- `go test -race ./internal/adapters/nativepty -run '^TestPTYLifecycle(UnicodeResizeInterruptReconnect|NoSignalControl)$' -count=100 -v` passed all 100 signal and 100 no-signal runs (14.295 seconds).
- `go test -race ./internal/adapters/nativepty` passed (8.632 seconds).
- `go vet ./internal/adapters/nativepty` passed.
- `GOOS=linux GOARCH=amd64 go test -c ./internal/adapters/nativepty -o work/nativepty-linux.test` passed compilation. Linux execution remains separate.

No assertion or deadline was weakened, no production file changed and no
model/provider/customer operation occurred.

# Independent native terminal core acceptance

Tested implementation: `4bf01620db4fad2994d245212a4fab9c05571c8f`.

Harness SHA-256: `ecb947840145bc98c1367ff4a394165f8d84fc86cc4d6ff523424c67df379db8`. During the run, only the new harness/evidence paths were dirty; `cmd/`, `internal/`, `go.mod` and `go.sum` matched the tested SHA. The evidence commit adds tests and documentation after that implementation SHA; it does not change the implementation under test.

Commands, run in the acceptance worktree:

```sh
make build
python3 tests/native_acceptance.py --report docs/evidence/native-acceptance-2026-10-10.json
make check
```

All eight integration check groups passed. `make build`, `go test -race ./...` and `go vet ./...` passed. Machine-readable results are in `native-acceptance-2026-10-10.json`.

Environment: macOS 26.7.1 (25G309), darwin/arm64, Go 1.26.6, tmux 3.7b, Codex CLI 0.159.0, Claude Code 2.1.296, protocol `bp/1`. Native programs reused the existing native login realms and existing trust for `/Volumes/DataSSD1/agent-backplane`. Effective account, model and native thread remain unknown. No model prompt was submitted. Codex used its native read-only sandbox and a process-only `check_for_update_on_startup=false` override. Claude used its existing native permission setup.

The daemon/control socket/tmux socket and fixture workspace were private, owned `/tmp/bp-a-*` paths. All tracked test daemons, tmux servers, PTYs and disposable directories were cleaned. The user's default tmux socket and existing terminals were never used.

| Check | Evidence and boundary |
| --- | --- |
| Two real native programs | The inherited helper links the exact launch, then `exec`s the installed Codex or Claude program. The actual child working directory matches the requested workspace. This is native program launch, not an authenticated model turn. |
| Exact link | Both launch identities link successfully. Forged nonces and public IDs without a nonce are refused. Capabilities expose native attachment and explicitly deny semantic prompt delivery. Native vendor skill invocation is not proven by this wrapper. |
| TUI/native loop | Both native composers show a Unicode draft. Native pane size reaches 92×29. Ctrl-G d returns to the same selected session and saved search filter; quitting/reopening the TUI attaches the original execution. |
| Client crash | SIGKILL of the TUI while attached, followed by PTY closure and TUI restart, releases/reconciles the saved grant and returns to the same execution. |
| Daemon crash | SIGKILL/restart of bpd while a native attachment survives preserves the exact execution/runtime/PID/pane. A competing writer is refused. Repeating the original launch operation does not create another registry row or process. |
| Lost acknowledgements | Discarding an already-produced launch, attach or stop response and retrying its exact operation is idempotent. An attach grant retains its exact token across daemon restart. This covers completed-response loss; uncertain in-effect crashes are a separate source fixture gate. |
| Input ownership | A simultaneous two-client attach race yields exactly one durable writer grant. Conflicting operation reuse and forged release tokens are refused. A live native writer also blocks another writer after daemon restart. |
| Raw keys/read-only | A fixture through the actual TUI/tmux path receives exact UTF-8 Unicode, Escape, Ctrl-C and doubled-prefix literal Ctrl-G bytes and resize to 76×23. A 30×10 read-only observer cannot inject text or change the writer's dimensions. This is fixture evidence, not a claim about every vendor shortcut. |
| Groups/place/draft | Ten registered sessions contain two real native programs plus eight explicit fixture processes. Company/product membership, selected fixture session and a Unicode local draft survive detach and TUI restart. No prompt is delivered from the local draft. Human speed/accessibility targets remain unmeasured. |
| Native interrupt | Separate from the uninterrupted recovery journey, one Ctrl-C changes both native screens. Both native processes remain alive at the observation/reconciliation point. Native Ctrl-C can legitimately exit in other states; general survival is not promised. |
| Stop safety | The real API refuses stale generation and conflicting operation reuse. A lost stop response retries after daemon restart without another effect. The integrated source suite additionally covers delayed old stop against a synthetic replacement registry execution; this is not a live continuity API. |
| Durable journal | 59 events across ten threads have contiguous per-thread order and include durable launch intent, attempt, outcome, exact link and attach/release evidence. No private link nonce appears in the journal. |
| tmux loss | Killing only the owned tmux server and reconciling reports missing executions, preserves stopped state where applicable and launches no replacement. This is distinct from daemon restart and host reboot. |

Native setup is a separate gate. Earlier attempts in a new temporary workspace encountered Codex's update selector and new-folder trust guard. Escape could end that native invocation. A process-only project trust override did not satisfy the guard in this tested version; no persisted trust decision or upgrade was selected. Composer evidence therefore uses the previously trusted repository and an invocation-only update-check override.

This certifies a narrow local native terminal core on the stated combination. Terminal alpha remains open: native skill preload/invocation, verified account/model and authenticated turns, multiple credential realms, shared repairs, host reboot/native resume, real independent remote servers, model/account/harness continuation, Mattermost/IRC delivery and independent-user simplicity/accessibility remain missing evidence. Local logical hosts and raw-key fixtures cannot substitute for those gates.

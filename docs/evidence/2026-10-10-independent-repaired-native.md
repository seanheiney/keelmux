# Independent native acceptance after authority and transport repairs

Both actual native gateway journeys and all eight local native acceptance groups passed. This supports the stated local terminal combination; it does not certify the full terminal-alpha contract.

Tested worktree SHA: `498abebbd69f99bff9bb7542bac37a6815d71c5d`. Its implementation paths exactly match the captain's frozen SHA `4b36f4d773a31388c7ec8ade146eb1929a8d2a19`:

```sh
git diff --exit-code 4b36f4d -- cmd internal go.mod go.sum Makefile
```

That command returned an empty diff before and after the native runs. The worktree SHA reflects independently cherry-picked implementation aliases plus the explicit synchronization of the captain's committed CLI prerequisite. During acceptance, only owned tests/evidence paths were dirty. The evidence commit follows the tested SHA and does not change implementation paths.

Environment: macOS 26.7.1 (25G309), darwin/arm64, Go 1.26.6, tmux 3.7b, Codex CLI 0.159.0, Claude Code 2.1.296, protocol `bp/1`. Both native programs reused existing native login realms and the already trusted `/Volumes/DataSSD1/agent-backplane` repository. Effective account/model and native conversation identity remain unknown. No native model prompt, skill command, account/login change or upgrade was submitted. Codex used the same process-only update-check suppression and read-only sandbox as the earlier proof.

Commands run in `/Volumes/DataSSD1/agent-backplane-worktrees/acceptance`:

```sh
make build
python3 tests/native_gateway.py --report docs/evidence/native-gateway-repaired-2026-10-10.json --native-workspace /Volumes/DataSSD1/agent-backplane
python3 tests/native_acceptance.py --report docs/evidence/native-core-repaired-final-2026-10-10.json --native-workspace /Volumes/DataSSD1/agent-backplane
```

`make build` passed. The native gateway reported all three groups passing; the local harness reported all eight groups passing. The captain's broader build/race/vet, entry authority and gateway failure fixtures are separate receipts; this report does not substitute native terminal evidence for those gates.

| Artifact | SHA-256 |
| --- | --- |
| `tests/native_gateway.py` | `00255959715e48e1675f9ef55d3a76157dba4b786f20e537cd3f451c245fe3a6` |
| `tests/native_acceptance.py` | `4480a541aea72432f8f0f497841f6a4b358d1e3d7d4b80f985f4d64a3301248f` |
| Gateway helper `tests/gateway_acceptance.py` | `09ebded8b6046468b65c94ad330a63d0a90abf2dc4b83b65f172f7bad8958780` |
| `bin/bp` | `3eb78d7ff587097ab4cb2bacd6e99d57eb495413a841aba4356fcedee7082b65` |
| `bin/bpd` | `951a4e2ce2e112dd998dfe28045310a7215b72df722df29442e8d5b76163a098` |
| `bin/bpg` | `49a6b835913a471d316b4a41a590481a75a7fd4b4e04de971aefac2d2417e08e` |

The binary hashes remained unchanged after the runs. Native gateway and local receipts record the exact worktree SHA, harness hashes, environment and individual results.

| Check | Observed evidence and limit |
| --- | --- |
| Native gateway launch/link | Two separate real `bpd` processes launched the installed Codex and Claude programs. An inherited helper linked each exact child before native exec. The entry united only the two granted sessions and scrubbed home runtime locators. This proves owned correlation and native program launch, not native skill invocation. |
| Native rendering through entry | Each actual composer prompt crossed `bpd` → `bpg` → `bp drive` → the test PTY. Unicode draft characters were visible in the native pane. No Enter was sent in native mode. |
| Gateway terminal fidelity | Both programs received Escape and resize to 91×28. Ctrl-G d returned the actual TUI, confirmed exact lease release, preserved selected work/filter/all-work group, and left no native tmux client attached. |
| Gateway reconnect | Quitting and reopening the TUI reattached each same native home execution, runtime reference, PID and pane. The display reader was continuously pumped; this is ordinary native rendering, not a blocked-display stress gate. |
| Local native recovery | Both programs passed launch/link/Unicode/92×29 resize/detach/reconnect, TUI SIGKILL recovery and bpd SIGKILL recovery while attached. Runtime reference/PID/pane stayed identical, and a competing writer was refused. |
| Lost acknowledgement/ownership | Completed launch/attach/stop response loss was retried by exact operation. Attach grant survived restart. Forged link/release, conflicting operation reuse and stale generation were refused. A simultaneous cooperating-client race produced exactly one writer grant. Uncertain in-effect acquisition is covered by a separate adverse executable fixture. |
| Raw fixture/place | Two native programs plus eight explicit fixtures formed ten registered sessions. The fixture oracle received exact Unicode/Escape/Ctrl-C/literal prefix and resize, while read-only input/size stayed unchanged. Company/product selection and the local Unicode draft survived TUI restart. |
| Native Ctrl-C | Separate from uninterrupted persistence, one Ctrl-C changed each native screen. Both native processes remained alive at the observation point. General Ctrl-C process survival is not promised. |
| Registry/journal | 59 events across ten threads were contiguous and included durable lifecycle intents/outcomes. No private link nonce appeared in the journal. |
| tmux loss | Killing only the owned tmux server reported missing/stopped executions and created no replacement. This remains distinct from daemon restart and host reboot. |

The initial fresh native run exposed a harness false negative. Codex's actual composer used U+00BB `»`, while the predicate expected only U+203A `›`. Both gateway and the unchanged local harness timed out before typing a native draft. Safe diagnostics recorded a live pane, terminal query counts and screen line shapes without native banners, credentials or raw screen content. A private footer inspection identified the prompt glyph. The only existing local harness behavior change accepts either observed Codex glyph. The same frozen implementation then passed both full matrices. The initial unchanged local receipt is retained as `native-core-repaired-2026-10-10.json`; the diagnostic gateway receipt is `native-gateway-codex-diagnostic-2026-10-10.json`. They are predicate failures, not established implementation failures. The earlier gateway attempt and separate Claude-only pass are retained with their own exact harness digests.

All owned native daemons, entry processes, PTYs, private tmux servers and temporary state directories were cleaned. Existing user terminals and the default tmux socket were not used. Native credentials were left in their existing realms.

Missing evidence remains explicit: independent remote computers and enrollment/authentication, native skill loading/invocation, authenticated model turns with verified effective account/model, multiple credential realms, host reboot/native resume, native-rendering entry crash recovery, display backpressure stress, execution movement/continuity, Mattermost/IRC delivery and an independent-user simplicity/accessibility pilot. The local narrow core is demonstrated on this combination; the full terminal-alpha gate remains open.

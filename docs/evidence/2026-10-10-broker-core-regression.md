# Real native terminal regression on the Codex broker source

All eight local native acceptance groups and all three native gateway groups passed at frozen source `a0787357dc8c8cb3749deca43074fd4beafff1ca`. This independently checks the existing terminal paths after the broker work changed the durable store and service guards. It does not prove the Codex semantic broker journey or clear the full terminal-alpha gate.

The run used the clean `codex/broker-core-regression` worktree at `/Volumes/DataSSD1/agent-backplane-worktrees/gateway-latency-acceptance`. Before and after the native runs, the following returned an empty implementation diff:

```sh
git diff --exit-code a0787357dc8c8cb3749deca43074fd4beafff1ca -- cmd internal go.mod go.sum Makefile
```

Only the evidence files were added. No source, manifest, protocol or harness was edited. The native core and native gateway harnesses match the prior repaired acceptance digests. The gateway helper includes the captain's previously committed explicit refresh/selection synchronization, and its exact digest is recorded below. Both native runs passed on their first attempt; there is no omitted failure or diagnostic rerun.

Environment: macOS 26.7.1 (25G309), darwin/arm64, Go 1.26.6, tmux 3.7b, installed Codex CLI 0.159.0 and Claude Code 2.1.296, protocol `bp/1`. Local acceptance began at `2026-10-10T18:56:13Z`; native gateway acceptance began at `2026-10-10T18:56:54Z`. Both used the already trusted `/Volumes/DataSSD1/agent-backplane` workspace and existing native credential realms. Effective account/model and native conversation identity remain unknown. No native Enter, model prompt, skill invocation, login/billing change or upgrade was submitted.

Commands:

```sh
make build
python3 tests/native_acceptance.py --report docs/evidence/broker-core-regression-native-core-2026-10-10.json --native-workspace /Volumes/DataSSD1/agent-backplane
python3 tests/native_gateway.py --report docs/evidence/broker-core-regression-native-gateway-2026-10-10.json --native-workspace /Volumes/DataSSD1/agent-backplane
```

`make build` and both commands exited successfully. The existing harnesses ran without changes. This worker did not rerun the full race/vet suite; the captain owns that separate source gate.

| Check | Result and scope |
| --- | --- |
| Codex and Claude local native journeys | Both actual installed programs passed exact inherited link, unsubmitted Unicode draft, Escape, resize to 92×29, Ctrl-G d, TUI reopen/reconnect, TUI SIGKILL recovery and daemon SIGKILL recovery while attached. Session/execution/runtime/PID/pane stayed stable. A competing writer was refused. |
| Lost acknowledgement and writer arbitration | Exact attach operation retry preserved the same durable grant across response loss and daemon restart. Conflicting operation reuse and forged release were refused. A simultaneous two-client race produced exactly one writer grant. |
| Fixture keys and grouped place | Two actual native programs and eight fixture processes formed ten registered sessions. The fixture oracle received exact Unicode, Escape, Ctrl-C, literal prefix and resize. Read-only input/size stayed unchanged. Company/product selection and local draft survived reopen. |
| Stop and generation | Lost stop acknowledgement retry remained idempotent across daemon restart; stale generation and conflicting operation reuse were refused. |
| Native Ctrl-C | Each native screen changed; both processes remained alive at the observation point. This is a separate interrupt probe, not a general process-survival guarantee. |
| Registry/journal | 59 events across ten threads were contiguous and retained lifecycle intents/outcomes without private link nonce fields. |
| Owned tmux loss | Reconciliation reported missing/stopped executions and launched no replacement. This is distinct from host reboot or native resume. |
| Native gateway directory | Two real local `bpd` processes launched Codex and Claude. One actual `bpg` entry exposed exactly the two granted sessions and scrubbed home runtime locators. Both homes were on the same physical Mac. |
| Native gateway terminal journeys | Actual composer rendering crossed `bpd` → `bpg` → `bp drive` → PTY for both programs. Unsubmitted Unicode, Escape and 91×28 resize passed. Ctrl-G d returned the actual TUI, released the exact lease, cleared pending attachment and preserved selection/filter/group. TUI reopen reattached the same home execution/PID/pane. The PTY reader was continuously pumped. |

Artifact digests were checked before and after the runs and remained identical:

| Artifact | SHA-256 |
| --- | --- |
| `tests/native_acceptance.py` | `4480a541aea72432f8f0f497841f6a4b358d1e3d7d4b80f985f4d64a3301248f` |
| `tests/native_gateway.py` | `00255959715e48e1675f9ef55d3a76157dba4b786f20e537cd3f451c245fe3a6` |
| `tests/gateway_acceptance.py` | `fb4066ab7531bf58dc1459569e3a781e48bb9348d8f23dff0a99dbc51904f014` |
| `bin/bp` | `cac659f0ba230576f68ac50746ff2e492607866d24030bc8698ef26bf5ef7849` |
| `bin/bpd` | `17585c5be64c9cc4e53ad9257b8dfc438e018c830035a8d838a6747868b4de7b` |
| `bin/bpg` | `c527137fa31a1a23a91432b2b7b6e3dff86678b12f1eca804977cb9c137e3674` |

The unchanged harness receipts are [local native checks](broker-core-regression-native-core-2026-10-10.json) and [native gateway checks](broker-core-regression-native-gateway-2026-10-10.json). [Runtime/source/cleanup receipt](broker-core-regression-runtime-2026-10-10.json) records their hashes, commands, exact source, artifact digests and cleanup checks. Private scratch receipts captured the three owned `bpd` states and one owned `bpg` state while running; after completion, all observed temporary state was removed and no matching owned daemon, entry or tmux-socket process remained. The generated Python import cache was also removed. User terminals, default tmux and Cheers were not used.

The runs created fresh owned state and exercised daemon restart recovery. They do not establish an actual old-version registry migration with surviving native sessions; migration fixtures remain a separate source gate. Other missing evidence includes the Codex semantic broker native-link/bootstrap journey, authenticated model turns and verified effective account/model, independent remote computers and enrollment/authentication, native skill loading/invocation, host reboot/native resume, native-rendering entry crash recovery, display backpressure stress, execution movement/continuity, Mattermost/IRC delivery and an independent-user simplicity/accessibility pilot. The feature SHA has passed this legacy terminal regression; the earlier accepted terminal core remains separately identified by its original evidence.

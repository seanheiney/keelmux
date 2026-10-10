# Silent-home executable latency gate, 2026-10-10

Passed against implementation `4b36f4d773a31388c7ec8ade146eb1929a8d2a19` on macOS 26.7.1 arm64, Go 1.26.6, tmux 3.7b. All three executable build records name that revision with `vcs.modified=false`; their digests remained unchanged during the run. The new harness is committed separately as `1eb46eb70f0967f8531ef38cccc73107b7632111`.

The [JSON receipt](gateway-silent-home-latency-2026-10-10.json) records six passing groups, exact harness and executable digests, build identities, elapsed time, and cleanup results. The existing `GatewayAcceptance` setup created two owned actual `bpd` homes, separate private state directories and tmux sockets, distinct host identities, an actual `bpg`, and raw terminal fixture sessions. One home daemon received SIGSTOP while its tmux pane remained alive.

One actual `bin/bp --state CLIENT --entry SOCKET directory` invocation completed successfully in **3.009835 seconds**, within the **4.5 second** gate. It returned exactly the healthy granted session, labeled its home reachable, and labeled the distinct stopped home unreachable. Runtime locators remained scrubbed; no automatic local home appeared.

The actual PTY-hosted TUI then displayed `1 execution homes unreachable; entry remains connected.` without an entry-offline label. With the second home still stopped, it selected healthy work, attached through the entry to the healthy home's native tmux client, delivered a unique raw-input fixture probe only to that home, and detached with the same saved selection and no pending acquisition. Both original pane PIDs survived. SIGCONT restored both original reachable sessions and execution identities. Cleanup resumed the paused daemon before stopping all owned processes and removing the owned temporary directories.

This is same-computer executable process-fixture evidence. It does not establish independent remote hosts, real network partitions or enrollment, a latency distribution under load, native provider model turns, host reboot recovery, or independent-user usability. No model prompt was submitted, existing user terminal was touched, or implementation source was changed.

Run from the harness checkout, pointing `--root` to the checkout holding the frozen binaries:

```sh
python3 -B tests/gateway_latency.py \
  --root /Volumes/DataSSD1/agent-backplane \
  --expected-sha 4b36f4d773a31388c7ec8ade146eb1929a8d2a19 \
  --report docs/evidence/gateway-silent-home-latency-2026-10-10.json
```

| Artifact | SHA-256 |
| --- | --- |
| `tests/gateway_latency.py` | `6589dd429a48f2517f37c6163718a6853a013af36b6d37d221be3059f2bb4d6b` |
| Shared `tests/gateway_acceptance.py` | `09ebded8b6046468b65c94ad330a63d0a90abf2dc4b83b65f172f7bad8958780` |
| Shared `tests/native_acceptance.py` | `ecb947840145bc98c1367ff4a394165f8d84fc86cc4d6ff523424c67df379db8` |
| `bin/bp` | `e91b4e6b73f8d0daf7e41d206f8b7c61409bd93a638a477cacdf6ed609d2a71d` |
| `bin/bpd` | `090679acb535354cfee1af42cc79b0b7071abec5b625d619feed6ed89e7e3d91` |
| `bin/bpg` | `d8319da643b121faed40a7e37273ce64316407fbaa34fc6b31bdc3a68c5440b9` |

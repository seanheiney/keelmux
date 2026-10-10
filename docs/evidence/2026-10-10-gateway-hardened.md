# Private entry authority and terminal recovery gate

Implementation `4b36f4d773a31388c7ec8ade146eb1929a8d2a19` passed `make build check` on macOS 26.7.1 arm64 with Go 1.26.6 and tmux 3.7b. This built `bp`, `bpd` and `bpg`, passed all race tests and vet. `GOOS=linux GOARCH=amd64 go build ./cmd/...` passed compilation; Linux runtime behavior remains untested.

Independent review first reproduced an entry-restart authority gap after a home acknowledgement was lost, a directory grant-revocation leak and blocked terminal display cancellation. The repair records and fsyncs the original principal and exact acquisition before calling the home, reuses that durable claim for direct open and acknowledgement recovery, projects inventory against the final current grant set, and bounds concurrent home fetches. Private-directory creation and stable host identities now fail closed on malformed identities and preserve uncertain bytes. Terminal IO uses bounded nonblocking reads/writes and joins workers before restoring terminal mode and descriptor flags.

The [executable authority receipt](gateway-authority-hardened-2026-10-10.json) passed two adverse restart cases plus setup. A counted private home proxy discarded a completed attach response. After entry restart, a different configured principal with the public IDs was refused before another home call; the original principal recovered the same grant. Direct native open without a prior entry attach also preserved original ownership across restart. Tokens stayed in memory and are absent from the receipt.

The [six-group gateway receipt](gateway-hardened-2026-10-10.json) passed scoped inventory, actual TUI routing and raw bytes, read-only isolation and writer contention, entry SIGKILL/recovery, startup-config revocation with exact release, partial home availability, and attached-home partition/repair. Two actual daemon processes, private state directories and tmux sockets ran on one computer. The fixture sessions are raw input oracles, not model CLIs. Home execution IDs, PIDs and panes survived supported daemon/entry recovery; no replacement execution or unintended local home was launched.

Commands against the frozen binaries:

```sh
python3 tests/gateway_authority.py --report docs/evidence/gateway-authority-hardened-2026-10-10.json
python3 tests/gateway_acceptance.py --report docs/evidence/gateway-hardened-2026-10-10.json
```

The six-group harness now explicitly refreshes inventory and selects the exact requested work after home recovery. The first attempt assumed API readiness also meant the independently polled TUI was ready. A second attempted assertion waited for an unchanged row to be redrawn, which Bubble Tea need not do. Both harness failures are retained in [selection observation](gateway-selection-observation-2026-10-10.json) and [render observation](gateway-render-observation-2026-10-10.json). The correction preserves the exact selected-ID assertion and explicit user selection; it does not make background arrivals steal focus or change product implementation.

Separate [silent-home latency evidence](2026-10-10-gateway-silent-home-latency.md) proves one actual directory command returns partial healthy inventory within the 4.5-second gate while another owned daemon is stopped. Separate [native provider evidence](2026-10-10-independent-repaired-native.md) proves both real Codex and Claude native gateway journeys and the eight-group local matrix against an implementation-equivalent checkout. These receipts have their own harness and binary digests; they are not interchangeable.

The source regression tests also cover full-pipe cancellation with raw mode and descriptor flags restored, inherited registry-lock descriptors, crash boundaries around acquisition receipts and skill removal, final grant projection, and malformed host identity retention. Actual power loss, arbitrary blocked regular-file IO, independent remote hosts/enrollment, live executable configuration reload, authenticated model turns, native continuity, and Mattermost/IRC delivery remain unproved. No public release or deployment occurred.

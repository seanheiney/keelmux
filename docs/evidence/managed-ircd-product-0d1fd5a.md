# Managed IRC local product acceptance

Passed on clean source `0d1fd5ad8af05fe0d1f531a9a6519206e166721b`, based on integrated root `b99a5f97d82ba4e64853016903c9d24cc95f9585`, on macOS arm64 with Go 1.26.6. The opt-in acceptance test passed under the race detector in 12.45 seconds (package invocation 13.871 seconds); `go vet ./cmd/bpd` and `git diff --check` passed. The adjacent JSON receipt records exact binary and runtime digests, operation IDs, registered owner and timestamp, process lifetimes, and finite check results.

The test built the actual `keelmux`, `keelmuxd`, and `keelmux-supervisor` executables with source linker identity. Their test environment had a PATH containing only those owned binaries. An owned short `/tmp` state directory held all generated configuration, authentication, channels, journal, and inbox state. The shell fixture used `/bin/sh` and a byte input sentinel; it invoked no provider or model and received no native input.

Offline provisioning used Ergo 2.19.1, source `63c743a70644f0f19109508ade8580bf37f7d23d`, from the macOS arm64 archive with SHA256 `1bd97a0917036061e2dcdfc29149c2e252e3bc856282e0956afa2aaaa54dd787`. Installing the verified offline archive is the harness setup step. Init, start, account creation for `alice`, channel registration and binding, queued bot reply, control restart, IRC replacement, stale stop refusal, and fixture stop used actual CLI/API paths.

Verified behavior:

- Startup left IRC and tmux state absent until explicit IRC initialization. Repeating the exact IRC start operation preserved its original native instance.
- Authenticated API confirmation proved the deterministic stable thread channel, exact `keelmux-bot` owner, original registration operation, and registered timestamp. Alice connected through the real IRC adapter with the manager's scoped private CA and SASL services account proof.
- Alice's note entered the durable authored inbox under the allowlist. Text resembling approval or shell instructions remained an authored note; semantic dispatch stayed disabled and the native byte input sentinel remained empty with no writable terminal clients.
- An explicit CLI bot reply was durably queued, reached Delivered through its matching server echo, and arrived at Alice with the same server message ID. Reusing the same message operation before and after control restart preserved the receipt and produced exactly one observed reply, including a one second observation after retries.
- Replacing the control daemon changed its instance and PID while preserving the exact native fixture and IRC child/helper process lifetimes. Authored history persisted, the bot adapter reconnected, and a subsequent Alice note entered the inbox.
- Explicit IRC replacement created a fresh native instance. A stale stop could not stop it. Registered channel ownership, original registration operation and timestamp, and Alice's account survived replacement.
- Exact authenticated cleanup stopped the fixture, managed IRC execution, and control daemon. All three owned native execution/helper pairs exited; the owned temporary state was removed after successful cleanup.

Reproduce from a clean checkout containing the acceptance test, using the appropriate pinned archive for the host:

```sh
KEELMUX_MANAGED_IRCD_ACCEPTANCE=1 \
KEELMUX_ERGO_ARCHIVE=/absolute/path/ergo-2.19.1-macos-arm64.tar.gz \
KEELMUX_MANAGED_IRCD_REPORT=/absolute/new/managed-ircd-product-receipt.json \
go test -race ./cmd/bpd -run '^TestManagedIRCDProductAcceptance$' -v -count=1
```

Without the explicit opt-in, the test skips and performs no provisioning or network activity. The receipt path must be a new absolute `managed-ircd-product-*.json` file. Failure retains only the owned private test state for repair and reports bounded diagnostics without credential bytes.

This is local integrated product proof for the recorded source and host. It is separate from package qualification, other operating systems, production or customer use, reader receipt, and model acceptance. IRC intake remains bounded live coverage; disconnects report a history gap. A registration intent consumed before unsuccessful authentication can still require manual repair because unknown mutations are not replayed.

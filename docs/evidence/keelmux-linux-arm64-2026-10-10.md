# Packaged Linux arm64 runtime acceptance

The clean Keelmux v0.1.0 archive built from `0be153b2ae2fc2ad0d47120287bcdce2f8f3cc44` passed all eight terminal and durable-state groups as non-root UID/GID 65534. This proves packaged CLI, daemon, TUI, and tmux behavior with an authored raw terminal fixture. It does not establish native Codex or Claude behavior on Linux.

The consumed archive SHA-256 is `38c04ebb0d2815bebea4f4966e0cbd254412072fe4cf16c0ac6210b083df4aa3`. Its manifest and executable build metadata agree on the exact clean source, version, Linux arm64 target, and Go 1.26.6. Every archived binary and documentation digest was verified before execution. No checkout-built executable was substituted.

The runtime was an owned Debian bookworm container on Docker Engine 29.2.1, Linux kernel 6.8.0-100-generic, aarch64/glibc 2.36, tmux 3.3a, and Python 3.11.2. Debian image digest: `sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251`; arm64 manifest digest: `sha256:0c8bbb8e987a035fe1d9704eb2e571b7e9a836e1caa46345290674b45b69e417`. The container had no mounts or host credentials, was not privileged, and was disconnected from Docker networking during the test. Its private fixture HOME and state were created inside its own `/tmp` tree.

## Passed groups

1. Checksum-pinned archive, finite extraction, manifest file hashes, canonical and compatibility executable version metadata.
2. Real packaged daemon and dedicated tmux launch, exact inherited link, forged nonce refusal, a completed launch acknowledgement deliberately discarded, exact retry after daemon SIGKILL, and unchanged session/thread/execution/PID/pane.
3. Real packaged CLI attach/detach/reconnect, raw Unicode, Escape, Ctrl-C, doubled Ctrl-G, 76×23 resize, read-only input refusal, and preservation of writer dimensions.
4. Actual packaged TUI, local saved draft and exact selection, daemon SIGKILL while attached, TUI SIGKILL, exact saved grant release, and reconnection to the same runtime.
5. Completed attach acknowledgement deliberately discarded, identical durable grant after restart, forged release refusal, retired operation refusal, and exactly one writer grant in a concurrent two-client race.
6. Stale stop generation refusal, discarded completed stop acknowledgement, exact retry after restart, and conflicting operation target refusal.
7. Persisted host identity, second daemon refusal, private authority/client-state modes, and 24 contiguous journal events across two fixture threads without link nonce disclosure.
8. Owned tmux destruction projects the running fixture as missing; repeating the original launch and restarting the daemon preserves the original execution and performs no replacement launch.

The passing machine receipt is [keelmux-linux-arm64-2026-10-10.json](keelmux-linux-arm64-2026-10-10.json). Executed harness SHA-256: `5f6d29ac11a9a4216d76306044cdbed384182a6eda217f762f245205b746ffdc`; harness source: `260174b` (before the subsequent optional daemon lifecycle extension).

```sh
docker exec --user 65534:65534 keelmux-validation-20261010 \
  python3 /probe/linux_release_smoke.py \
  --archive /probe/release.tar.gz \
  --archive-sha256 38c04ebb0d2815bebea4f4966e0cbd254412072fe4cf16c0ac6210b083df4aa3 \
  --source-sha 0be153b2ae2fc2ad0d47120287bcdce2f8f3cc44 \
  --environment /probe/environment.json \
  --report /probe/nonroot/result-final.json
```

All tracked terminal clients and directly spawned daemons were reaped. Exact recorded pane and tmux server PID/start lifetimes were no longer live, and the owned fixture temporary directory was removed. Container removal is a separate final cleanup receipt after the forthcoming release rerun.

## Preserved diagnostics

- [First run](keelmux-linux-arm64-first-2026-10-10.json): groups 1–3 passed; the harness sent Enter immediately after Escape without waiting for the TUI's draft-save render. It did not establish a TUI attachment within the deadline. Its cleanup also sampled a still-exiting pane immediately after tmux shutdown. This receipt remains failed.
- [Second run](keelmux-linux-arm64-second-2026-10-10.json): all eight groups passed after observing the draft-save render. Final cleanup correctly remained failed because an already-destroyed tmux server had left a socket pathname, and the harness treated a redundant shutdown refusal as failure. Cleanup was corrected to observe exact recorded server and pane lifetimes; the final non-root run passed without product changes.

The draft-render wait establishes readiness before an attachment action. It does not loosen identity, authority, or runtime predicates. The cleanup correction treats a socket pathname and a live server as distinct facts and checks exact process lifetimes.

Linux amd64 runtime, native Linux Codex/Claude journeys, authenticated model turns, native skill invocation, account/model entitlement, independent remote servers/SSH, reboot/native resume, and independent user speed/accessibility pilots remain separate gates. New daemon `status/stop/restart` product controls are outside this consumed archive and require a new exact-artifact run using `--daemon-lifecycle`.

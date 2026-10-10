# Native pre-attach readiness fixture correction

The hosted macOS race run for source `6a6b20cd1def5e6ad0e93d4e59020a79659c89f3` failed `TestPreAttachQueriesBecomeReady` on 2026-10-10 at 23:30:38 UTC. The failure at `screen_test.go:112` showed a valid blank 80x24 screen and cursor at 1;1, before `READY-WITHOUT-CLIENT` had been consumed. The original failure is retained in `/Volumes/DataSSD1/agent-backplane/work/full-features/pr-macos-6a6b20c.log`, SHA-256 `19c887117d9c51b1cafa6048163d2a369a336f6356433efb4e2926414b93943e`.

This correction was developed in the isolated `codex/native-ready-sync-fix` worktree from `6e69bc4f3ceeceaeb19a8e085495993b8197970a`. Only the test fixture and this evidence record change; production Go is unchanged.

## Cause and causal synchronization

The child published the original readiness file after its initial CPR and DA answers but **before** printing the marker. Those protocol answers acknowledge earlier output, so observing the file does not establish that future output exists. Even publishing immediately after `printf` would leave the asynchronous PTY drain unordered with attachment.

The corrected child prints the full 20-character marker, emits a second CPR, reads its exact seven-byte `ESC[1;21R` reply, then atomically publishes the answer file. The test compares all three replies exactly: `ESC[1;1R|ESC[?1;2c|ESC[1;21R`. The tracker processes the preceding text before issuing that second reply. The original three-second deadline, full-marker initial snapshot assertion, future-input assertion, and reconnect assertion are retained.

`TestQueryAnswersDoNotAcknowledgeFutureOutput` is a deterministic negative control. An owned private FIFO blocks the child before printing the future marker. After the same initial CPR/DA answers used by the old readiness condition, a real read-only attachment correctly lacks the blocked marker. Releasing the FIFO permits the second CPR barrier; live output and a subsequent snapshot must contain the entire marker. The FIFO descriptor remains open until the child acknowledges rendering, so release bytes cannot disappear before a delayed reader opens it. This proves the ordering error without scheduler assumptions or added sleeps.

## Final local validation

On macOS:

- `go test -race ./internal/adapters/nativepty -run '^(TestPreAttachQueriesBecomeReady|TestQueryAnswersDoNotAcknowledgeFutureOutput)$' -count=100`: passed 100 corrected cases and 100 negative controls, 17.737s. Retained `work/pre-attach-ready-final-controls.log`, SHA-256 `c370f9ff412de106ea97ddb65aead7f1072398a36acb290d212fb0e7a717933e`.
- `go test -race ./internal/adapters/nativepty`: passed, 8.527s. Retained `work/pre-attach-ready-package.log`, SHA-256 `5efd10acfa43461f5567af7c9efd43e083d7e8c6cb3a72d7f93129938ff6f0b3`.
- `go vet ./internal/adapters/nativepty`: passed.
- `GOOS=linux GOARCH=amd64 go test -c ./internal/adapters/nativepty -o work/pre-attach-nativepty-linux.test`: passed compilation. This is not Linux runtime qualification.
- `git diff --check`: passed.

Linux runtime was attempted using the already present `golang:1.26.6-bookworm` image, no network, no image pulls, and read-only source/cache mounts. Colima rejected the `/Volumes` mount before running the test. A second attempt with an owned tracked-source copy under `/Users` stalled Docker container creation; its output log remained empty and no container existed. Only that exact owned Docker client was terminated (exit 143). The two container nonces were `keelmux-native-ready-6e69bc4` and `keelmux-native-ready-6e69bc4-copy`; both were verified absent with `docker ps -a`. The owned copy `/Users/sean/Documents/Codex/2026-10-10/agent-backplane-captain/work/native-ready-linux-01w3kfrc` was removed. Retained attempt logs are `work/pre-attach-ready-linux-runtime.log` and `work/pre-attach-ready-linux-runtime-copy.log`. No Linux test executed in these attempts; runtime remains unqualified by this record.

No model calls, provider changes, customer sessions, credentials, installed services, production code, shared root checkout edits, pushes, or release claims were involved.

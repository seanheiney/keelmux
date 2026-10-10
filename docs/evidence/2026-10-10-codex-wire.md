# Real Codex Unix frame transport

On clean source `3335c076d643525ab6277a56723263b02d945a16`, the opt-in Go race test initialized two actual Codex 0.159.0 app-server WebSocket connections. The exact string and signed-int64-maximum IDs were acknowledged. Both responses named the canonical disposable native home. No thread or model turn was created. [Sanitized receipt](codex-wire-2026-10-10.json).

Command: `BP_NATIVE_CODEX_WIRE=1 BP_CODEX_NATIVE_BINARY=<installed native binary> go test -race -json ./internal/codexbroker/wire -run TestNativeCodexUnixInitialization -count=1`. The recorded binary digest identifies the installed native executable; no upgrade or credential copy was used. macOS 26.7.1 arm64, Go 1.26.6. The package passed in 1.469 seconds, including race cleanup; the native case took 0.21 seconds. The ordinary test command explicitly skips this native gate unless opted in.

The initial interoperability attempts refused the requested endpoint because Codex creates a symlink, including when the owned launcher used umask 077. The actual socket lives in a private per-user native directory. The test now explicitly resolves the owned startup alias, then leaves the transport's exact owned-directory/socket checks intact. The real socket was already mode 0600; it was not chmodded. A separate initial assertion was corrected to compare canonical disposable homes because `/tmp` and `/private/tmp` are aliases. These were test compatibility corrections, not privacy-policy exemptions.

The test uses empty isolated HOME/CODEX_HOME, a no-auth custom provider at loopback port 1, disabled apps/plugins and private owned state. It waits for its owned primary process to exit and removes only the exact observed socket inode if still present; it never removes the shared native parent. Native output and auth/config bodies are not recorded. The isolated directory is removed after cleanup.

This proves transport initialization only. It does not certify policy/authority integration, real native CLI resume through the relay, product daemon launch/recovery, authenticated account compatibility, model work, callbacks, handover or Linux runtime behavior.

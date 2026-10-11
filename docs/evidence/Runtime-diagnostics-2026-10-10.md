# Read-only runtime diagnostics

Source commit: `7419b387acc2beee66f1c80b0b80a0688077d11f`.
Base: `71d831f05405b68fc80cb38132b321f3ba1e6cc2`.
Branch: `codex/runtime-diagnostics-features`.
Environment: macOS 26.7.1 arm64, Go 1.26.6.

## Implemented contract

`cmd/bp/diagnose.go` provides `diagnoseCommand(stateDir, entry string, args []string) error`, a writer-testable helper, human output, JSON, help, and explicit `--probe-versions`. Captain owns dispatch in `main.go`; it must occur before `Ensure`, `ensureDaemon`, or any status helper that can initialize state. This source commit does not itself expose a dispatched product command.

The default inspects existing files without creation, chmod, sync, migration, journal recovery or repair. A shared read-only descriptor helper rejects private leaf symlinks, hardlinked files, special files and unsafe owner/mode, anchors child reads, and permits platform aliases in ancestors such as `/var`. Client-place decoding reuses the existing strict parser without exposing draft text, pending attachment tokens or other private values.

An offline registry inspection requires the existing private authority lock and a nonblocking shared lock. It refuses any WAL/SHM/journal entry and WAL-mode database header. SQLite opens with `mode=ro`, `immutable=1`, `query_only(1)` and `trusted_schema(0)`. Integrity and existing authority/channel/project validators run with a two-second context. No `store.Open`, schema migration or implicit lock creation occurs. Only counts, last event time and saved runtime references reach the diagnostic aggregator. Registry snapshots are limited to 64 MiB; unsupported schema, oversized state and active locks remain explicit.

Existing daemon inspection requires a private socket and valid saved host identity. One Unix connection binds the kernel UID/PID and boot/process-start lifetime to an exact lifecycle instance. After that check it reads only `/v1/channels` and `/v1/sessions`, then checks the same lifecycle instance, socket inode, process lifetime and saved host identity again. Returned sessions must match the saved host ID. Each body is capped at 64 KiB, the connection at 256 KiB total and two seconds. The current DTO representation must be canonical and unambiguous. Large or future-format responses can therefore be unavailable even when a daemon is healthy.

A socket path alone is only `socket_present`. `owner_process_verified` proves the owner process and same-connection lifecycle consistency; it does **not** prove loaded executable authenticity or an authenticated daemon host binding. The lifecycle protocol has no host/process-start fields. No saved PID is signalled. Saved native lifetime references are checked against the kernel without contacting native helpers; malformed or unsupported identity formats remain unavailable. A matching lifetime does not establish terminal readiness.

Managed IRC checks reuse the existing configuration and runtime-record validators through narrowly injected read-only directory checks. They validate configuration records, artifact digests and release metadata without `New`, `Status`, locks, downloads, native commands, API probes or authentication. This is local receipt validation, not independent archive provenance or live Ergo proof. Channel configuration is validated offline; a running daemon supplies only a redacted projection and freshness counts.

Providers are static PATH/filesystem observations by default. `--probe-versions` permits only supported Codex/Claude `--version` argv, at most three seconds per process, bounded output, no stdin, discarded stderr and process-group cleanup on cancellation. Only recognized version strings are projected. Grok remains unsupported. No auth/status/login command is used. Remote entry sockets receive static checks only and are never contacted.

Findings have finite states, summaries and next actions. Saved-event/channel-check freshness uses a stated five-minute diagnostic window; future timestamps are invalid. This threshold is a display assessment and grants no authority. A completed report returns success even when individual checks report missing/invalid/unavailable state; argument/output failures return errors.

## Validation

All at the source commit above:

```
go test -race -count=1 -timeout=60s ./internal/diagnosticfs ./internal/diagnostics ./internal/store ./internal/ircd ./internal/clientstate ./cmd/bp
```

Passed: diagnosticfs 1.216s, diagnostics 2.816s, store 9.528s, ircd 6.813s, clientstate 1.906s, cmd/bp 2.781s.

```
go vet ./internal/diagnosticfs ./internal/diagnostics ./internal/store ./internal/ircd ./internal/clientstate ./cmd/bp
git diff --check
GOOS=linux GOARCH=amd64 go test -c -o <scratch>/runtime-diagnostics-linux.test ./cmd/bp
```

All passed. Linux is compilation-only, not runtime qualification.

Fourteen new top-level tests plus adverse subcases cover absent state without creation or executable invocation; unsafe files and symlinks; strict client-place parsing; private content redaction; valid, corrupt, locked and unlocked registries; missing/public/linked authority locks; forbidden sidecars; exact database/configuration byte preservation; stale versus missing runtime lifetime evidence; same-user false PID, duplicate lifecycle identity, changed instance and foreign-host inventory rejection; bounded/cancelled responses; remote-entry nonconnection; exact opt-in version argv, unrecognized-output redaction and cancellation; and IRC receipt corruption without launch or lock creation. Native-version subprocess tests use owned fixtures, not real model providers. A duplicate-identity test initially returned while holding its fixture mutex; that test-only error was corrected and the full final race suite passed.

## Remaining gates

Captain CLI dispatch and integrated exact-SHA validation remain separate. No native runtime, model turn, terminal input, IRC server, customer channel write, authentication change, repair, remote publish, release or deployment was performed. No model, auth, account, quota or native resume capability is inferred from these diagnostics.

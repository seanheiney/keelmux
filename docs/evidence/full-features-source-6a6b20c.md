# Fuller feature source acceptance

Source: `6a6b20cd1def5e6ad0e93d4e59020a79659c89f3`, clean at build.
Environment: macOS arm64, Go 1.26.6. Executables report development metadata, not a published version.

| Executable | SHA256 |
| --- | --- |
| keelmux | 0e15a812e3ed39ffadd0bbaafb76f5cb5253924b25bfce03c62ac6aa19a7661b |
| keelmuxd | 6b31801a5c91f61b5445663220fb37f91ab7eff670a30046ea23544749f64f1c |
| keelmux-entry | 89a6622cae240fb17fbd82fdee6660546145b3059b725c566f30b75ba56e45ae |
| keelmux-supervisor | 40888cc29e55cd8659e6528cab33952ae1544f2cd1bf777ede8af6228323ce70 |

## Actual command and terminal proof

The compiled CLI passed 25 steps: four command help paths and absent-state reads without creating state; nested and overlapping group membership, rename, atomic cycle rejection, idempotent removal, active selection, deletion with filters and unsent drafts preserved; manual usage import and identical replay; scoped projection; rejection of forged provider provenance without changing ledger bytes; populated read-only diagnosis. Owned fixture state was removed. No daemon, provider, model or external service was started by this command journey.

The actual switcher usage panel was captured at 80×24, 120×32 and 40×14. Each capture used deterministic inventory and one explicit user-entered observation, one read-only inventory GET, zero mutating requests and normal quit without signals. Accounts/money stayed hidden and unknown measured model/tokens stayed unknown. [Capture receipt and asset digests](../site/evidence/usage-6a6b20c/receipt.json). PNGs rasterize actual interpreted PTY cells; they are not native Terminal screenshots.

The owned loopback SSH product test used the actual daemon, peer-export command and enrolled client with strict generated host-key pinning. It passed exact host/session/principal checks, grant narrowing and revocation, TTL/unavailability, stopped daemon, private diagnostics and process cleanup. The first run failed at SSH authentication before managed sessions; moving the test's authorized keys beneath private owned temporary ancestors repaired the fixture without weakening StrictModes. [Exact SSH proof and limits](../../internal/peers/PRODUCT_SSH_FIXTURE.md). This qualifies two execution homes on one computer, not an independent physical server or remote terminal attachment.

## Combined source gates and retained failures

`go test -race -p 1 -count=1 ./...` and `go vet ./...` passed locally. Installer 22 groups, package-harness six cancellation/identity controls and documentation ten acceptance groups passed. [Hosted push workflow](https://github.com/seanheiney/keelmux/actions/runs/38095185819) passed all six jobs, including macOS/Ubuntu race/vet, unchanged pinned UI tests, installer controls, actual archive smoke and four-target packaging.

The separate [PR workflow](https://github.com/seanheiney/keelmux/actions/runs/38095208271) failed macOS `TestPreAttachQueriesBecomeReady` and passed its other five jobs. Its fixture published a readiness file before printing the expected terminal content, so the current snapshot could correctly be blank. This failure is retained and holds the candidate until the fixture's actual terminal-output ordering barrier and fresh final-source gates pass. No timeout or content assertion was relaxed.

The earlier interrupted-shell lifecycle failure and its independent controls remain in [the lifecycle fixture investigation](../../internal/adapters/nativepty/LIFECYCLE_FIXTURE_EVIDENCE.md). Initial CLI fixture input used an invalid thread ID and was correctly rejected before mutation; the corrected valid fixture passed. These failed attempts are retained privately alongside passing receipts.

## Limits

This is source qualification, not installed-release qualification. The new commands are not present in frozen v0.2.1 archives. Native providers, packaged managed IRC, installer lifecycle and anonymous public installation require fresh final archives. No model turn was submitted. The native structured-controller constructor and launcher remain disabled; no account/quota reader, semantic prompt dispatch or independent-server continuity is enabled by these results.

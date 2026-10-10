# Codex controller bounded peer review

Reviewed source: `f4b0e22fbf668048be852d71c1af844aa8f840e2` (the source also
present beneath documentation-only `b9a1f78`). Repair source:
`7c995cf3e7891cca00470cab8ec1e9d10ebb8546`, branch
`codex/controller-peer-hardening`. No author or captain worktree was edited.

This review covers the experimental controller contracts, journal, lifecycle,
fixture tests and implementation document. It does not establish real upstream,
shared authority, process, model or provider behavior.

## Findings and regressions

| Defect in reviewed source | Bounded before evidence | Repair and after evidence |
| --- | --- | --- |
| Raw native-home path comparison permits a new realm revision plus symlink alias to launch a second primary for the same native thread. | `TestNativeHomeAliasCannotBypassLiveThreadFence` failed: nil error, two fixture launches. | Canonical existing owned directories are checked through opened descriptors. Canonical paths and device/inode identities are retained and rechecked; alias and replacement regressions pass. |
| Ordinary JSON decoding accepts ambiguous journal input that can erase one-shot history. | `TestJournalRejectsNoncanonicalJSON` accepted duplicate Version, case-aliased version, an unknown field, and duplicate Entries with a null reset. | Version 2 requires exact canonical writer representation. All four malformed inputs now refuse. Version 1 fails closed without automatic migration or deletion. |
| Journal validation inspects pathnames before use; ledger open follows links and can block on a replaced FIFO before regular-file validation. Lock validation also checks the pathname rather than its opened descriptor. | Source inspection of `journal.go`: Lstat followed by os.Open; lock opened without nonblocking mode and then checked via Lstat. No adversarial filesystem race was claimed as executed. | Directory-relative nofollow/nonblocking opens with descriptor owner/mode/type/single-link validation; descriptor-relative atomic writes and fsync; changed directory/lock identity fails closed. Symlink, FIFO, hardlink, public-mode, replaced-directory and replaced-lock tests pass. |
| Formatting the exported journal handle exposes the private capability through its internal map. | `TestJournalFormattingDoesNotExportCapability` failed using `%v`. The capability was compared in memory and never printed. | Journal handle String/GoString redact and JSON serialization refuses. `%v`, `%+v`, `%#v` tests pass. |
| Executable validation blocks when the executable path is replaced by a FIFO, retaining the common lifecycle gate. | `TestExecutableFIFORefusesWithoutBlocking` failed after 100 ms; its negative-test cleanup safely released the blocked reader. | Nonblocking executable open precedes the existing regular/executable/size/hash checks. The regression passes without a supervisor effect. |

These are local journal/configuration integrity and availability findings, not
proof of a remote attacker or isolation from the same OS user. The executable
path hash still does not prove the eventual running image; the required real
adapter must handle that separate launch race.

## Validation

The tested source tree was committed unchanged as repair source `7c995cf`.
Environment: macOS 26.7.1 (25G309), arm64, Go 1.26.6.

- `go test -count=1 -race ./internal/codexcontroller`: PASS, 3.460 s; 25 top-level
  tests, including 8 added peer-review regression tests and their subtests.
- `go vet ./internal/codexcontroller`: PASS.
- `GOOS=linux GOARCH=amd64 go test -c -o <isolated-scratch-output> ./internal/codexcontroller`:
  PASS. Compilation only; no Linux runtime claim.
- `git diff --check`: PASS.

No model/provider launch, native app-server, native UI, model turn, credential
read, auth/config/trust change, external message, tracking write, production
process or remote host was exercised. Fake binary files were never executed.

## Integration verdict

The repair source is suitable for captain review and integration as a disabled
experimental library. Existing durable effect-before-acknowledgement ordering,
one-shot restart behavior and common-gate-before-journal lock ordering remain in
place. The journal lock is not writer authority; no grants are created or retired
by this library. Public View redaction and Launch serialization refusal remain.

Product enablement remains blocked on a reviewed real common Service/store gate,
authenticated upstream with exact loaded-thread/realm/child/socket provenance,
actual isolated zero-turn primary lifetime and restart qualification, and separate
authorized model-turn/approval qualification. Fixture lifetime and identity checks
do not replace those gates. Search of Go sources found no default product wiring
or concrete non-fixture `WithPrimaryLifecycle` implementation.

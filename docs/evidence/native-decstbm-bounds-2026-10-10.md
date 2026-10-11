# Terminal scroll-margin bounds correction — 2026-10-10

Base source: `4ef3c9afce086a07abd4646bd6c428abe8d43b90`. The private original controls and logs are retained; their SHA-256 values are recorded in the adjacent JSON.

The exact 15-byte trace `ESC [ 2 ; 3 r ESC [ 2 ; 1 H ESC [ M` on a 1-column, 2-row screen panics on the unchanged base. The pinned DECSTBM handler accepts a bottom margin beyond the screen; the later DeleteLine reaches an out-of-range buffer row. This baseline failure is independent of the separate scrollback throughput change.

The existing screen CSI hook now refuses out-of-screen top/bottom margins before the pinned handler changes its region or cursor. The matching delayed-wrap tracker keeps its old state for rejected bounds. Valid and default bounds continue through the pinned handler, and the visible stream still contains the original margin sequence. No dependency update, parser replacement, panic recovery, output suppression, or deadline change is involved.

Seven focused test functions cover the exact crash, prior valid state and cursor replies, default/in-bounds parser equivalence, chunked Unicode and delayed wrap, height changes after resize, finite malformed bounds through scrolling operations, and alternate/main regions. A read-only negative control keeps the screen guard but substitutes the original wrap tracker; the Unicode/delayed-wrap test fails, proving the second guard is necessary.

The full `internal/adapters/nativepty` package passed `go test -race -count=1` (11.759 seconds), scoped `go vet` (0.187 seconds), and Linux amd64 compilation (0.702 seconds). Independent source review found no blocker and ran no duplicate workload.

This is source correctness proof on macOS arm64 plus compilation for Linux. The full package tests include existing owned shell/helper PTY fixtures. No native provider process, model turn, customer message, or real native configuration was used. Captain owns integrated and installed release qualification; frozen candidate archives and prior receipts remain unchanged.

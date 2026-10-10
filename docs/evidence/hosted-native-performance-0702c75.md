# Hosted native framing and drain investigation

Transport repair source: `0702c75df200e76a173e967654eda2124e925ffa`, based on
`54fda85e112dfcc901c6635eecb222e4d90e2c94`. See the accompanying JSON for
source hashes, commands, results and missing evidence.

[Hosted run 38089912539](https://github.com/seanheiney/keelmux/actions/runs/38089912539)
failed the unchanged eight-second slow-reader gate on macOS (8.10 s) and Ubuntu
(8.43 s). Ubuntu also returned EOF during actual native detach. Command-package
tests took 34–50 seconds while the full race suite was running. Those tests build
actual executables recursively, overlapping the package-level runtime gates with
compiler work under the default full-suite scheduling.

The transport failure has a deterministic cause. The host sends a zero-length
Close frame through its `net.Pipe`, then closes the pipe. A buffered reader may
already have fetched all five frame bytes when `ReadFrame` tries to set its
connection deadline. The closed pipe rejects the deadline, so the bridge loses
the valid Close and its caller receives EOF. A controlled connection makes both
the frame-reader regression and the bridge regression fail against the old
source; the bridge reproduces the same downstream EOF seen in hosted CI.

The repair reads complete buffered frames without requesting another connection
deadline. When more header or payload bytes require a connection read, it uses
the existing five-second bound once for that frame. Tests retain malformed and
truncated frame rejection, bound partial headers and payloads, check that the
deadline is not extended, and confirm that absence of a subsequent frame still
returns EOF. This changes framing, not EOF interpretation in `Drive`.

On Darwin arm64, Go 1.26.6, Apple M4 Max, the full transport race suite passed
three runs (21.522 s) and vet passed. The complete buffered-frame and bridge
regressions passed ten runs. Twenty baseline actual native detach repetitions
passed locally (27.113 s), so the original stochastic native failure was not
independently reproduced through the actual process path here.

The screen implementation and its eight-second test remain byte-identical to
the base. The race-enabled 2 MiB benchmark took 2.967 s with 2,123,611 allocations.
Three unchanged slow-reader tests passed in 6.27, 5.65 and 6.74 seconds while
twenty transport iterations repeatedly built the native helper. This supports
compiler/package contention as the performance explanation, but it does not
establish that explanation on a hosted runner. The next bounded comparison is
`go test -race -p 1 ./...`: every original package, assertion, timeout and race
check remains active, while timed native work receives the runner CPU without
parallel package compilers and nested builds competing with it. The captain owns
that CI decision and must verify the actual hosted result.

No screen optimization, shared manifest change, provider/model activity,
publication, or external billing change occurred. Hosted qualification of the
repair and serialized schedule, actual Linux transport proof, and exact release
artifact/provider qualification remain missing.

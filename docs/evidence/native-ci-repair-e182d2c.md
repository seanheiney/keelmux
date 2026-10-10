# Native CI repair

Source `e182d2c46c035f2cb4ecef4c1ac48a2fe84a8581` repairs the two native failures
observed in GitHub Actions run `38086615176` at source
`5772ad21155ce163dcfe95d07c7031abcde17dbc`. Its native adapter files were identical
to the repair's clean base `5e8d3e93ed942accd7bb9e6eb0ebd26dc4bef2e3`.

The eight-second slow-reader deadline remains unchanged. The baseline ASCII
screen path allocated and segmented each character separately. A race-enabled
2 MiB benchmark on Darwin arm64 took 8.695 seconds and 6,319,033 allocations.
The repair batches printable ASCII only when the control parser is in ground
state; split controls and UTF-8 still go through that parser. The first ASCII
character preserves the preceding Unicode grapheme tail; the final cell and
pending-wrap state are retained for a later combining character. The equivalent
benchmark now takes 3.047 seconds and 2,123,604 allocations. These are single-run
measurements, not universal throughput guarantees.

The Linux lifecycle failure came from the shell fixture. `dash` returns failure
from a `read` interrupted by a trapped SIGINT, ending the old `while read` loop.
The fixture now explicitly retries an interrupted read and still exits on EOF.
It continues to assert the real PTY SIGINT trap and a live exact execution after
reconnect. Product signal behavior was not changed. The unmodified failure was
reproduced in the restricted Linux container before applying the repair.

The repair also retains the actual post-write row of the final glyph, including
scrolling at a vertical margin. A focused test verifies that a combining mark
extends that final cell and cannot appear outside the scroll region.

Validation on the exact repaired source:

| Environment | Native/helper race suite, three runs | Vet | 2 MiB slow-reader test |
| --- | --- | --- | --- |
| Darwin 25.6.0 arm64, Go 1.26.6 | Pass, 19.490 s total | Pass | Pass, 3.66 s single run |
| Linux 6.8.0-100-generic arm64, Go 1.26.6, dash | Pass, 20.736 s total | Pass | Pass, 4.45 s single run |

The Linux run used an exact pinned official Go image, UID/GID 65534, no network,
a read-only root filesystem, all capabilities dropped, and finite source/vendor
archives delivered over stdin into owned executable temporary filesystems. No
host bind mounts, production sockets, credentials, or native model CLIs were
used. The Linux race benchmark took 3.687 seconds for the same 2 MiB workload.

New differential tests compare batches with bytewise input across Unicode
Prepend, decomposed accents, variation selectors, ZWJ emoji, C0/C1 framing,
OSC/DCS/APC content, queries, alternate screens, cursor wrap, origin mode and
vertical scrolling margins. Existing exact-instance, no-replay, raw-key,
resize, detach, query-authority and snapshot tests also passed.

See [native-ci-repair-e182d2c.json](native-ci-repair-e182d2c.json) for source
hashes, image identity, commands, numerical results, and cleanup status. These
local Darwin and actual Linux arm64 results do not substitute for hosted CI on
the integrated commit. Linux amd64 runtime, real provider acceptance on this
repair, and release artifacts remain separate gates.

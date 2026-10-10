# Native terminal correction qualification

Source: `2397355a6ae9cbd0df742f6df0f8fa5788fe6766`, based on `812d6401790e2487e04136330e9d774dad9b3607`. Independent correction review is pending. The JSON receipt records exact hashes, commands, isolation, and cleanup.

The base correction retained the ASCII speed improvement but still misplaced one-/two-character tails after controls that preserve the emulator's delayed wrap. New expected-cell tests fail against the old source. The correction tracks explicit wrap-reset operations, reconciles glyph positions after writing, and preserves a complete Prepend-plus-ASCII cluster using the emulator's public cell API. The conservative batching guard remains.

The 1,620-case expected-cell matrix covers widths 1, 2, and 7; top, bottom scrolling, and origin/scroll margins; nine controls; combining marks, Prepend, and ZWJ; and byte chunks 1, 2, 5, and 128. Separate fixtures check cursor resets, REP, restore, backtab, reverse index, invalid margins, and resize. These tests assert actual cells and cursors rather than using the same bytewise implementation as their only oracle.

| Exact-source check | macOS arm64 | Actual isolated Linux arm64 |
| --- | --- | --- |
| Native/helper race suite, three runs | PASS, 22.248 s | PASS, 24.551 s |
| Native/helper vet | PASS | PASS |
| Race benchmark, 2 MiB ASCII | 3.011 s | 3.979 s |
| Eight-second slow-reader gate | PASS, unchanged | PASS, unchanged |

The slow-reader test file is byte-identical to the initial ASCII batching repair. Before this final correction, the frozen 812d640 base also passed its existing macOS/Linux suite and benchmarks; that limited success did not establish the new short-tail cell requirements.

No hosted CI, amd64/clean-VM, or real-provider proof is claimed here. There were no provider/model starts or model turns. No production state, auth, root/shared manifests, or entrypoints changed. The emulation contract remains tied to its pinned VT version; a two-column glyph cannot render on a one-column screen. The owned container was removed and its absence verified. Immediately before removal, its process inventory contained only init, the fixture keepalive, and the inventory command. Independent source review remains separate and pending.

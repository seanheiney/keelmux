# Pinned VT ASCII allocation improvement

This is the complete 33-file Go module `github.com/charmbracelet/x/vt` at
`v0.0.0-20261008172826-faa4adf95555`, commit
`faa4adf95555331dfb439b716cc83767ecdadeb4`. All upstream tests, module files and
the MIT license are retained. Exactly one expression in `utf8.go` differs:

```diff
- e.handleGrapheme(string(e.grapheme[0]), 1)
+ e.handleGrapheme(string(e.grapheme[:1]), 1)
```

The enclosing condition requires `len(e.grapheme) == 1` and
`printableASCII(rune(e.grapheme[0]))`. That helper accepts only bytes `0x20`
through `0x7e`. For those bytes, integer-to-string Unicode encoding and
byte-slice-to-string conversion produce exactly the same one-byte string.
The conversions are not equivalent for arbitrary bytes above ASCII; the guard
is unchanged and mandatory. The resulting string remains immutable when the
grapheme buffer is immediately reused. Width, charset mapping, cursor, wrap,
styles, hyperlinks, modes, callbacks and Unicode paths are unchanged.

In Go 1.26.6 the one-byte slice conversion uses immutable static byte storage,
avoiding the escaping integer-conversion allocation. The language-level
equivalence does not depend on that runtime optimization. Primary sources are
the [pinned VT source](https://github.com/charmbracelet/x/blob/faa4adf95555331dfb439b716cc83767ecdadeb4/vt/utf8.go),
the [Go conversion rules](https://go.dev/ref/spec#Conversions_to_and_from_a_string_type)
and the [measured Go runtime](https://github.com/golang/go/blob/go1.26.6/src/runtime/string.go).

The bounded hosted investigation measured 99.983% fewer allocations and a 3.69%
median race-benchmark improvement across three sequential 2 MiB samples on one
Linux amd64 runner. This is an allocation improvement. It does not establish
the cause of the earlier hosted eight-second failures, establish a latency
guarantee, or qualify every terminal input. The exact runs, equivalence trace,
failed observations and limits are recorded in
[the scoped VT evidence document](../../docs/evidence/vt-ascii-allocation-2026-10-10.md).

`PROVENANCE.json` records the full commit, Go checksums, ZIP digest and every
upstream file hash. `upstream/module.zip` is the original official Go module
ZIP, including its license. `upstream/utf8.go.orig` preserves the original
expression. `upstream/module.info` preserves the official module origin receipt
binding its pseudo-version to the full Git commit. These artifacts are
verification material and are not additional
Go packages. The verifier derives the permitted active files directly from the
hard-pinned ZIP; editing the inventory metadata cannot authorize another patch.

Run from the repository root, without network access:

```sh
python3 third_party/vt/verify.py
python3 -B third_party/vt/verify_test.py -v
```

An independent official cached ZIP can also be checked with
`verify.py --upstream-zip /path/to/official.zip`. The verifier refuses missing or
extra entries, source/test/license/module drift, a changed guard or second patch,
sidecar/ZIP/provenance drift, symlinks, hardlinks, special files and executable
file modes. It does not alter the source. Its trust anchor is reviewed source
and Git, not a security boundary against a local user who can replace both the
verifier and its anchors.

The copied module's unchanged upstream tests run with:

```sh
cd third_party/vt
go test -race -mod=readonly -count=1 ./...
go vet -mod=readonly ./...
```

Captain integration owns the root module replacement and CI wiring. No other
dependency, parser, native PTY deadline or throughput assertion changes belong
to this contribution.

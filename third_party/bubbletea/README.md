# Bubble Tea v1.3.10: bounded terminal initialization patch

Keelmux retains the official Bubble Tea v1.3.10 implementation and upstream unit
tests, with one file omitted: `tea_init.go`. That file queries the terminal's
background color through Lip Gloss during Go package initialization. Because
Keelmux's command executable also imports its switcher, the query ran before
`main` for ordinary commands, including native terminal driving. A delayed
terminal response could then reach the attached provider as input.

Removing the eager query preserves the existing renderer, `ExecProcess`,
cancellation, panic recovery and terminal restoration fixes. There are no edits
to copied upstream Go files, dependency declarations or tests. Keelmux's switcher
uses its own finite ANSI palette and does not need adaptive Lip Gloss background
detection. This patch does not filter user input or change terminal/environment
settings, provider authentication, application permissions or native commands.

The upstream workaround prevented applications that perform adaptive Lip Gloss
color detection after Bubble Tea acquires input from waiting for a terminal
response. Such detection must not be added implicitly to Keelmux. If it is ever
needed, its terminal ownership and response lifecycle require explicit design
and tests. Upstream removed this workaround on its v2 development path; a future
v2 migration should replace this patch after independent UI and attach/recovery
qualification.

`PROVENANCE.json` pins the official repository, tag, commit, Go module checksums,
SHA-256 for every copied file, and the original omitted file. Every excluded
upstream path is listed. The original upstream README is retained as
`UPSTREAM_README.md`, and the upstream MIT license remains in `LICENSE`.

Run the finite source check from the repository root:

```sh
python3 third_party/bubbletea/verify.py
```

Compare the retained bytes against a separately downloaded official Go module:

```sh
python3 third_party/bubbletea/verify.py \
  --upstream "$(go env GOMODCACHE)/github.com/charmbracelet/bubbletea@v1.3.10"
```

The verifier rejects extra/missing source files, symbolic links and any changed
copied bytes, and requires `tea_init.go` to remain absent. The captain owns the
root `go.mod` replacement and an actual CLI PTY regression that verifies terminal
queries are absent before ordinary commands start. Source verification alone
does not establish terminal behavior or provider compatibility.

To update: select an exact official upstream version, review its complete
initialization and terminal ownership behavior, preserve its license, regenerate
the finite provenance map, then run upstream tests, Keelmux UI/recovery tests and
the actual command PTY regression. Do not silently edit copied files or replace
this dependency with an unrecorded fork.

Primary sources:

- [Pinned official v1.3.10](https://github.com/charmbracelet/bubbletea/tree/9edf69c677c7353eca5fae6d3ea3986af39717b7)
- [Original eager initialization](https://github.com/charmbracelet/bubbletea/blob/v1.3.10/tea_init.go)
- [Introduction of the workaround](https://github.com/charmbracelet/bubbletea/commit/d6458e03f27245a597a30234a532ef345af31d36)
- [Upstream v2 removal](https://github.com/charmbracelet/bubbletea/commit/ee16c6e8bbdc62b65d51d9c71c602893860318f9)

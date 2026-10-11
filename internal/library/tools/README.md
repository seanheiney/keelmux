# Explicit MCP tool health

`Check(context.Context, library.ResolvedTool, Options) Receipt` observes one
selected tool. The caller must obtain the declaration through a freshly verified
library resolution and pin. This package validates its shape; it does not prove
that caller-supplied metadata belongs to an imported package.

```go
receipt := tools.Check(ctx, selected, tools.Options{}) // no execution or writes
receipt = tools.Check(ctx, selected, tools.Options{
    Health: true,
    WorkingDirectory: authorizedWorkspace,
    Timeout: 5 * time.Second,
})
```

`LookupEnv`, when supplied, replaces `os.LookupEnv`. Ordinary availability checks
only executable metadata and declared environment presence. They do not run a
version command, create a directory, contact an endpoint, or initialize a server.
`Health` explicitly starts the selected argv directly, without an implicit shell.
The selected program remains trusted executable code; private environment paths
are not an operating-system sandbox. HTTPS is unsupported and makes no request.

## Receipt contract

The JSON fields are `package_id`, `package_digest`, `tool_id`, `transport`,
`observed_at`, `declared`, `availability`, `health`, `started`, `initialized`,
`tools_listed`, `tool_count`, `protocol_version`, `invoked`, `cleanup`, and `code`.
Optional identity/version fields are omitted when unavailable.

| Field | Meaning |
| --- | --- |
| `declared` | The supplied declaration passed bounded structural validation. |
| `availability` | `available`, `unavailable`, or `unknown`; metadata/environment observation only. |
| `health` | `not_checked`, `healthy`, `unhealthy`, or `unsupported`. Healthy means initialization and a complete bounded tool listing succeeded. |
| `started` | An owned child was started during this check. |
| `initialized` | The server returned an accepted version and capability shape. |
| `tools_listed` / `tool_count` | A complete bounded listing was observed. Names, descriptions and schemas are not returned. |
| `invoked` | Always false. This package contains no `tools/call` request. |
| `cleanup` | `not_needed`, `complete`, or `failed`. Cleanup failure forces unhealthy. |
| `code` | A finite local reason, never a native error or captured stderr. |

No command, working-directory path, URL, environment name/value, native error,
instructions, server identity string or tool description enters the receipt.
Invalid declarations return no unvalidated identifiers. `healthy` does not prove
executable provenance, native harness loading, authorization, account identity,
quota, compression accuracy, token savings, or successful tool invocation.

## Environment and lifecycle

Only PATH and explicitly selected EnvRefs enter the child, alongside fixed locale
settings. Each health check creates fresh mode-0700 HOME, cache, configuration,
data, state and temporary directories. Reserved HOME/USERPROFILE, TMPDIR/TMP/TEMP,
XDG_CACHE_HOME/XDG_CONFIG_HOME/XDG_DATA_HOME/XDG_STATE_HOME and APPDATA/LOCALAPPDATA
EnvRefs are rejected before lookup. Explicit references may contain credentials;
values remain local and never enter receipts or captured stderr. Stderr goes to
the null device. No ambient credential environment is copied.

Health requires an absolute existing working directory. Its default deadline is
five seconds, with a maximum of fifteen. Deadline/cancellation closes owned pipe
endpoints. Shutdown closes stdin, waits 25ms, signals the fresh owned process
group with TERM, waits 100ms, then KILL. The direct child remains unreaped until
all signals finish, preventing process-group ID reuse during signaling. Kernel
group state is checked for up to 250ms; child reaping has a separate one-second
wait. Private-directory removal is also checked. These are ordinary OS operations,
not hard realtime guarantees against a wedged kernel or filesystem.

Darwin and Linux are supported by source. Other platforms return unsupported.
No saved PID is ever used to kill a process. Descendants that deliberately leave
the owned group, arbitrary same-user filesystem access, and executable replacement
between metadata inspection and execution are outside this contract.

## Protocol scope and bounds

The client sends initialize, notifications/initialized, and tools/list only. It
advertises an empty client capability object. The requested version is 2025-11-25;
the observed response may negotiate 2024-11-05, 2025-03-26, 2025-06-18 or
2025-11-25. The receipt records that actual version. Their published lifecycle and
paginated tool-list subset is shared. Unknown versions are refused.

Each input frame is at most 256KiB, total input at most 1MiB, total frames at most
32, pagination at most four pages, listed tools at most 256, cursor at most 1024
bytes, and JSON nesting at most 32. Duplicate keys, invalid UTF-8, unexpected IDs,
unknown result fields and unqualified capability objects fail closed. Only
bounded logging notifications are discarded; server-initiated sampling,
elicitation, roots and other requests are refused. Input schemas receive bounded
structural validation, not full JSON Schema semantic validation or evaluation.
No links, resources, instructions or callbacks from server output are followed.

Primary protocol contracts:

- [Lifecycle and version negotiation](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle)
- [Stdio transport](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)
- [2024-11-05 tool listing](https://modelcontextprotocol.io/specification/2024-11-05/server/tools)
- [2025-03-26 tool listing](https://modelcontextprotocol.io/specification/2025-03-26/server/tools)
- [2025-06-18 tool listing](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)

## Qualification on 2026-10-10

Base: `2e09b0ed11678c9248ab0168da1505cc9a9bafc5`. macOS 26.7.1 build 25G309,
arm64; Go 1.26.6. Final `go test -race -json ./internal/library/tools` passed 50
named tests/subtests in 6.849s. `go vet ./internal/library/tools` passed. Linux
arm64 and amd64 test binaries cross-compiled; Linux execution is not claimed.
Tests cover ordinary no-execution, private environment and literal argv,
reserved-key rejection before lookup, malformed/oversized/budget-exhausting
responses, policy and ID rejection, cancellation, pagination, negotiated versions,
and inherited descendants that ignore TERM.

An initial Darwin cleanup implementation failed when killpg returned EPERM for a
group containing only exited zombies. The correction verifies the still-anchored
group's kernel state before claiming cleanup; it does not accept EPERM for a live
group. The initial failure and correction are retained in private working evidence.

The actual optional Headroom check used an isolated wheel-only installation of
`headroom-ai==0.40.0`, Python 3.14.7, and MCP SDK 1.30.0. The selected command was
`headroom mcp serve`, wrapped in a Darwin deny-network policy for this proof only.
Explicit offline/update-check environment references, private HOME/cache/temp,
and an empty owned working directory were used. Final health passed in 1.025s:
protocol 2025-11-25, three tools listed, zero invocations/model turns, cleanup
complete. The first successful check took 2.895s and is retained separately.

The wheel's 579 installed files (excluding installation-rewritten RECORD) match
the public wheel. The wheel differs from the plugin declaration's source revision
`976aa714ef3e19174dc0cb586cee7b20d064e4ef`; these bindings are deliberately separate.
The exact wheel SHA-256 is
`f7b0186ad5e76d5c8f75e5de6c57e001637258ead75481575ccca4d57557f5fb`.
See [PyPI 0.40.0 metadata](https://pypi.org/pypi/headroom-ai/0.40.0/json) and the
[declared source](https://github.com/headroomlabs-ai/headroom/tree/976aa714ef3e19174dc0cb586cee7b20d064e4ef).

This is standalone Check API proof. The fixture's declaration hashes are not
Registry package digests; package import/profile wiring and native loading are
separate integration gates. No wrap, proxy, install-into-agent command, global
configuration change, provider call, or compression/retrieval tool call was made.
The compact receipt is in `headroom-health-evidence.json`; detailed dependency
pins, original receipts and install/source comparisons remain private working
evidence. No dependencies were added to Keelmux's Go modules.

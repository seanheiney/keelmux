# Internal supervisor stdio

This backend implements the typed `primarystdio.Supervisor` contract for an
internal Codex 0.159.0 inspection milestone. It does not expose chat submission,
approval answers, arbitrary JSON, native input, login, model selection or product
activation. Native stdout and stderr remain private to the detached helper.

`LaunchStdio` binds one existing Host/Session/Execution generation and start
operation to a new primary/channel incarnation. It launches a fixed absolute
executable with only `app-server`, three distinct pipes, and an explicit clean
environment. Both helper and child exclude ambient credentials, proxy, loader
and broker capability variables. The selected private HOME/Codex/XDG/temp
directories must already exist, be canonical, owned and mode 0700. The existing
workspace must be canonical and owned. Directory inodes are pinned.

The helper consumes its durable launch intent before starting the child. Each
bootstrap/query attempt is persisted before writing bytes. Bootstrap consists
only of initialize, initialized and exact-ID resume, and cannot rearm after any
attempt. Subsequent inspection is the fixed loaded/read/queue/background set.
A disconnected caller cancels unsent steps; neither daemon reconnection nor a
lost reply authorizes another bootstrap or child launch.

The private sidecar retains every accepted receive frame, including results,
notifications and reverse requests, with typed IDs, hashes and a chained cursor.
The common store receives bounded metadata batches and acknowledges their exact
hash/frontier; an acknowledgement cannot clear a callback or other blocker.
Raw native bytes are not returned through IPC. Malformed/unknown/correlated
frames, partial EOF, overflow, identity loss and storage replacement are explicit
sticky blockers. Bounds reserve space for an overflow sentinel; evidence after
a broken gap is never presented as complete. Native stderr has a separate
bounded tail and truncation flag and is never parsed as protocol.

Only disabled remote-control status metadata is nonblocking. Connecting,
connected and errored statuses block publication and stop. A consumed response
does not rearm its original request ID; a second response is a correlation fault.

Publication uses a connection-scoped guard with a two-second ceiling. It pins
the exact imported cursor and snapshot, and rechecks current executable,
directory and spool identity before allowing the local callback and before
acknowledging release. Receive registration is serialized through that callback.
This is a cooperating actor ordering guarantee, not OS sandbox isolation or a
promise that future native events cannot invalidate readiness. Lost release,
deadline or storage failure returns uncertainty even if the callback committed.
The caller must handle that uncertainty and fresh subsequent blockers.

`RetainedStdio` never sends native bytes. After a strictly matched terminal
receipt it reads retained evidence directly and sets process/image/readiness
flags false. Stop/death recovery can import that final frontier before confirming
death. A missing/corrupt receipt or sidecar is not repaired. Generic PTY attach,
input and stop operations cannot control stdio channels. Typed stop requires a
fresh, exact, imported quiescent snapshot and persists its signal attempt before
using the retained child process handle; retries only reconcile that attempt.

`CheckStdioReady` is a side-effect-free known-platform preflight. Installed
Darwin currently refuses because running-image qualification is unavailable;
this happens before launch intent/helper/child creation. Linux verifies the
actual `/proc/<pid>/exe` bytes together with boot and before/after process-start
identity. Compilation is not Linux runtime qualification. Binary/schema pins
are necessary identity evidence, not provider acceptance or model-turn proof.

Tests use a real detached helper and three-pipe child implemented entirely by
the compiled Go test executable. Its package-private test verifier is not in
the installed binary and cannot be selected by environment, options or IPC.
These fixtures qualify engine ordering, clean environment, typed protocol,
durability and bounded failure behavior. They do not qualify any real provider,
credentials, model calls or installed Linux runtime. Existing PTY/process paths
and their original terminal/deadline assertions are retained.

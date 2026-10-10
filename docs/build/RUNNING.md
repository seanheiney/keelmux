# Running the current build

Build with `make build`; `make check` runs race tests and vet. The binaries are `bin/bp`, `bin/bpd` and `bin/bpg`. The current runtime needs tmux and the chosen native CLI installed. Provider discovery reports evidence without upgrading programs or changing their login state.

## Local native terminal

```sh
./bin/bp providers
./bin/bp doctor
./bin/bp run --workspace /path/to/project codex
./bin/bp run --workspace /path/to/project claude
./bin/bp
```

The native CLI retains its own authentication, workspace trust and conversation behavior. A launch is not an authenticated model-turn receipt. Arrows or `j`/`k` select a session; Enter opens its native terminal and `v` opens a read-only viewer. **Ctrl-G d** detaches to the switcher; **Ctrl-G Ctrl-G** sends the literal prefix. A single managed writer owns input. A local draft edited with `p` stays local.

Use `bp --state /absolute/private/directory` for isolated state. The daemon starts automatically for local commands. Its directory and files are private; keep the path short enough for Unix sockets. `bp list`, `bp inspect SESSION` and `bp reconcile` expose observed state. Retry an uncertain action with its original operation and execution IDs; creating a new operation does not reconcile the old effect.

## Owned native skills

Setup previews by default:

```sh
./bin/bp setup --scope project --root /path/to/project --providers codex,claude
./bin/bp setup --scope project --root /path/to/project --providers codex,claude --apply
./bin/bp skills remove --scope project --root /path/to/project --providers codex,claude
```

User scope uses an explicitly selected root or the user's home; project scope requires a root. Install and remove only the chosen providers. The pinned `backplane` bundle requires explicit native invocation and calls the inherited exact-session helper. Setup receipts prove file installation, not vendor discovery or instruction loading. Existing files and ownership mismatches are preserved. Removal moves the intact owned bundle into a private `.backplane/removals` archive, with a durable receipt; there is no automatic purge. Open editor descriptors can still refer to those retained bytes.

Codex discovery has been checked through the native skills catalogue, and Claude displayed `/backplane` in its native selector. No skill invocation or model turn was submitted in those checks. `/rc` remains Claude's native command; the backplane bundle does not replace it.

## Private entry experiment

`bpg` routes permitted native terminal attachments to explicit daemon homes. It currently binds one principal through an owned private configuration and Unix socket. The companion `bp` must be on the client machine. It has no public listener, automatic enrollment, or independent remote-host acceptance. The same-computer two-home fixture is a transport test. Consult [STATUS](STATUS.md) before treating this experiment as accepted.

Prepare a private JSON configuration with exact IDs from the execution homes:

```json
{
  "principal": "local",
  "peers": [
    {"host_id": "host_FROM_HOME_A", "socket": "/absolute/home-a/control.sock"},
    {"host_id": "host_FROM_HOME_B", "socket": "/absolute/home-b/control.sock"}
  ],
  "grants": [
    {"host_id": "host_FROM_HOME_A", "sessions": ["session_ALLOWED_A"]},
    {"host_id": "host_FROM_HOME_B", "sessions": ["session_ALLOWED_B"]}
  ]
}
```

Replace the example IDs and paths; an empty session grant grants no inventory or attachment. The configuration must be a regular file owned by the running user with mode `0600`, at most 64 KiB. Run the entry in its own terminal:

```sh
chmod 600 /absolute/entry.json
./bin/bpg --state /absolute/entry-state --config /absolute/entry.json \
  --companion /absolute/path/to/bin/bp
./bin/bp --state /absolute/client-state --entry /absolute/entry-state/control.sock
```

The entry hides home runtime locators and exposes native attachment capabilities. Launch, stop, link and reconciliation management remain at the execution home. An unreachable home appears separately from an unreachable entry. Saved acquisition identity remains necessary for exact release/recovery after lost acknowledgements. Configuration changes apply on entry restart; dynamic grant-revocation behavior is tested through the gateway API.

An independently authenticated SSH-forwarded private peer socket is a possible future transport; enrollment, tunnel recovery and an actual second machine have not been proved. Native account/model attribution, semantic messages, continuation, and thread migration are separate unfinished features.

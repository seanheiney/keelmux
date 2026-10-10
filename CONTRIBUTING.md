# Contributing

Keelmux is a terminal-first backplane for real native agent CLIs. Contributions should make launch, switching, input ownership or recovery easier to trust and use. Read [AGENTS.md](AGENTS.md), the [architecture guide](docs/user/architecture.md), [product specification](docs/product/PRODUCT_SPEC.md) and [build plan](docs/build/BUILD_PLAN.md) before changing behavior.

## Develop locally

Install the Go version declared in `go.mod`. Native terminal journeys also require tmux and the provider CLI. From a source checkout:

```sh
make build
make check
```

`make check` runs Go race tests and `go vet`. Current pre-rename source produces `bin/bp`, `bin/bpd` and `bin/bpg`; release integration supplies the Keelmux command names and compatible aliases. Keep companion binaries together. Use an explicit temporary private state path for experiments so development work cannot accidentally address your daily home.

Do not run native login, model turns, provider upgrades, destructive tool actions or external publication as part of an implicit test. Native prompts and approvals need explicit test scope. Fake providers and loopback model backends belong only in fixtures; product defaults must launch real supported CLIs.

## Keep changes reviewable

Describe the concrete trigger, the behavior before and after, and the validation appropriate to the change. Keep unrelated refactors separate. Shared domain, protocol, store, runtime and provider interfaces need one coordinating owner before parallel workers modify them. Delegated work should name its writable paths and consumed interfaces.

Preserve separate session, execution, native-thread, process, terminal and host identities. A daemon restart, provider exit and host reboot are different recovery events. Keep failed, completed and uncertain operations distinct. Never turn a timeout into an automatic retry of input or a guessed process takeover.

Use typed provider evidence with explicit `unknown` and provenance. Do not infer account or model from display text, parse transcripts as execution authority, or promote experimental Codex broker tests into general product capabilities. Follow the [Codex broker contract](docs/research/CODEX_BROKER_CONTRACT.md) for that experimental boundary.

## Validation and documentation

Run checks relevant to the changed paths. For lifecycle work, add meaningful failure tests for the affected ownership and recovery transitions. A passing unit suite is distinct from native runtime evidence, and cross-compilation is distinct from running on that OS.

Record runtime evidence with the exact source commit, OS/architecture, tmux and native CLI versions, actual actions, assertions and remaining gaps. Use synthetic data and explicit test-owned processes. Never stop an unrelated daemon or attach to an arbitrary provider socket.

Update the user guide and changelog when a command or behavior changes. Keep planned features labeled and release claims tied to the release's actual source. Documentation-only changes usually need link and command-reference checks rather than new implementation-mirroring tests.

The checked-in [LICENSE](LICENSE) governs contributions. Do not change licensing or claim compliance through incidental documentation edits.

# Reusable capability library and provider adapters

Status: implementation contract, 2026-10-10. This expands the owner's reusable plugin/rule/skill/tool request. It does not qualify a native model call or extend published v0.2.1 support.

A library pack is a declarative version-1 manifest, pinned files and source metadata. A profile selects exact pack digests and individually enables skills, rules or tools. The same pack can be mapped to several native harnesses. Import and selection never execute scripts or tools, change logins, start a model or mutate a provider's unrelated configuration.

## Stable core boundary

`internal/library/types.go` publishes the shared types and `Registry` interface. The implementation exposes `Open(stateDir string) (Registry, error)` without creating state. Read and absent resolution are observations. Mutation may create a private library under the explicitly selected state, but cannot start a daemon or touch native account stores.

- Pack manifests use `schema: 1`, a validated stable `id`, a version, optional declared source/revision, an exact `files` SHA-256 map, skills, rules and MCP tool declarations. Declared source is provenance metadata; a local import does not verify an upstream publisher.
- Skills name a bundle-relative directory; rules name a bundle-relative Markdown file. The manifest excludes itself from `files`. All other files must match the manifest: no symlinks, traversal, special files, undeclared files or unbounded recursive import. Limit 512 files, 1 MiB per file, 16 MiB total, and a bounded strict manifest. Installation copies bytes without running them.
- Tools use stdio argv or an explicit HTTPS endpoint. There is no shell interpolation. Environment references contain variable names only; credentials and their values are never exported. Arbitrary native permission increases are not portable rules.
- Every package revision has an immutable content digest and owned directory. Multiple revisions coexist. Importing a new revision does not change profile selections. A profile names the exact selected digest, with `kind/id` component overrides (`skill`, `rule`, `tool`). Package enablement and component enablement are separate.
- Profile IDs and package/component IDs are bounded, clean portable identifiers. Profile names are bounded UTF-8 without terminal controls. Unknown fields and duplicate JSON keys are refused. Corrupt or future state is preserved rather than overwritten.
- Interactive and implicitly pinned CLI selection edits use `GuardedSelections`: expected profile revision and component pin are checked under the retained writer lock. Stale controls refuse and require refresh rather than editing another client's newer selection.
- Reads are anchored to existing owned private files. Mutations serialize through an owned private lock and recoverable atomic writes. Refuse symlinks, special files, permissive/foreign ownership and content drift before effects. Same selections are idempotent and keep revisions stable.
- `Resolve` verifies every selected pin and asset, returns canonical deterministic ordering/digest, and reports loaded state as unknown. It cannot certify the running process or effective permissions. `PackageRoot` verifies the exact pin before exposing its local root to a mapper.
- Removal selects one exact pin, disables matching desired selections and archives its intact owned directory. It preserves unrelated versions and files; edited/unowned content prevents removal. Running native sessions are not killed or silently reconfigured. There is no purge.

The initial default profile is `default`. Global profile selection and explicit workspace/session bindings sit above the registry; a launch records the resolved profile revision and digest. A change affects the next explicit apply/launch. Existing processes report reload pending or unknown until a supported native observation proves otherwise.

## Native mapping and application

The mapper consumes a verified resolution and produces a provider-specific preview, including every supported file, CLI argument or native configuration change, collisions, environment references, external prerequisites and unmapped fields. Application revalidates the preview and owned content, stores its original/snapshot identity, and preserves native scope precedence. Imported declarations do not bypass native tool approvals.

Portable skill assets follow the Agent Skills directory format. Rules remain Markdown instructions and do not represent enforced policy. MCP tool declarations map to the harness's supported configuration. Discovery, applied files, loaded configuration, healthy tools and observed invocation remain different states.

Gemini and Grok are native terminal adapters. OpenCode is the first additional portable terminal harness for explicit OpenRouter model selection; its native login and provider registry remain authoritative. The larger Pi continuity study remains separate. A requested model identifier does not prove the accepted provider/model, subscription or quota.

[Gemini skills](https://geminicli.com/docs/cli/skills/) describes its interoperable skill directories; [OpenCode providers](https://opencode.ai/docs/providers/) describes explicit OpenRouter routing. Exact installed versions and native flags require local verification before adapter implementation.

## Optional Headroom integration

Headroom is an optional pack, selected like any other tool. Its upstream [repository](https://github.com/headroomlabs-ai/headroom) currently offers an MCP entry point and proxy integrations. The initial tool declaration uses the canonical upstream MCP contract and an explicitly installed executable. A proxy is a separate opt-in capability, with an exact upstream endpoint and owned local process; it must never silently send one provider's key to another upstream.

Do not use Headroom's durable automatic wrapper as a hidden implementation step. Native setup writes, additional bundled tools and shared runtime settings must be visible in a preview. On/off restores only owned unchanged assets; live-session effect and required reload remain explicit. Savings are observations with source/scope, not guaranteed percentages for Keelmux.

Tack remains a potential source/deployment adapter behind this narrow registry interface, as requested by the governing product specification. Evaluate its actual versioned contract before adopting it; it is not a mandatory runtime dependency.

## Development ownership

| Lane | Allowed paths | Boundary |
| --- | --- | --- |
| Registry | `internal/library/` excluding captain-owned `types.go`; focused registry evidence | Import, pins, profiles, selection, resolution, ownership and removal; no native application |
| Native provider adapters | `internal/adapters/providers/`; provider conformance evidence | Gemini/Grok/OpenCode discovery and safe interactive argv; no model turns, login writes or false native qualification |
| Portable application | new `internal/library/native/`; focused mapping fixtures/evidence | Rules/skills/MCP preview and ownership-aware provider application against the shared resolution contract |
| Tool runtime/Headroom | new `internal/library/tools/`, `plugins/headroom/`; scoped fixtures/evidence | Explicit optional tool health/process contracts and on/off recovery; no global wrapper/configuration changes |
| Product interface/docs | captain owns shared CLI/TUI/config entry points and manifests | Explicit commands and contextual controls, launch snapshots, guides, screenshots and release wiring |

The captain reviews shared interfaces and each contribution. Workers use separate branches and do not modify the frozen v0.3.0 candidate. Model-turn, customer-message, reboot/resume and independent-server claims require their own evidence and authorization.

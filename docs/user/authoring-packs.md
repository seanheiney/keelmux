# Author a reusable capability pack

**Development source only:** this sample completed 59 library-only compiled CLI operations at Keelmux `c9480ae06efb79a3db49cf7268496415dd9d5bac`. The public v0.2.1 installer does not contain these commands. The checks verify saved selections and owned workspace files; native loading and tool invocation remain unknown.

A pack keeps one immutable set of skills, Markdown rules and MCP declarations. Profiles select an exact pack digest and remember component switches. Importing or selecting a pack starts no provider or tool. Explicit application writes selected workspace files; it still does not prove that a native process loaded them.

## Start with this sample

[The complete sample](../../examples/packs/careful-review/keelmux-plugin.json) has this inventory:

```text
examples/packs/careful-review/
  keelmux-plugin.json
  rules/preserve-files.md
  skills/bounded-review/SKILL.md
  skills/bounded-review/checklist.md
```

The rule asks a reviewer to preserve existing work and qualify claims. The skill gives a bounded review procedure and links its text checklist. Rules and skills are instructions, not permission enforcement. There are no bundled executables, scripts or install hooks.

The optional tool declaration uses `command: ["headroom", "mcp", "serve"]`, the entry point recorded by the source's Headroom 0.40.0 preset. Headroom must be installed separately and available to the native environment you choose. The pack version/digest pins the declaration, not the installed executable. `env_refs: ["LANG"]` demonstrates a noncredential environment-name reference: it supplies no value and introduces no server-specific setting. Ensure that name exists in the selected native environment if you keep the declaration enabled. Keelmux does not invoke the command during import, selection, preview or application. No automatic Headroom wrapper or token-savings claim is made.

## Manifest and asset constraints

Use exact lower-case JSON field names. Schema 1 supports `schema`, `id`, `version`, optional `description`/`source`/`revision`, `files`, and optional `skills`/`rules`/`tools`. Do not add permission, installation or auto-start fields.

| Field | Authoring rule |
| --- | --- |
| Package/component/profile IDs | 1–64 characters; `[a-z0-9][a-z0-9._-]*` |
| Version | Nonempty text, at most 128 UTF-8 bytes; an author label, not an executable version |
| `files` | Exact lower-case 64-character SHA256 for every bundled file except the manifest itself |
| Skill | Directory containing declared `SKILL.md`; every companion is also declared in `files` |
| Rule | Declared `.md` file containing UTF-8 text without NUL |
| Stdio tool | `transport: "stdio"` and a nonempty structured `command` array; no implicit shell or `url` |
| Environment references | Names only: `[A-Za-z_][A-Za-z0-9_]*`, at most 128 characters; no `NAME=value` or credentials |

Paths are clean bundle-relative paths, at most 512 bytes and 16 components. No absolute paths, `..` traversal, backslashes, symlinks, hard links, special files, undeclared files or empty extra directories. Source files/directories must be owned and not writable by another user. Keep editor backups and `.git` outside the import directory, or import will treat them as undeclared inventory.

Bounds: 256 KiB manifest, 512 source files, 512 declared components, 1 MiB per asset and 16 MiB total asset bytes. Use ordinary readable text files; stored pack assets receive private non-executable modes. The library does not turn a bundled script into an installed program.

For native portability, skill IDs and frontmatter names use the narrower `[a-z0-9]+(-[a-z0-9]+)*` shape. Start `SKILL.md` with LF-delimited frontmatter:

```markdown
---
name: bounded-review
description: Review a supplied change against its stated scope and report source-linked findings.
---
```

The name must equal the skill asset ID. Description is one line, 1–1024 bytes. Optional single-line `license` and `compatibility` are supported. Unknown, structured or multiline native frontmatter—including permission metadata—is reported as unmapped. Companion paths stay byte-identical; keep links relative to the skill directory.

After editing an asset, recompute its raw-byte SHA256 and update `files`. On macOS, `shasum -a256 rules/preserve-files.md` prints that hash; on Linux, use `sha256sum`. Changing line endings changes the hash. The library's returned package `digest` is the normalized manifest identity, incorporating those file hashes; it is not the hash of the pretty-printed manifest file. Do not guess it—retain the import result.

HTTPS declarations are a separate schema option; they prohibit embedded login, query and fragment values. Environment names do not define an HTTPS authentication header, so such authentication mapping is unsupported. This sample uses local stdio.

## Import and choose a named profile

Use the same local state directory for every command. Add global `--state /path/to/private-state` before `plugins`, `profiles` or `tools` if you do not want the default state. All examples below use that same selection.

```sh
keelmux plugins import /path/to/keelmux/examples/packs/careful-review
keelmux profiles create --name 'Careful review' review
keelmux plugins on --profile review --digest PACK_DIGEST careful-review
keelmux profiles show review
keelmux tools list --profile review
```

Replace `PACK_DIGEST` with the exact 64-character `digest` from import. The virtual `default` profile is separate; creating `review` does not copy default's selections. Unspecified `--digest` retains an already selected pin; a previously unselected pack with multiple revisions requires an explicit digest.

Direct component switches preserve the pin and other preferences:

```sh
keelmux tools off --profile review careful-review/headroom
keelmux tools on --profile review careful-review/headroom
keelmux plugins off --profile review --component skill/bounded-review careful-review
keelmux plugins on --profile review --component skill/bounded-review careful-review
```

Tool switches neither enable a disabled pack nor start its server. Turning the whole pack off preserves its selected digest/component preferences. The TUI library's **p** picker chooses a saved profile to inspect/edit; launch still chooses a profile explicitly.

## Map the same pinned pack to four native harnesses

The inspected mapping contracts are Codex 0.159.0, Claude Code 2.1.296, Gemini CLI 0.52.0 and OpenCode 1.18.21. They establish the file/argv mapping subset, not current native loading. Use separate existing owned workspaces for this recipe so shared skill aliases cannot collide.

| Harness | Skill directory | Rule mapping | Stdio MCP mapping |
| --- | --- | --- | --- |
| `codex` | `.agents/skills/bounded-review` | Owned `AGENTS.md` | Owned `.codex/config.toml`; environment names |
| `claude` | `.claude/skills/bounded-review` | Namespaced `.claude/rules` file | Owned application MCP file, selected by receipt-bound supplemental `--mcp-config` |
| `gemini` | `.gemini/skills/bounded-review` | Owned `GEMINI.md` | Owned `.gemini/settings.json`; name placeholders |
| `opencode` | `.opencode/skills/bounded-review` | Owned instruction file selected by `opencode.json` | Owned `opencode.json` local MCP/environment placeholders |

Start with one workspace/harness:

```sh
keelmux plugins preview --profile review --harness codex --workspace /path/to/codex-project --application review-codex
keelmux plugins apply --profile review --harness codex --workspace /path/to/codex-project --application review-codex --expect-plan PLAN_DIGEST
keelmux plugins status --harness codex --workspace /path/to/codex-project --application review-codex
```

Replace `PLAN_DIGEST` with that preview's exact `digest`. Read `files`, `launch_args`, `collisions`, `unmapped`, `loaded` and `reload` first. Proceed only after resolving collisions/unmapped components. Preview creates no state; apply regenerates the plan and refuses stale source/workspace identity. Apply writes files only and never installs or starts Headroom or a native harness.

Repeat this three-command sequence with harness/workspace/application tuples `claude`/`/path/to/claude-project`/`review-claude`, `gemini`/`/path/to/gemini-project`/`review-gemini`, and `opencode`/`/path/to/opencode-project`/`review-opencode`. Use each tuple's fresh plan digest. The selected package pin is identical; generated files and application receipts differ by harness/workspace. An application ID belongs to one exact harness/workspace pair; keep it consistent for preview, apply, status, launch and clear.

A later explicitly requested native session uses the matching current application:

```sh
keelmux run --profile review --workspace /path/to/codex-project --application review-codex codex
```

Use the equivalent tuple/provider for the other harnesses. These are real native launches, not dry runs; do not include them in a declaration-only verification. Native credentials, trust and tool permissions remain native. Launch refuses a stale, edited or incompletely mapped application rather than applying it silently. Loaded state remains unknown until independently observed. Existing native sessions need their supported reload or a new session; saving/applying a toggle is not a live revocation guarantee.

## Grok keeps rules/index, with MCP disabled

The reviewed Grok 1.0.50 contract maps rule text and a generated skill instruction index into one literal `--rules` argument. It does not establish native skill discovery. MCP is unsupported and would remain unmapped. Select the same package digest in a separate profile, then disable only the tool:

```sh
keelmux profiles create --name 'Grok review' grok-review
keelmux plugins on --profile grok-review --digest PACK_DIGEST careful-review
keelmux tools off --profile grok-review careful-review/headroom
keelmux plugins preview --profile grok-review --harness grok --workspace /path/to/grok-project --application review-grok
keelmux plugins apply --profile grok-review --harness grok --workspace /path/to/grok-project --application review-grok --expect-plan GROK_PLAN_DIGEST
```

Use the returned Grok preview digest for `GROK_PLAN_DIGEST`. Package identity remains the same, while the profile's resolution reflects the disabled component. The literal rules/index argument has a 32 KiB JSON-encoded limit, including escapes; the complete launch request still has its separate 64 KiB limit.

## Update, collide safely and clear

Publish an update as a separate authoring directory: edit assets, refresh every changed file hash and change the manifest version/provenance label. Import it again. Importing a distinct revision never changes a profile's selected pin. Explicitly choose the new import `digest` with `plugins on --profile review --digest NEW_PACK_DIGEST careful-review`, review component preferences, then preview/apply again. Selecting a different digest starts its component preferences at defaults; off/on on the same digest preserves them. A retry of an existing recorded launch retains its original intent; start a new session for deliberately changed selections.

Existing mapped `AGENTS.md`, `GEMINI.md`, project configuration, discovery aliases and unowned skill directories are preserved as collisions, even if bytes match. Claude’s existing `.mcp.json` may keep unrelated server names: Keelmux preserves that file and supplies its separate receipt-bound MCP file. A duplicate generated server name still refuses. Two skills mapping to the same native name also collide. Preview does not silently merge, adopt or overwrite them. Use a different workspace or disable the conflicting declared component and preview again; manually managed native files remain outside this ownership receipt. Edited owned files cause drift refusal. `status` verifies owned bytes/identity, not loading or agreement with a newly toggled profile; launch separately checks current selections.

To remove this application's unchanged owned files:

```sh
keelmux plugins status --harness codex --workspace /path/to/codex-project --application review-codex
keelmux plugins clear --harness codex --workspace /path/to/codex-project --application review-codex --expect-receipt RECEIPT_DIGEST
```

Use `receipt.digest` from the verified status response for `RECEIPT_DIGEST`. Clear does not stop a running harness or remove unrelated content. `plugins recover` is an explicit separate operation for a valid interrupted local file intent; ordinary reads do not recover it automatically.

`plugins remove --digest PACK_DIGEST careful-review` archives that intact revision and disables matching selections. It does not clear already applied workspace files or stop sessions; re-preview/apply or clear those separately. The archive is retained, with no purge/automatic restoration; importing the exact archived revision is refused.

## What this sample verified

A freshly compiled development CLI completed 59 library-only operations with this exact sample. It imported package digest `583f6f5a1c56955461552f504a5039a5e55558a130c78db7a79c118136e48747`, retained the same pin across independent profiles, preserved the default selection during review-tool switches, and kept component preferences while a whole pack was disabled. Importing a distinct updated revision did not change existing selected pins.

In separate disposable owned workspaces, Codex, Claude, Gemini and OpenCode preview/apply/status/clear verified the exact skill, companion, rule and MCP files. Grok preview reported its enabled MCP component as unsupported; a separate tool-off profile verified its rules/index mapping. Preview and absent reads made no fixture changes. Exact repeated application reused its receipt; deliberately edited owned content caused status/clear refusal, restoring the original bytes verified the original receipt, and clear preserved unrelated sentinels. The fixture reaped all directly launched CLI commands and removed all 75 owned entries.

Two administrative setup failures and a later verifier expectation error remain retained: an unsafe report-parent mode before any command, a one-command Unix-socket path-length refusal, and a 28-command incomplete run whose Gemini preview correctly returned `launch_args: null`. The corrected verifier received independent source review before the fresh complete run. These earlier failures are not relabeled as passes. The cumulative bounded scope used 88 CLI calls, including the 29 calls from incomplete runs.

This sample journey did not run a provider, model, tool, MCP health check, login, network request or daemon. It did not test undeclared-file/hash/native-collision negatives, explicitly select the updated pin, or prove native reload, skill discovery, tool permissions or executable provenance. Each application reports native loaded state as **unknown**. Broader library/source checks have their own evidence and do not expand this sample’s executed scope.

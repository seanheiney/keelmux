# Reusable capabilities

Keep one versioned pack of skills, Markdown rules and MCP tools, then select it for several native agent harnesses. A profile pins exact pack bytes and remembers individual switches. Press **l** in the terminal switcher to inspect the local library and toggle a pack. Press **p** to choose a saved profile; the library shows the profile and revision you are editing. Browsing profiles never changes a running session.

Press **Tab** in the library to inspect that pack's component switches; **Enter** toggles the selected skill, rule or tool. Component preferences remain saved while the whole pack is off. An inactive row distinguishes that saved preference from the pack's effective gate. A different unselected revision does not borrow the selected revision's preferences.

**Development source:** these commands are implemented on the reusable-capabilities and capability-control branches. Published v0.2.1 does not include them. Saving a selection, applying native files, native loading, tool health and tool invocation are separate observations.

## Try the optional Headroom pack

Import its bundled declaration and switch its MCP component on or off:

```sh
keelmux plugins import --builtin headroom
keelmux plugins on headroom
keelmux tools off headroom/mcp
keelmux tools on headroom/mcp
keelmux profiles show default
```

This imports pinned declaration bytes for Headroom 0.40.0. It does not install the upstream executable. Install Headroom through its [upstream instructions](https://github.com/headroomlabs-ai/headroom), using the version you intend to review. The declaration calls `headroom mcp serve`; an executable with that name on PATH remains an external prerequisite. The pack's source revision and digest identify its declarations, not the installed executable's provenance.

Keelmux does not call Headroom's automatic native wrapper. It does not configure a proxy or guarantee token savings. Existing native sessions need their supported reload or a new session after application changes.

## Apply to one workspace

Select a harness and an existing workspace you own:

```sh
keelmux plugins preview --harness claude --workspace /path/to/project
keelmux plugins apply --harness claude --workspace /path/to/project
keelmux plugins status --harness claude --workspace /path/to/project
keelmux run --workspace /path/to/project --profile default claude
```

Preview creates nothing. It lists generated files, structured launch arguments, preserved collisions and unmapped declarations. Apply rechecks the exact pin, preview and workspace identity before writing. Pass `--expect-plan DIGEST` to bind application to a reviewed preview. Files and receipts are private, and credentials are neither read nor copied.

Launch checks the matching applied receipt and records the profile revision, resolution digest and application digest on the session. It refuses stale, edited, incompatible or partially mapped applications. It never silently applies files. A native CLI may still combine user, parent, managed or environment-selected configuration; its trust and permission controls remain authoritative.

An interrupted launch can be retried from its original recorded intent:

```sh
keelmux run --operation ORIGINAL_OPERATION_ID
```

This operation-only form accepts no workspace, provider or profile overrides and does not inspect today's provider version or saved selections. A recorded intent may be attempted once; an attempted or uncertain intent is reconciled against its exact runtime. A completed retry returns the original result. Missing operations refuse without creating a launch. Retrying after a profile toggle therefore retains the original snapshot; use a new operation for a deliberately changed launch.

To turn Headroom off for this workspace, save the switch and apply again:

```sh
keelmux plugins off headroom
keelmux plugins apply --harness claude --workspace /path/to/project
```

Use `tools off --profile review headroom/mcp` to disable only its MCP component in a named profile. `tools on` restores that component preference without changing the pinned version or enabling a disabled pack. These shortcuts use the same guarded selection API as `plugins on/off --component tool/mcp headroom`; neither runs Headroom nor applies native files.

To remove this application's unchanged owned files, use `plugins clear --harness claude --workspace /path/to/project`. This preserves other native files and does not stop a running harness. Edited owned content causes a refusal. `plugins recover` explicitly finishes a valid interrupted application; ordinary status and preview never recover it implicitly.

## Profiles and revisions

```sh
keelmux profiles create --name 'Code review' review
keelmux plugins on --profile review headroom
keelmux profiles show review
keelmux plugins apply --profile review --harness gemini --workspace /path/to/review
keelmux run --profile review --workspace /path/to/review gemini
```

The default profile is global to the selected local state directory. Other profiles are independent; choose one explicitly per launch. `--application ID` names the workspace application ledger and defaults to `work`. An application ID belongs to one exact harness/workspace pair. Use the same ID for preview, application, launch and removal.

Importing an update leaves selected pins intact. Use `plugins on --digest SHA PACK` to select another immutable revision deliberately. `off` keeps the selected digest and component preferences. Package enablement gates every component. `plugins remove --digest SHA PACK` archives one verified revision and disables matching selections; it preserves other revisions, archives and running processes. Archived revisions have no purge or automatic restore in this initial API.

## Inspect tools without invoking them

These commands inspect the enabled default-profile selection. After the off workflow above, explicitly enable the pack and its component first; this saves selection only and does not apply or execute anything.

```sh
keelmux plugins on headroom
keelmux tools on headroom/mcp
keelmux tools list
keelmux tools check headroom/mcp
keelmux tools check --health --workspace /path/to/empty-workspace headroom/mcp
```

Ordinary check reads declaration, executable metadata and required environment availability. It does not run a version probe, server or network request. Explicit `--health` starts the selected stdio server in a fresh private home/cache/temp, initializes MCP and lists its tools. The receipt reports the negotiated protocol, tool count and owned process cleanup. It never invokes a tool or sends a model request. The server executable remains trusted user-selected code; private environment directories are not an operating-system sandbox. HTTPS health is unqualified and performs no request.

## Author a portable pack

Create a directory with a `keelmux-plugin.json` manifest. Every other bundled file needs its exact SHA-256 in `files`. A skill points at a directory containing `SKILL.md`; a rule points at a Markdown file. For example:

```json
{
  "schema": 1,
  "id": "team-review",
  "version": "1.0.0",
  "files": {
    "skills/review/SKILL.md": "REPLACE_WITH_64_CHARACTER_SHA256",
    "rules/review.md": "REPLACE_WITH_64_CHARACTER_SHA256"
  },
  "skills": [{"id": "review", "path": "skills/review"}],
  "rules": [{"id": "review", "path": "rules/review.md"}]
}
```

Portable skill frontmatter uses a matching lower-case, hyphenated name and a one-line description:

```markdown
---
name: review
description: Review a change against project requirements and report actionable findings.
---
Read the project context, inspect the change, and cite the affected code.
```

Optional `license` and `compatibility` fields are accepted. Other native permission, executable or structured frontmatter fields are reported as unmapped. Companion assets are copied unchanged. Rules are instructions; they do not enforce permissions.

MCP tools use `transport: "stdio"` with a structured `command` array, or `transport: "https"` with an explicit URL. `env_refs` lists names only. No shell interpolation or credential values belong in a manifest. HTTPS authentication-header mapping is not implemented; such declarations are reported as unmapped. Import refuses symlinks, hard links, special files, writable foreign content, undeclared files, mismatched hashes and oversized packs.

## Native mapping coverage

| Harness | Skills | Rules | MCP |
| --- | --- | --- | --- |
| Codex | Project `.agents/skills` | Owned `AGENTS.md` | Owned project `.codex/config.toml` |
| Claude Code | Project `.claude/skills` | Namespaced `.claude/rules` | Supplemental receipt-bound `--mcp-config` |
| Gemini CLI | Project `.gemini/skills` | Owned `GEMINI.md` | Owned `.gemini/settings.json` |
| OpenCode | Project `.opencode/skills` | Explicit instructions file | Owned `opencode.json` |
| Grok | Applied instruction index | Literal `--rules` text | Unsupported by the reviewed installed contract |

Existing canonical files and skill discovery aliases are preserved and reported as collisions. No automatic merge overwrites them. Grok's index is an instruction compatibility mapping; it does not establish native skill discovery or invocation. All mappings report native loaded state as **unknown** until independent native evidence proves more.

Grok carries rules and its skill index as literal launch arguments. Preview counts JSON escaping and refuses an argument above 32 KiB of encoded text. The complete launch request retains its 64 KiB limit and is checked before a request or runtime launch effect; long additional arguments can still exceed it. Refusal preserves the prior applied files.

[Architecture contract](../architecture/0003-reusable-capability-library.md), [additional providers](providers.md) and [support matrix](support-matrix.md) describe the boundaries and qualification state.

## Qualified library controls

Development source `da42952` adds saved-profile selection and direct tool switches. Complete local normal/race tests, vet, development builds and twelve site checks passed. Fifteen actual PTY captures across three sizes verified profile independence and exact cleanup; the [control qualification record](../evidence/capability-controls-da42952.md) keeps this scope separate from native loading and release qualification.

The 80×24 [saved-profile picker](../site/evidence/library-profiles-da42952/profile-saved-profile-picker-80x24.png), [review-only tool switch](../site/evidence/library-profiles-da42952/profile-review-tool-off-80x24.png) and [saved-on component in a disabled pack](../site/evidence/library-profiles-da42952/profile-review-pack-off-saved-on-80x24.png) rasterize actual PTY cells from a deterministic inventory fixture and real Registry. No provider or tool ran; native loaded state remains **unknown**. The [capture bundle](../site/evidence/library-profiles-da42952/README.md) includes text equivalents and the original receipt.

Earlier development source `00c6b52` passed 31 compiled CLI checks, six actual PTY captures at three sizes, complete local and hosted source checks, and fresh installed macOS Codex/Claude terminal lifecycle. A separate executable from that source passed actual Headroom initialize/list-only health with three tools listed, zero invoked and complete owned cleanup. The [qualification record for that source](../evidence/reusable-capabilities-00c6b52.md) distinguishes those executables, receipts and remaining native-loading/provider gates. Published v0.2.1 does not include the library commands.

Development source `b730500` passed 32 compiled CLI checks and six actual PTY captures at 120×32, 80×24 and 40×14. The [qualification record](../evidence/reusable-capabilities-b730500.md) binds the exact source, executable and sanitized receipts. This does not establish native loading or qualify a released build.

The 80×24 [pack view](../site/evidence/reusable-capabilities-b730500/library-pack-on-80x24.png) and [component view](../site/evidence/reusable-capabilities-b730500/library-component-off-80x24.png) show real saved Registry changes against a deterministic inventory fixture. The images rasterize captured terminal cells. No native provider or model was started; loaded state remains **unknown**.

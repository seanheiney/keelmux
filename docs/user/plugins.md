# Reusable capabilities

Keep one versioned pack of skills, Markdown rules and MCP tools, then select it for several native agent harnesses. A profile pins exact pack bytes and remembers individual switches. Press **l** in the terminal switcher to inspect the local default profile and toggle a pack.

Press **Tab** in the library to inspect that pack's component switches; **Enter** toggles the selected skill, rule or tool. Component preferences remain saved while the whole pack is off.

**Development source:** these commands are being qualified on the reusable-capabilities branch. Published v0.2.1 does not include them. Saving a selection, applying native files, native loading, tool health and tool invocation are separate observations.

## Try the optional Headroom pack

Import its bundled declaration and switch its MCP component on or off:

```sh
keelmux plugins import --builtin headroom
keelmux plugins on headroom
keelmux plugins off --component tool/mcp headroom
keelmux plugins on --component tool/mcp headroom
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

To turn Headroom off for this workspace, save the switch and apply again:

```sh
keelmux plugins off headroom
keelmux plugins apply --harness claude --workspace /path/to/project
```

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

```sh
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
| Grok | Immutable instruction index | Literal `--rules` text | Unsupported by the reviewed installed contract |

Existing canonical files and skill discovery aliases are preserved and reported as collisions. No automatic merge overwrites them. Grok's index is an instruction compatibility mapping; it does not establish native skill discovery or invocation. All mappings report native loaded state as **unknown** until independent native evidence proves more.

[Architecture contract](../architecture/0003-reusable-capability-library.md), [additional providers](providers.md) and [support matrix](support-matrix.md) describe the boundaries and qualification state.

# Skills and exact linking

The optional connection skill links the terminal that Keelmux actually launched. It uses inherited private launch correlation, rather than a guessed native thread, a matching directory or the newest transcript.

The current owned bundle remains named **backplane** for compatibility. Product renaming does not silently replace another skill with a similar name.

## Preview and apply

For a project:

```sh
keelmux setup --scope project --root /absolute/path/to/project --providers codex,claude
keelmux setup --scope project --root /absolute/path/to/project --providers codex,claude --apply
```

For your user setup, select your own root explicitly or omit `--root` to use your home:

```sh
keelmux setup --scope user --providers codex,claude
keelmux setup --scope user --providers codex,claude --apply
```

Choose only the providers you want. The default selection is `codex,claude`. Grok may appear in the catalogue but its mapping and native lifecycle are outside the initial support matrix.

Setup emits per-provider JSON receipts. Existing unowned files, ownership mismatches and modified owned bundles are preserved and reported. Applying the same valid owned revision is repeatable. Setup does not log in, change a model or load instructions into a running conversation.

## Invoke inside the intended managed CLI

| Provider | Owned location beneath the selected root | Current invocation |
| --- | --- | --- |
| Codex | `.agents/skills/backplane` | `$backplane` or select through `/skills` |
| Claude Code | `.claude/skills/backplane` | `/backplane` |

The bundle requests explicit invocation. Claude's native `/rc` remains separate. Depending on the CLI and discovery rules, reopen its selector or reload the session before the skill appears.

A skill invocation may use the model and its tool permissions. Discovery checks only show that the CLI found the bundle. Installed, discovered, loaded and invoked are different observations.

The helper accepts only no argument, `status` or `off`. Repeating a successful link returns its existing binding. `status` reports that binding; `off` turns off its declared link without stopping the process, logging out, deleting history or removing files.

From an exact managed shell/tool context, the same helper is available as:

```sh
keelmux link
keelmux link status
keelmux link off
```

An unrelated shell has no verified model context and is refused. Do not copy or print `BP_LINK_TOKEN`, invent launch variables or borrow another session's IDs.

## Remove a skill

```sh
keelmux skills remove --scope project --root /absolute/path/to/project --providers codex,claude
```

Removal checks the owned revision and moves the intact matching bundle into a private `.backplane/removals` archive with a receipt. It does not automatically purge retained bytes or erase later user edits. Processes with open file descriptors can still access retained files. Removing files is not proof that a running model forgot instructions.

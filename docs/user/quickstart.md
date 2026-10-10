# Your first session

You need a terminal and an installed Codex or Claude Code CLI. Keelmux 0.2 bundles native supervision; historical v0.1.0 additionally needs tmux. Authenticate through that provider's normal workflow before using it for model work. Keelmux does not sign you in, copy credentials or install a provider CLI.

The tested runtime is macOS on Apple Silicon. Check the [support matrix](support-matrix.md) before relying on another platform or CLI version. Installation and release availability are described in [Installation](install.md).

## Check the installation

```sh
keelmux version
keelmux providers
keelmux doctor
```

`providers` probes native version and login status without a model turn. A credential status is narrower than verified account identity, model access or quota. The 0.2 `doctor` checks the bundled native supervisor, at least one launchable native provider and daemon reachability. Published v0.1.0 checks its tmux prerequisite. Missing prerequisites return a nonzero exit status; a passing check does not establish account or model access.

## Launch work

Use an existing project directory. Flags for Keelmux precede the provider; remaining arguments go to the native CLI.

```sh
keelmux run --name "API review" --workspace /absolute/path/to/project codex
keelmux
```

Select **API review** and press **Enter**. You enter the real Codex terminal. Its workspace trust, prompts, authentication and permissions follow Codex's own behavior. For Claude Code, substitute `claude` in the launch command.

The launch receipt contains stable session and execution IDs. It confirms the owned terminal launch; it does not confirm a model accepted a prompt or completed work.

## Leave and return

Press **Ctrl-G**, then **d**, to detach to the switcher. Press **Enter** again to return to that terminal. Press **q** in the switcher to close the client; the owned terminal and daemon remain running.

**Ctrl-G Ctrl-G** passes a literal Ctrl-G to the native program. Other keys remain native. Ctrl-C goes to the native CLI, so its effect depends on that CLI and current activity.

You can inspect or attach directly with the session ID from the launch receipt:

```sh
keelmux list
keelmux inspect session_REPLACE_WITH_EXACT_ID
keelmux attach session_REPLACE_WITH_EXACT_ID
```

Use **v** in the switcher, or `attach SESSION --read-only`, for a viewer that cannot send terminal input or resize it. A second managed writer is refused while the first owns input.

## Add the optional connection skill

Preview an owned project installation before applying it:

```sh
keelmux setup --scope project --root /absolute/path/to/project --providers codex,claude
keelmux setup --scope project --root /absolute/path/to/project --providers codex,claude --apply
```

The current skill is named **backplane**: use `$backplane` or `/skills` in Codex and `/backplane` in Claude. Native discovery or reload may be necessary. Invoking a skill can involve the model and native tool permissions. File installation alone does not establish invocation or instruction loading. See [Skills and linking](skills.md).

If a step fails, keep the launch operation ID and open [Troubleshooting](troubleshooting.md). Repeating a command with a new ID is not a way to resolve an uncertain launch.

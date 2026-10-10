# More ways to organize and inspect work

This guide covers the feature batch developed **after the published v0.2.1 release**. Use a source build that includes the command and switcher integration. Installing v0.2.1 alone does not add these commands. Existing switcher groups predate this batch; their new command-line administration is covered here.

Check the executable you will use before following a walkthrough:

```sh
keelmux version --json
keelmux groups --help
keelmux usage --help
keelmux peers --help
keelmux diagnose --help
```

Keep the reported source commit with any diagnostic or test result. For a source build, follow [Installation](install.md) and keep `keelmux`, `keelmuxd`, `keelmux-entry` and `keelmux-supervisor` from that build together. Source availability, a successful local test and a published release are separate milestones. The [support matrix](support-matrix.md) remains the reference for qualified platforms and runtime behavior.

## Choose the right tool

| You want to… | Start here | What changes |
| --- | --- | --- |
| Arrange company, product or personal collections | [Groups](groups.md) | Private client navigation and references |
| Review recorded calls, models, tokens or money | [Usage](usage.md) | Reads the local ledger; explicit import adds user-entered records |
| Discover permitted sessions on an existing SSH home | [SSH peers](ssh-peers.md) | Explicit enrollment, inventory grants and a bounded local cache |
| Investigate state without starting services | [Diagnostics](diagnostics.md) | Observations only; optional native version subprocesses require `--probe-versions` |

These commands do not submit model turns. Groups, usage and peer administration select a local `--state` directory and reject `--entry`. Diagnostics can inspect a supplied entry socket statically; execution-home diagnostics still run at that home.

## A daily workflow

Use the same private client state each day. Global flags go before the command:

```sh
keelmux --state /absolute/private/keelmux groups list
keelmux --state /absolute/private/keelmux usage
keelmux --state /absolute/private/keelmux diagnose
```

Before changing groups from the CLI, close the switcher using **q**. If attached to a native terminal, first detach with **Ctrl-G d**. CLI group edits deliberately refuse to overwrite a place that an open switcher owns. Reopen the same state afterward; the active group, filters and selections are remembered.

In the integrated switcher, select a session and press **u** to inspect its local usage observations for the last 24 hours. **r** rereads the ledger and **Escape** returns. This key is contextual: **u** in the channel inbox still reconciles a delivery receipt. A remote session's usage panel reads the client's local ledger; it does not fetch the remote account or ledger.

For an enrolled SSH home, use `peers list --principal owner --refresh` to obtain a fresh permitted inventory. A fresh record can help you find work, but this SSH source has no terminal attachment capability. Continue terminal work through the execution home's existing supported workflow.

## Keep evidence and authority separate

Group membership does not grant access. SSH visibility requires explicit host and session grants. A saved session or matching PID does not establish current model, account or quota. An empty usage ledger means no recorded observations; it does not mean zero actual consumption.

The native controller work in this batch is host-internal integration. It does not provide a public structured-prompt command, a qualified native account reader, a product launcher or conversation resume. Native model interaction continues through the supported native terminal. See [daily use](daily-use.md) and [recovery](operations.md).

## Keep the selected state private

Use a local, owned directory with mode `0700`; private configuration and state files use `0600`. Use clean absolute paths in examples. JSON does not expand `~` or shell variables. Do not copy an execution home's registry to a second machine to enroll it: enroll the existing home's stable identity instead.

The guides use illustrative names, paths, IDs and amounts. Replace identities with exact values from your own inventory or command receipts. Never put SSH private keys, native login tokens or provider credentials in the examples. Client state, peer inventory and usage exports can still contain private metadata; preserve their permissions when backing them up or sharing selected evidence.

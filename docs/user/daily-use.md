# Daily use

Run `keelmux` to open the remembered switcher. Navigation reads known inventory; it does not call a model. Entering a terminal brings you back to the real native CLI.

## Switcher keys

| Key | Action |
| --- | --- |
| Arrows or `j` / `k` | Select work |
| `/` | Search the current group; Enter or Escape finishes editing |
| Enter | Acquire managed input and attach |
| `v` | Attach a read-only viewer |
| `g` | Open groups |
| `b` | Return to the previous group |
| `p` | Edit a local draft for the selected thread and lane |
| `c` | Open the selected thread's authored channel inbox |
| `r` | Refresh inventory |
| `?` | Show contextual help |
| `q` | Close the switcher; owned sessions keep running |

## Source usage panel

In the feature batch after v0.2.1, press **u** from the switcher to read the local ledger for the captured host/session. The panel preserves navigation; **r** rereads and **Escape** returns. It keeps unknown coverage, manual declarations, model attribution and stale observations visible. No account or money detail is granted by this session view. [Usage guide](usage.md), [source availability](feature-expansion.md).

## Selected runtime

The selected workspace is followed by a compact **Runtime** line. `native` identifies Keelmux's owned supervisor; `legacy tmux` identifies an existing execution on the optional compatibility adapter. `unknown` means the inventory did not supply a recognized backend. It is never inferred from the session's name, workspace or managed mode.

The state is the home's last inventory observation. `missing` retains the work without launching a replacement; `uncertain` means the outcome needs inspection. When the connection is offline, **(stale)** marks the retained runtime observation and attachment is unavailable. A missing supervisor does not prove that its child exited. Use `keelmux inspect SESSION_ID` for the exact execution, capabilities and source-qualified evidence, then `keelmux reconcile` when recovery calls for a fresh runtime check. An unavailable legacy execution may need its original tmux installation restored; new native work does not use tmux as a fallback.

Model and account details use independent evidence. Terminal appearance, a running process and a successful attachment cannot verify either identity. Unknown evidence stays unknown.

## Recover a saved attachment

After a lost attachment response, the switcher retains the original acquisition identity. **Saved attach** names that pending work and its saved generation (`g1`, for example). This recovery target can differ from the currently selected row, and a replacement generation does not change it. Long labels may be clipped; the private client state retains the full session, execution, generation and operation IDs.

Follow **Enter retry same attach** or **x reconcile/release** for the saved acquisition. If **lease release pending** appears, **x** retries its exact release. Reconnect with **r** if the connection is offline. Do not edit the pending token, delete the saved intent or acquire different work to bypass recovery. These actions recover attachment authority; they never replay native input. See [recovery boundaries](operations.md#recover-a-session) and [troubleshooting](troubleshooting.md).

## Native terminal keys

**Ctrl-G d** detaches to the switcher. **Ctrl-G Ctrl-G** sends literal Ctrl-G. Other keys, including Ctrl-C, reach the native CLI. Only the managed input owner changes terminal dimensions.

Keelmux 0.2 owns persistent native PTYs and processes, with optional compatibility for existing tmux work. Historical v0.1.0 uses its qualified tmux adapter. Detach returns to the same terminal while its host/runtime survives. Native application exit ends that terminal. Host reboot and native conversation resume are separate capabilities; consult the [exact support scope](support-matrix.md) before relying on them.

## Company and product groups

Groups organize references; they do not duplicate conversations or change permissions. Select the session you want to organize, press **g**, then use:

| Key in groups | Action |
| --- | --- |
| Enter | Open the selected group or All work |
| `n` | Create a top-level group |
| `c` | Create a child of the selected named group |
| `a` | Add the selected session's workspace as a project reference |
| `s` | Pin the selected session |
| `d`, then `d` again | Remove only the navigation group |
| Escape | Return to the switcher |

Each group remembers selection, filter and available view position. **All work** includes ungrouped sessions. Removing a group retains its sessions and drafts; child collections are reparented. Group membership does not grant access to another host or account.

## Local drafts

Press **p** on a selected session to compose a draft. Enter adds a newline; Escape saves and returns. Drafts are keyed to stable thread and recipient lane and remain local. They are not submitted to a model, posted to chat or injected into a native composer.

A changed target is shown before future delivery can be added. Current model interaction happens in the native terminal. Structured prompt submission remains on the [roadmap](roadmap.md).

## Channel inbox

After [binding a thread](channels.md), select its session and press **c**. Incoming notes are authored snapshots, with their sender and binding visible. **b**, then Enter chooses one active destination; **p** opens a local reply. **Enter** adds a newline and **Ctrl-S** queues the exact reply to chat. Escape returns to the inbox or work. Reply text never enters a native composer.

Use arrows to select a note, **PgUp/PgDn** to scroll its text, **]** for the next history page and **[** for the previous page. Each page contains at most 100 notes; paging preserves the chosen destination and reply draft. **d** queries the selected local note's exact delivery receipt and **u** reconciles an uncertain receipt read-only. **r** refreshes the current page and any saved queue intent. **?** shows the contextual keys. Configuration activity, intake freshness, a local queue receipt and an acknowledged external post remain separately labeled.

If the queue acknowledgement is lost, the saved intent is frozen. Reopen Keelmux and query its receipt; do not create a replacement ID. An explicit Ctrl-S retry can reuse that exact intent only after the local service reports it missing and the same destination remains active. An uncertain external post is never resent. See [receipt meanings](channels.md#read-and-reply).

## Stop a session deliberately

Detach and inspect the exact session first:

```sh
keelmux inspect session_REPLACE_WITH_EXACT_ID
keelmux stop session_REPLACE_WITH_EXACT_ID
```

Stop ends the owned terminal execution. It is different from closing the switcher, unlinking a skill or removing a group. Preserve the printed stop operation ID if the result is uncertain; see [Recovery](operations.md#recover-a-session).

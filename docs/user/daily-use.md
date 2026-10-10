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
| `r` | Refresh inventory |
| `?` | Show contextual help |
| `q` | Close the switcher; owned sessions keep running |

After a lost attachment response, the switcher retains the original acquisition identity. Follow its **Enter retry same attach** or **x reconcile/release** action. Do not edit the stored pending token or start a different acquisition to bypass recovery.

## Native terminal keys

**Ctrl-G d** detaches to the switcher. **Ctrl-G Ctrl-G** sends literal Ctrl-G. Other keys, including Ctrl-C, reach the native CLI. Only the managed input owner changes terminal dimensions.

This release path proves detach and return to the same terminal while its host/runtime survives. Native application exit ends that terminal. Host reboot and native conversation resume are separate unfinished capabilities.

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

A changed target is shown before future delivery can be added. Current model interaction happens in the native terminal. Mattermost, IRC and structured prompt submission remain on the [roadmap](roadmap.md).

## Stop a session deliberately

Detach and inspect the exact session first:

```sh
keelmux inspect session_REPLACE_WITH_EXACT_ID
keelmux stop session_REPLACE_WITH_EXACT_ID
```

Stop ends the owned terminal execution. It is different from closing the switcher, unlinking a skill or removing a group. Preserve the printed stop operation ID if the result is uncertain; see [Recovery](operations.md#recover-a-session).

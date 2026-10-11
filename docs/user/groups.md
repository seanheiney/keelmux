# Company and product groups

Groups are optional, private collections of references to work. A company can contain product groups; a personal collection can stay flat. One session can appear in several groups without creating another session or changing its execution home.

The switcher already supports groups. **The CLI commands below belong to the source feature batch after v0.2.1.** Check `keelmux groups --help` in your installed build. See [feature availability](feature-expansion.md).

## Create a company and product

Close the switcher before editing its groups from another terminal. Use **Ctrl-G d** to detach from native work, then **q** to close the switcher. Its owned sessions continue running.

```sh
keelmux groups list
keelmux groups create --name "Northstar"
```

Every successful command returns JSON. Copy the new `id` from the create response; use that exact `group_…` ID for the next command. `GROUP_ID` below is a placeholder, not an accepted ID:

```sh
keelmux groups create --name "Website" --parent GROUP_ID
```

Save the child's returned ID too. Add a project workspace to that child:

```sh
keelmux groups add --kind workspace --workspace /absolute/projects/website CHILD_GROUP_ID
keelmux groups select CHILD_GROUP_ID
keelmux
```

The workspace reference matches the exact recorded path. Without `--host`, it matches that path across the hosts already visible to this client. To limit it to one execution home, use the exact host identity from inventory:

```sh
keelmux groups add --kind workspace --workspace /absolute/projects/website \
  --host HOST_ID CHILD_GROUP_ID
```

A host-qualified reference and an unqualified reference are distinct. If you added the unqualified one first, remove it to narrow the group:

```sh
keelmux groups remove --kind workspace --workspace /absolute/projects/website CHILD_GROUP_ID
```

Group names may contain spaces and must be 1–128 UTF-8 bytes after trimming. Paths must be clean and absolute. Flags precede the final group ID.

## Pin a particular item

Use `keelmux list` or an existing inventory receipt to find the exact identity. Unlike group administration, `list` follows the normal local daemon workflow and may start the daemon. A saved reference is a navigation choice; adding one does not check that its target is currently available.

```sh
keelmux groups add --kind session --id SESSION_ID GROUP_ID
keelmux groups add --kind work --id WORK_ID GROUP_ID
keelmux groups add --kind thread --id THREAD_ID GROUP_ID
```

Choose a workspace reference to collect sessions recorded at that path, or a stable identity to pin particular work. References match stable identities across execution changes. A parent group also includes members of its descendants. Neither choice can expose sessions that the client's source does not permit.

Remove the exact reference when it is no longer useful:

```sh
keelmux groups remove --kind session --id SESSION_ID GROUP_ID
```

For workspace removal, repeat the original path and optional host. Removing an already absent exact reference is safe.

## Reorganize without losing your place

```sh
keelmux groups rename --name "Customer website" CHILD_GROUP_ID
keelmux groups parent --parent NEW_PARENT_GROUP_ID CHILD_GROUP_ID
keelmux groups parent CHILD_GROUP_ID
keelmux groups select all
```

The third command makes the child a top-level group. Rename and reparent preserve the group ID and its saved place. A missing parent or a parent cycle is rejected. `select all` returns to **All work**, including ungrouped sessions.

```sh
keelmux groups delete GROUP_ID
```

Delete removes the collection and its saved navigation place. Sessions and drafts remain. Children move to the deleted group's parent; deleting a top-level group makes its children top-level. If the deleted group was active, the next view is All work.

## Use groups in the switcher

Select a session, then press **g**:

| Key | Action |
| --- | --- |
| Enter | Open the selected group or All work |
| `n` | Create a top-level group |
| `c` | Create a child of the selected named group |
| `a` | Add the selected session's workspace |
| `s` | Pin the selected session |
| `d`, then `d` | Delete the selected navigation group |
| Escape | Return to the switcher |

Back in the switcher, **b** returns to the previous group. Each group remembers its selection and filter. Use the CLI for rename, reparent and exact-reference removal.

## State selection and recovery

All examples use the default client state. To choose another, consistently put `--state /absolute/private/client` before `groups`. The commands never contact or start a daemon. Reading an absent group state returns an empty list without creating it; a successful edit creates private client state as needed.

For an entry client, edit its local client state without `--entry`, then reconnect using both the same `--state` and the entry socket. The entry host does not store this client's groups.

If an edit reports that the client place is locked, close the switcher and retry the intended edit. Do not delete its lock or overwrite `place.json`. If saved state is corrupt or incompatible, preserve it and use [diagnostics](diagnostics.md) and a trusted backup. `place.json` also contains private drafts and pending attachment information; replacing it just to reset groups can discard recovery evidence.

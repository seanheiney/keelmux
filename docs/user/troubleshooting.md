# Troubleshooting

Start with the selected state and exact session. Preserve operation IDs and drafts. Repairs should recover the original target instead of silently creating another one.

| Symptom | Check | Next action |
| --- | --- | --- |
| `keelmux` is not found | Installation destination and current shell PATH | Use the installed absolute executable path; apply the printed PATH step yourself |
| Daemon companion unavailable | CLI and daemon executables are beside each other | Reinstall the complete verified release or use the complete source `bin/` build |
| tmux unavailable | `keelmux doctor` | Install tmux through your normal trusted package workflow, then retry |
| Provider unavailable/unrecognized version | `keelmux providers` | Install or repair that native CLI separately; unsupported versions require their own acceptance |
| Native login or trust prompt | Actual native CLI screen | Complete that provider's own intended workflow; Keelmux does not bypass it |
| Model/account shows `unknown` | `keelmux inspect SESSION` evidence/source | Treat it as unknown; credential presence and launch are not per-session attribution |
| State ownership/private-mode refusal | Exact selected directory, real owner, mode and symlinks | Inspect and repair the intended directory; do not create a replacement authority to evade the refusal |
| Unix socket path too long | `--state` path plus socket filename | Choose a shorter owned private path for new state; moving an existing authority needs a reviewed migration |
| Another writer owns input | Existing managed attachment and exact pending state | Detach it; use a read-only view if appropriate |
| Attach outcome unknown | Switcher footer and saved pending identity | Retry that exact attachment or use its reconcile/release action; retain tokens privately |
| Missing runtime | `keelmux inspect SESSION`, then `keelmux reconcile` | Retain the record and inspect native history; no automatic replacement is started |
| Lost launch/stop response | Printed operation ID and full original request | Retry only that operation; uncertainty stays explicit until reconciled |
| Skill missing in native selector | Setup receipt, provider mapping and native reload | Reopen/reload the supported catalogue; installation is not loading |
| Skill collision or modified bundle | Per-provider setup/removal receipt | Preserve the existing file; resolve ownership or choose the intended scope before applying |
| Link refused in a normal shell | Inherited managed launch context | Invoke inside the intended owned session; never manufacture private launch variables |
| Home unavailable through entry | `directory` home availability versus entry connection | Repair the named home; healthy homes can remain usable |
| Codex broker method/schema refused | Experimental pinned support and receipt | Keep the blocker and report a sanitized schema; do not allow arbitrary passthrough |

## Collect a useful report

Provide the application build/source, OS/architecture, tmux/native versions, exact command with sensitive values removed, expected versus observed behavior, session capability state and operation outcome. State whether the issue used local native terminals or the experimental entry/broker.

`providers` and `inspect` already qualify unknown evidence, but their JSON may contain local executable/workspace paths and stable IDs. Review it before posting. Native screens, full state directories, runtime receipts and logs can contain more sensitive material. The project does not need credential files or private launch tokens to reproduce a report.

See [Support](../../SUPPORT.md) for issue routing and [Security](../../SECURITY.md) for sensitive vulnerabilities.

# Troubleshooting

Start with the selected state and exact session. Preserve operation IDs and drafts. Repairs should recover the original target instead of silently creating another one.

| Symptom | Check | Next action |
| --- | --- | --- |
| `keelmux` is not found | Installation destination and current shell PATH | Use the installed absolute executable path; apply the printed PATH step yourself |
| Daemon companion unavailable | CLI and daemon executables are beside each other | Reinstall the complete verified release or use the complete source `bin/` build |
| Native supervisor unavailable | `keelmux doctor` | Install the complete native release with its bundled supervisor; preserve uncertain execution records |
| tmux unavailable for an existing legacy execution | `keelmux inspect SESSION` | Restore tmux for that exact legacy session; new native sessions do not depend on it |
| Provider unavailable/unrecognized version | `keelmux providers` | Install or repair that native CLI separately; unsupported versions require their own acceptance |
| Native login or trust prompt | Actual native CLI screen | Complete that provider's own intended workflow; Keelmux does not bypass it |
| Model/account shows `unknown` | `keelmux inspect SESSION` evidence/source | Treat it as unknown; credential presence and launch are not per-session attribution |
| State ownership/private-mode refusal | Exact selected directory, real owner, mode and symlinks | Inspect and repair the intended directory; do not create a replacement authority to evade the refusal |
| Unix socket path too long | `--state` path plus socket filename | Choose a shorter owned private path for new state; moving an existing authority needs a reviewed migration |
| Another writer owns input | Existing managed attachment and exact pending state | Detach it; use a read-only view if appropriate |
| Attach outcome unknown | **Saved attach** target and generation, even if another row is selected | Enter retries the original saved acquisition; x reconciles/releases it. Reconnect with r when offline; retain tokens privately |
| Lease release pending | Original saved acquisition and connection state | x retries its exact release; do not acquire different work or remove private recovery state |
| Runtime line says `unknown` | Recorded backend in `keelmux inspect SESSION` | Preserve the unknown state; managed mode and a plausible path do not establish ownership |
| Runtime observation is `(stale)` | Offline connection versus retained inventory | r refreshes/reconnects; a retained running observation does not authorize attachment |
| Missing runtime | `keelmux inspect SESSION`, then `keelmux reconcile` | Retain the record and inspect native history; no automatic replacement is started |
| Runtime outcome uncertain | Exact execution and operation receipt | Reconcile the original attempt; a missing helper or reply does not prove child exit or authorize another launch |
| Lost launch/stop response | Printed operation ID and full original request | Retry only that operation; uncertainty stays explicit until reconciled |
| Skill missing in native selector | Setup receipt, provider mapping and native reload | Reopen/reload the supported catalogue; installation is not loading |
| Skill collision or modified bundle | Per-provider setup/removal receipt | Preserve the existing file; resolve ownership or choose the intended scope before applying |
| Link refused in a normal shell | Inherited managed launch context | Invoke inside the intended owned session; never manufacture private launch variables |
| Home unavailable through entry | `directory` home availability versus entry connection | Repair the named home; healthy homes can remain usable |
| Codex broker method/schema refused | Experimental pinned support and receipt | Keep the blocker and report a sanitized schema; do not allow arbitrary passthrough |
| Channel binding refused | Exact bot identity, server/team, channel/root, private credential file and bot membership | Repair that scoped configuration; display names and broader credentials do not substitute for proof |
| Active channel has no new notes | Intake state, last check/success, sender allowlist and activation time | Incoming bot/self, other roots and unlisted senders are excluded; a failed poll does not erase the last success |
| Channel reply acknowledgement lost | Saved message ID, immutable destination/text and delivery receipt | Query the original receipt; never create a replacement ID for an uncertain external effect |
| Channel receipt stays uncertain | Matching existing remote post and bounded read-only reconciliation | Keep it uncertain if evidence is unavailable; a missing result does not authorize resend |
| IRC history gap | Persistent connection generation and required server capabilities | Treat disconnected history as unavailable; reconnect does not restore complete history |
| Old binary refuses upgraded state | Registry schema 3 or client place version 2 | Keep the newer state; use a compatible release or restore a coordinated pre-upgrade backup |

## Collect a useful report

Provide the application build/source, OS/architecture, runtime backend and native CLI versions, exact command with sensitive values removed, expected versus observed behavior, session capability state and operation outcome. State whether the issue used local native terminals, authored channels or the experimental entry/broker. For channels, include the provider/server version, finite intake/receipt state and a synthetic reproduction; exclude bot tokens, private note text and raw server responses.

`providers` and `inspect` already qualify unknown evidence, but their JSON may contain local executable/workspace paths and stable IDs. Review it before posting. Native screens, full state directories, runtime receipts and logs can contain more sensitive material. The project does not need credential files or private launch tokens to reproduce a report.

See [Support](../../SUPPORT.md) for issue routing and [Security](../../SECURITY.md) for sensitive vulnerabilities.

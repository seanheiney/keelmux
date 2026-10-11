# Usage observations

Use `keelmux usage` to review the local usage ledger without starting a daemon or contacting a provider. This command and the switcher's usage panel belong to the **source feature batch after v0.2.1**. Check `keelmux usage --help`; see [feature availability](feature-expansion.md).

The ledger records evidence with its source, time and scope. There is no automatic native account or quota reader wired into this command. A new installation can correctly show no ledger, unknown models and unknown token usage. Zero observed calls means the ledger has no matching observed calls; actual consumption may be unknown.

## Review a day or a session

```sh
keelmux usage
keelmux usage --host HOST_ID --session SESSION_ID
keelmux usage --from 2026-10-10T00:00:00Z --to 2026-10-11T00:00:00Z \
  --timezone America/New_York --money
keelmux usage --json
```

Replace the sample date and IDs with your review period and exact context. The default is the preceding 24 hours, displayed in UTC. The start is inclusive and the end is exclusive. `--timezone` changes the display timezone; it does not reinterpret the absolute timestamps.

Use `--state /absolute/private/keelmux` before `usage` to read another local ledger. If `usage.db` does not exist, the view reports that absence without creating it. `--entry` is unsupported: run the command at the execution home whose ledger you intend to inspect.

| Filter or option | Meaning |
| --- | --- |
| `--host`, `--session`, `--project`, `--task` | Exact recorded context identities |
| `--harness`, `--provider` | Exact recorded names |
| `--model` | Trusted actual model evidence; a requested model alias does not satisfy this filter |
| `--freshness 15m` | Positive maximum evidence age; default 15 minutes |
| `--expected-source ID` | Report a missing expected source; repeat for several sources |
| `--accounts` | Include available local account and subscription detail |
| `--money` | Include available local money detail |
| `--json` | Emit the qualified projection and its coverage fields |

Use source IDs from the recorded projection for source-specific inspection. Manual import isolates source IDs and record namespaces from trusted observations. Account and money flags are display choices for the local OS user, not a permissions boundary against that same user.

## Read the result accurately

| Result | How to use it |
| --- | --- |
| Observed provider calls | Calls supported by structured observations in this period and scope |
| User-entered calls | Your own declarations, counted separately |
| Logical requests and native turns | Known correlations; unattributed calls remain visible |
| Retries and child calls | Additional attempts, not automatically additional user requests |
| Actual models | Qualified actual-model evidence; requested aliases remain separate |
| Token dimensions | Known measurements with unknown and conflicted attempts identified |
| Money | Separate kinds, currencies and subscription cadences |
| Quota windows | Capacity evidence, separate from spend and token counts |

A token dimension marked `included_in` is already included in its parent dimension. Do not add it again. Unknown counts stay unknown; a missing count is not zero. A source-claimed complete record does not establish complete account-wide coverage.

Estimates, actual charges, subscription commitments, credits and top-ups have different meanings. Keep them separate when reviewing a budget; the view does not turn them into a single bill or perform currency conversion. A subscription commitment does not measure consumption. A percentage-based quota observation cannot establish requests, tokens or money spent. Stale or conflicting quota evidence cannot establish current readiness.

Freshness measures when evidence was observed. Historical records may be useful for a past period while stale for a current decision. Increasing `--freshness` changes the label's threshold; it does not collect new evidence.

## Import a private estimate

Import is an explicit local write. It accepts a version-1 JSON document with 1–128 observations and a maximum size of 1 MiB. Each observation has exactly one attempt or money payload. Imported records are always `manual`, `user_entered`, with unknown scope coverage. Imports cannot establish provider-verified charges or shared quota.

Create an owned private folder for your input file, set `umask 077` in that shell, and save this JSON as `/absolute/private/usage-estimate.json`. The provider, context, date and amount below are illustrative; the amount is your entered estimate, not a quoted provider price.

```json
{
  "version": 1,
  "observations": [
    {
      "id": "website-estimate-2026-10-10",
      "source": {
        "id": "personal-review",
        "kind": "manual",
        "quality": "user_entered",
        "observed_at": "2026-10-10T17:00:00Z",
        "sequence": 1,
        "coverage": "unknown"
      },
      "money": {
        "provider": "example-provider",
        "namespace": "personal-review",
        "record_id": "website-estimate-2026-10-10",
        "context": {"harness": "native-cli"},
        "at": "2026-10-10T16:59:00Z",
        "kind": "estimate",
        "currency": "USD",
        "decimal": "0.30"
      }
    }
  ]
}
```

```sh
chmod 600 /absolute/private/usage-estimate.json
keelmux usage import /absolute/private/usage-estimate.json
keelmux usage --from 2026-10-10T00:00:00Z --to 2026-10-11T00:00:00Z --money
```

The import receipt reports `imported`, `duplicates`, `provenance` and `scope_coverage`. The resulting amount remains labeled user-entered. This example has no session identity, so it appears in the overall period view, not a selected session's panel. To attribute your own record, supply the exact applicable `host`, `session`, `project` or `task` fields inside `context`; do not guess them from a terminal title.

Amounts use decimal **strings**, not JSON floating-point numbers. Use `null` for an unknown amount, not `"0"`. Currency is a three-letter uppercase code. A `subscription_commitment` also needs a `context.subscription` and a `cadence` of `weekly`, `monthly` or `annual`; it cannot be linked to an individual attempt. Do not put credentials or message text in context fields.

For a manual call declaration, use an `attempt` payload instead of `money`. Its key needs `provider`, `namespace`, `request_id` and `attempt_id`; it also needs `at` and applicable `context`. A manual actual-model or token claim remains a declaration and does not become trusted measured usage. Keep observation and attempt identities stable across retries.

Standard input is available for a prepared document:

```sh
keelmux usage import - < /absolute/private/usage-estimate.json
```

## Retry and investigate safely

If the import acknowledgement is lost, replay the **identical file**. An identical observation is a duplicate, not an added charge or call. Reusing an observation identity for different content rejects the batch; do not assign new IDs merely to work around uncertainty. The batch is atomic: invalid or conflicting records reject the whole import.

Duplicate JSON keys, unknown fields, unsupported provenance, malformed identities and quota payloads are rejected. A rejected parse is not evidence of an import. Preserve the input and receipt when investigating. If output fails after a commit, the command identifies that outcome; an identical retry is safe.

There is no interactive ledger editor or automatic correction workflow in this batch. Preserve a corrupt ledger for investigation and use a trusted backup rather than deleting it to clear an error. A fresh projection does not query the provider to settle missing or conflicting evidence.

## In the switcher

Select a session and press **u**. The panel filters the client's existing local ledger to that exact host and session for the last 24 hours in UTC, with a 15-minute freshness threshold. It keeps account and money detail hidden. Use arrows or **j/k** to scroll, **r** to reread, and **Escape**, **q** or **u** to return.

For sessions reached through an entry, this remains a local ledger view. It grants no remote account access and makes no native RPC or model request. Use the CLI's explicit filters and detail flags when you need a broader local review.

[View the captured 80-column panel](../site/evidence/usage-6a6b20c/usage-80x24.png). This is an actual owned PTY capture with illustrative inventory and one manual observation, rasterized from terminal cells. [Three-size capture receipt](../site/evidence/usage-6a6b20c/receipt.json) records exact source, executable digest, cleanup and zero model activity.

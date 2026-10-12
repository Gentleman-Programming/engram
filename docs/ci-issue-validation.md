# Read-only linked-issue validation

**This evaluator slice is inactive on its own.** It adds a metadata-only module,
not a workflow, publisher, new required gate, or administrator setting. Existing
PR Validation remains unchanged: linked-issue label events do not refresh its
legacy checks by themselves. Integration and live rollout proof belong to later
serial, stacked-to-main slices.

## Verify this slice

Run from the repository root:

```sh
node --test .github/scripts/issue-validation.test.mjs
node --check .github/scripts/issue-validation.mjs
```

The public-interface tests use deterministic Octokit fakes and request journals.
They prove routing, explicit SHA targets, conclusions, API error handling and
absence of check writes in the evaluator. They do **not** prove event delivery,
GITHUB_TOKEN Checks API capability, GitHub Actions app identity `15368`, required
context recognition, fork execution, duplicate/legacy check selection or live
approval/revocation behavior. Observe those before claiming rollout compatibility;
retain legacy writers until that evidence supports retirement.

## Module contract

`.github/scripts/issue-validation.mjs` exports
`evaluateIssueValidation(github, context)`. Supply an Octokit-compatible client
and the actions/github-script context (`eventName`, `payload`, `repo`). It returns:

```js
[{ headSha, reference: { conclusion, summary }, approved: { conclusion, summary } }]
```

Conclusions are `success` or `failure`. An empty array means no check targets.
The caller owns error reporting and publication of the existing
**Check Issue Reference** and **Check Issue Has status:approved** names. This
module does not import a publisher, write checks, run shell commands, check out
code or acquire credentials. Untrusted bodies and labels are only metadata.

| Event | Read-only behavior |
|-------|--------------------|
| `pull_request_target` / `pull_request` | Opening, edits, labels, synchronization and reopening fetch the current PR, validate issues, then recheck PR metadata. Closed PRs are ignored. Event body/head fields are never validation evidence. |
| `issues` | Only `labeled` / `unlabeled` for `status:approved` on an issue, not a PR. Paginate all open PR numbers, then fetch authoritative bodies/heads; only matching closing references become targets. |
| `merge_group` | Only `checks_requested`; use the unchanged merge-queue resolver's full prefix through the tail PR, validating every constituent PR. Return one result on the explicit group `head_sha`, never a constituent head or inferred event SHA. |
| Other inputs | Unrelated actions, labels and invalid routing identifiers produce no requests or targets. |

Closing references retain exactly `/(?:closes|fixes|resolves)\s+#(\d+)/gi`,
including its existing substring and whitespace behavior. Every match is checked,
including duplicates; all linked issues must carry `status:approved`. Missing
references fail both conclusions for ordinary PRs and queue constituents. For
issue-label routing, no matching authoritative reference means no target.

## Failure and consistency boundaries

- Issue read failures or malformed labels fail approval explicitly. They never
  become success. As in the legacy policy, an `issues.get` record's
  `pull_request` field does not disqualify valid approval labels; issue-as-PR
  events remain excluded from issue-label routing.
- PR head/body/state changes at the final read fail both conclusions on the
  previously fetched SHA, requesting fresh reconciliation. The evaluator does
  not chase changing heads indefinitely or claim approval for a closed PR.
- A known target with unavailable recheck evidence receives failure. Merge queue
  resolution errors fail the explicit group target. If initial PR discovery or
  target resolution cannot complete, the evaluator throws; callers must report
  that error rather than fabricate success or silently treat it as no targets.
- Current labels, not the event's historical label state, decide approval. Thus a
  delayed approval event can observe revocation and vice versa. There are no
  locks, cancellation guarantees or compare-and-swap publication fences.

Refresh is eventual, not atomic: labels or metadata may change after the last
read, before publication, or while another run completes. Scheduling, event
ordering and API outages have no promised maximum delay. An existing check may
remain visible during errors until a successful reconciliation or documented
manual recovery. Legacy jobs can finish or be rerun later; their selection and
transition behavior require live verification. Refreshing a PR head does not
atomically invalidate an already evaluated merge group, and the queue itself can
change after resolution.

This slice needs only `pull-requests: read` and `issues: read` API access (plus
trusted-code loading access if required by its future caller). It requests no
permissions itself. Checks write scope belongs exclusively to the separate
publisher; no statuses/actions/issue/PR writes, PAT, new app or administrative
changes are justified here. A token capability blocker must be reported, not
worked around with broader credentials.

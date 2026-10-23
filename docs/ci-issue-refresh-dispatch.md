# Inactive issue approval refresh dispatcher

`.github/scripts/issue-refresh-dispatch.mjs` exports
`dispatchIssueApprovalRefresh(github, context, trustedSha)`. This preparation
unit adds no workflow or trigger: integration alone does not activate routing.
The trusted caller supplies the repository context and immutable policy SHA.

## Discovery and dispatch contract

Only genuine `issues` events for `labeled`/`unlabeled` on `status:approved`
are eligible. Other events, other labels, malformed issue numbers and
PR-as-issue payloads return `[]` without requests. Both approval addition
and removal request reevaluation; neither event payload asserts current approval.

The dispatcher lists every open PR page (100 per page, until a short page),
rejecting malformed or repeated positive PR numbers. It fetches every listed
PR's current record before dispatching anything. Listing bodies are never
proof. Current closed PRs and bodies without a matching closing reference
are excluded using the unchanged `closingIssueNumbers` parser. Multiple
references, including repeated references, result in at most one attempt per PR.
Discovery/API failures throw before any dispatch.

For each affected current open PR, the unchanged
[`rerunNativeIssuePolicy` helper](ci-issue-refresh-router.md) owns all
head/base/repository, original-event/run/job, trusted-source and final-target
admission. It alone may POST the admitted native metadata run's rerun endpoint.
There is no check/status publication, issue/PR mutation, broad CI wakeup,
retry, older-run fallback or substitute credentials in this dispatcher.

Success returns the helper results array; no affected PRs returns `[]`.
A helper failure does not prevent attempts for other affected PRs. After all
attempts, any failure throws an `AggregateError` with `results` (acknowledged
reruns) and `failures` (`{ pullNumber, error }` entries). Callers must not
report that error as success or silently discard it. A rejected or unavailable
target never contributes a fabricated successful result.

## Consistency and evidence limits

Dispatch is non-atomic: an earlier rerun may already be requested when a later
target fails, and requests cannot be rolled back. A successful request is not
a completed evaluation or approval verdict. Concurrent events, listing/current
metadata drift, head changes after the final read, scheduling and API delays
leave eventual-consistency windows. Newly opened PRs during discovery may need
a subsequent event/reconciliation; no snapshot or maximum delay is promised.
Existing visible checks may remain after failure. Already evaluated merge groups
are not atomically invalidated. Legacy, bootstrap, native and queue behavior,
the six required contexts and provider binding remain unchanged.

Focused public SDK regressions exercise the real helper through `github.request`:

```bash
node --test .github/scripts/issue-refresh-dispatch.test.mjs
node --check .github/scripts/issue-refresh-dispatch.mjs
node --check .github/scripts/issue-refresh-dispatch.test.mjs
```

These deterministic mocks are not live token-capability, event delivery,
required-gate blocking/recovery, duplicate-selection, fork or queue proof.
Workflow activation and serial delivery remain separate parent-owned gates;
this unit neither retires legacy writers nor authorizes probes/settings changes.

# Native issue-policy gating — integrated foundation, candidate routing

## Status

The maintainer approved the [#1802 scope amendment](https://github.com/Gentleman-Programming/engram/issues/1802#issuecomment-6135740826); the issue body records the decision. [#1834](https://github.com/Gentleman-Programming/engram/issues/1834) owns native metadata jobs and [#1835](https://github.com/Gentleman-Programming/engram/issues/1835) owns the selective router.

#1834 / PR #1838 integrated `.github/workflows/issue-policy.yml` and the read-only
`evaluateNativeIssuePolicy(github, context, policy)` adapter on trusted main.
PR #1843 integrated the inactive guarded rerun helper at `fc438498`.
PR #1850 integrated #1844's inactive dispatcher on verified main at `f14fc4f3`.
PR #1855 integrated #1835's issue-label routing at `47911e25`.
The original-event marker correction requires delivery to trusted code and a
new covered PR event; opening its introducing PR cannot activate that correction. The existing API-created publisher is transitional. Publication success did not demonstrate approval revocation blocking in the controlled fork probe; no required-gate or queue acceptance is claimed.

## Permission boundary

The approved replacement uses trusted `pull_request_target` metadata jobs with the existing **Check Issue Reference** and **Check Issue Has status:approved** names. Native job conclusions determine policy results, bound to the original run's PR head and current authoritative metadata. These jobs request only policy/metadata read permissions.

Only the candidate issue-label router job requests `actions: write`, never globally. The token capability covers repository Actions; trusted code must restrict usage to an eligible allowlisted metadata workflow, matching current PR head and trusted policy revision. Missing evidence or token capability is an explicit blocker, not permission for broad CI or an old-publisher fallback.

No fork/PR-head code, PAT, new app/service, issue/PR approval, settings changes, new required contexts or automatic merging is authorized.

## Native contract and local verification

The two independent native jobs select `reference` and `approved` verdicts from
current PR/issue metadata. All six PR actions are covered. Both check out only
`github.workflow_sha`, without persisted credentials; the exact checkout step
name exposes that source SHA for later router admission. Before evaluation,
each job also emits a successful constant no-op step named `Original issue event v1`
plus a typed JSON tuple from the original event and run/attempt context.
The [router contract](ci-issue-refresh-router.md) defines its exact fields and
required trusted-source validation. String fields use `toJSON` only in step
metadata, never shell or script source. Empty API PR associations cannot replace
this proof, and legacy runs without markers remain ineligible.
No retries, dependency
skipping, merge-group subscription or API-created checks are used. A failed
verdict or metadata/target error calls `core.setFailed` in the actual job wrapper.

The original payload PR head must equal the evaluator target; neither
`context.sha` nor a base SHA substitutes for it. Missing/ambiguous targets and
head drift fail closed. Current body and every closing-reference issue label
are read, with a final metadata recheck. The additive `closingIssueNumbers`
export preserves the original regex, duplicates, case and whitespace behavior;
`evaluateIssueValidation` retains its existing output and fetched-head contract.
For example, `FIXES\n#9; resolves #10` requires both issues approved, while
`closes#9` is not a closing reference. Reference success is independent of
approval failure; no failed prerequisite silently skips the approval job.

```bash
node --test .github/scripts/issue-native-policy.test.mjs .github/scripts/issue-native-workflow.test.mjs .github/scripts/issue-validation.test.mjs
node --check .github/scripts/issue-native-policy.mjs
node --check .github/scripts/issue-validation.mjs
```

These are SDK-mock and composed actual-script-body tests, not live token,
provider, fork, duplicate-selection or required-gate evidence. The bootstrap
publisher and original PR/queue validation remain unchanged.

## Rollout boundary

Keep legacy writers, all six required contexts, integration `15368` and native merge-group validation intact. Deliver implementation slices serially to verified `main`, with tests and guidance alongside each behavior change.

[#1810](https://github.com/Gentleman-Programming/engram/issues/1810) retirement remains blocked until live blocking, recovery, duplicate selection and queue compatibility are observed. #1802 stays open and its acceptance criteria stay unchecked until their evidence exists.

Refresh is eventual: event delivery, scheduling, API availability, metadata races and already evaluated merge groups leave documented windows. There is no fixed delay, atomic invalidation or strict no-stale-success guarantee. Discovery completes before dispatch, but per-target reruns are not transactional: an explicit later failure does not undo earlier requests. Missing eligible original-event identity proof or GITHUB_TOKEN capability blocks refresh; no broader credentials or fallback is allowed.

T4 live evidence requires fresh probe consent; local tests and integration are not live capability, duplicate-selection or queue proof. The earlier #1828 probe allowances are exhausted. Another probe mutation or fixture cleanup requires separate authorization. See the [existing bootstrap and recovery guide](ci-issue-refresh-rollout.md) for historical publication evidence and its limitations; do not treat local mocks as live compatibility proof.

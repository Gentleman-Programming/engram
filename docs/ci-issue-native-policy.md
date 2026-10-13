# Native issue-policy gating — approved scope

## Status

The maintainer approved the [#1802 scope amendment](https://github.com/Gentleman-Programming/engram/issues/1802#issuecomment-6135740826); the issue body records the decision. [#1834](https://github.com/Gentleman-Programming/engram/issues/1834) owns native metadata jobs and [#1835](https://github.com/Gentleman-Programming/engram/issues/1835) owns the selective router.

This page records authorization, not implemented native gating or automatic rerun capability. The existing API-created publisher is transitional. Publication success did not demonstrate approval revocation blocking in the controlled fork probe; no required-gate or queue acceptance is claimed.

## Permission boundary

The approved replacement uses trusted `pull_request_target` metadata jobs with the existing **Check Issue Reference** and **Check Issue Has status:approved** names. Native job conclusions determine policy results, bound to the original run's PR head and current authoritative metadata. These jobs request only policy/metadata read permissions.

Only the future issue-label router job may request `actions: write`, never globally. The token capability covers repository Actions; trusted code must restrict usage to an eligible allowlisted metadata workflow, matching current PR head and trusted policy revision. Missing evidence or token capability is an explicit blocker, not permission for broad CI or an old-publisher fallback.

No fork/PR-head code, PAT, new app/service, issue/PR approval, settings changes, new required contexts or automatic merging is authorized.

## Rollout boundary

Keep legacy writers, all six required contexts, integration `15368` and native merge-group validation intact. Deliver implementation slices serially to verified `main`, with tests and guidance alongside each behavior change.

[#1810](https://github.com/Gentleman-Programming/engram/issues/1810) retirement remains blocked until live blocking, recovery, duplicate selection and queue compatibility are observed. #1802 stays open and its acceptance criteria stay unchecked until their evidence exists.

Refresh is eventual: event delivery, scheduling, API availability, metadata races and already evaluated merge groups leave documented windows. There is no fixed delay, atomic invalidation or strict no-stale-success guarantee.

The earlier #1828 probe allowances are exhausted. Another probe mutation or fixture cleanup requires separate authorization. See the [existing bootstrap and recovery guide](ci-issue-refresh-rollout.md) for historical publication evidence and its limitations; do not treat local mocks as live compatibility proof.

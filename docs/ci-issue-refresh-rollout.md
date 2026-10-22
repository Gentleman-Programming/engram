# Legacy issue-writer retirement — draft candidate only

## Status: NO-GO for integration

This #1810 candidate is not an activated retirement on trusted `main`.
The [canonical #1810 evidence matrix](https://github.com/Gentleman-Programming/engram/issues/1810)
records the live evidence and acceptance boundary. Sequential and manual positives
are bounded; rapid burst is **PARTIAL**, including two failed routers whose
underlying causes were not exposed by aggregate errors. Local tests do not prove
hosted event selection, token capability, provider identity or required-gate behavior.
No new probes, diagnostic instrumentation, settings changes or acceptance amendments
are authorized by this draft.

Four premerge gaps remain:

1. Late-legacy required-check selection without a safe publication boundary.
2. In-flight/superseded-head reconciliation evidence.
3. Complete rapid-event admission and ordering evidence.
4. Post-merge-group approval-revocation evidence.

Do not merge/enqueue this candidate until the canonical prerequisites are met.
The six required contexts, GitHub Actions App15368 binding and existing queue
settings are unchanged; #1802 remains open. Preparing the draft is not live proof.

## Candidate event separation (when integrated)

| Event/path | Candidate responsibility |
|------------|--------------------------|
| Native `issue-policy.yml` / `pull_request_target` | Two read-only, head-bound issue-policy jobs on all six existing PR actions |
| Existing `pr-check.yml` / `merge_group` | Original workflow identity and complete unchanged jobs: Claude version plus both queue issue jobs and resolver/aggregation |
| New `pr-claude-plugin-version.yml` / `pull_request` | Original six PR actions, permissions and verbatim Claude version job only |
| `issue-refresh-router.yml` / approval-label issue events | Selectively request eligible native metadata reruns |
| Transitional `issue-validation.yml` | Removed by this candidate; no API-publisher fallback |

The old `pr-check.yml` PR trigger is removed, not replaced with skipped issue
jobs. The new Claude workflow has no merge-group subscription. The native policy,
router, evaluator, dispatcher, queue resolver and broad CI are unchanged.
Dormant `issue-check-publisher.mjs` and its helper tests remain for historical
contract coverage; `issue-validation.mjs` is still the native evaluator/parser.

The historical bootstrap used trusted base/default-branch checkout, metadata-only
reads and `checks: write` to create checks. Publication alone did not establish
required-gate enforcement. Its workflow is retired in this candidate, not a
recovery option. Its preserved helper tests do not imply an active publisher.

## Cutover and reconciliation windows

Removing a workflow or trigger does **not** cancel old in-flight or queued runs,
or prevent historical reruns from executing their old workflow code. Late legacy
writes can still overlap native checks. Observe current-head check selection and
reconcile through the eligible native metadata route; never substitute the API
publisher, original broad PR Validation or broad CI.

Delivery, runner scheduling and API availability have no fixed maximum delay.
Metadata can change after reads, and concurrent events can complete out of order.
There is no atomic cancellation/invalidation, automatic retry or strict
no-stale-success guarantee. Failed reconciliation can leave visible old success.
A later successful evaluation must be inspected rather than assumed.
PR recovery does not atomically revalidate an already evaluated merge group;
inspect its own head and queue checks separately.

## Trusted manual recovery only

Rerun an eligible **Issue Approval Refresh Router** metadata-only run to rediscover
currently affected PRs. Its original trusted revision must contain the dispatcher
and helpers. Every native target must pass the
[router admission contract](ci-issue-refresh-router.md), including the typed
original-event tuple, matching PR/head and verified trusted source.
API `pull_requests: []` is admissible only with that independent proof;
contradictory or ambiguous associations block.

Inspect both exact native policy job conclusions on each current head after
completion. A requested rerun is not completed recovery or required-gate proof.
Partial target failure does not undo earlier requests. There is no automatic
retry, older-run fallback or manual dispatch entry point.

Historical reruns retain their original workflow revision: they cannot acquire
missing helpers or original-event markers. If no eligible native target or token
capability exists, stop and report the missing evidence; do not broaden credentials
or fall back to an API publisher/broad CI. A new normal covered PR event may be
needed after trusted delivery; that is not authorization for a new live fixture,
probe mutation or cleanup.

## Local evidence boundary

Run the issue-policy helper tests, including `issue-writer-retirement.test.mjs`,
and the existing merge-queue/Claude helper suites. Structural tests pin unchanged
jobs and native/CI source contracts without reading Git history. An ephemeral
base comparison verifies that only the legacy PR trigger was removed and that
the PR-only Claude job is copied verbatim. Production JavaScript helpers are
unchanged, so a CLI behavior differential is not applicable.

These checks do not exercise Actions, live gate selection, token permissions or
race ordering. Normal hosted CI and independent review remain separate evidence;
all live acceptance and merge prerequisites stay on the canonical #1810 matrix.
See also the [native-policy boundary](ci-issue-native-policy.md).

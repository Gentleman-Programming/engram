# Roll out trusted issue-check refresh

**Status: integrated bootstrap/native foundations; native router activation candidate,
not live rollout proof.** PR #1823 integrated the bootstrap, #1838 the native
jobs/adapter, and #1843 the inactive guarded rerun helper. PR #1850 integrated
#1844's inactive dispatcher on verified main at `f14fc4f3`; the #1835 workflow
remains an activation candidate. Native issue-label
routing is not yet available on trusted main; an introducing PR cannot activate it.
Keep original PR Validation unchanged. Local tests and integration do not prove
token capability, App15368 identity, gate selection, forks or queue compatibility.

## Native replacement candidate

The [native-policy foundation](ci-issue-native-policy.md) now defines the #1834
integrated read-only jobs and head-bound adapter; their presence is not live proof.
The earlier controlled fork probe published failures without proving required-gate
revocation blocking. Keep this bootstrap and legacy/queue validation unchanged;
native job definitions and local mocks do not repair or prove live gate selection.
The selective router is an activation candidate. #1802 remains open, #1810 remains blocked,
and fresh probe/cleanup admission is required; no new live budget is granted.

## Quick path for maintainers

1. Evaluator #1807 / PR #1812 is integrated on verified `main` (`871f99e3`).
2. Publisher #1808 / PR #1819 is integrated on verified `main` (`4197117c`).
   These inactive foundations do not establish live token/provider compatibility.
3. Bootstrap #1809 / PR #1823 and native #1834 / PR #1838 are integrated.
   PR #1843's helper remains inactive without the issue-event caller. PR #1850
   integrated #1844's inactive dispatcher on verified main at `f14fc4f3`.
   The #1835 workflow still needs separate delivery and activation.
   Retain legacy issue jobs, six contexts/App15368 and the native queue.
4. Obtain fresh probe consent and collect the evidence below under #1802 before
   #1810 writer retirement. Implementation/CI does not close live acceptance;
   #1802 stays open. Native merge-group jobs/resolver remain intact after retirement.

CI preserves the exact original four-suite policy-test command and adds a
separate step discovering available `.github/scripts/issue-*.test.mjs` files.
The first slice therefore does not need unpublished publisher tests.
The new workflow cannot be exercised by merely opening its introducing PR:
`pull_request_target` loads the trusted base workflow, while issue events require
the workflow on the default branch. Trusted revisions predating the helpers
cannot supply them; missing imports fail the refresh rather than executing PR code.

## Historical bootstrap publication and permissions

The runner checks out `github.workflow_sha` from the repository hosting the
workflow, with credentials persistence disabled. Trust comes from the restricted
triggers: `issues` uses default-branch code and `pull_request_target` uses base-
branch code. `workflow_sha` identifies the executing workflow commit; it is not
a universal trust marker. A merge-group event can select a group-origin workflow
definition, so this checks-write publisher does not subscribe to `merge_group`.
Native PR Validation merge-group jobs and their resolver remain unchanged.
There is no PR-head checkout or candidate fallback. Metadata stays API data;
no issue titles, bodies or labels become shell commands.

| Scope | Sole purpose |
|-------|--------------|
| `checks: write` | Create the two existing issue-policy check names on explicit evaluated heads |
| `pull-requests: read` | Resolve open PRs and authoritative current bodies/head SHAs |
| `issues: read` | Read current labels for every closing-reference issue |
| `contents: read` | Load trusted repository helpers |

Global permissions are empty. No PAT, new app/service, `actions: write`,
`statuses: write`, issue/PR writes, ruleset changes or queue mutations are used.
Capability denial or a returned check not bound to GitHub Actions integration
`15368` and the requested head/name/result is an explicit workflow failure;
stop and report it, never obtain larger credentials.

Each evaluation creates two completed check runs with summaries. It never
updates a legacy job's run. Approval failure is published as a failed check,
not a neutral/skipped result. The entire batch is validated before any writes;
an empty batch produces no requests. Writes are not transactional: a later API
failure can leave earlier checks published. Returned identity can only be
verified after creation; detecting a mismatch does not undo that write.

## Candidate native routing and manual recovery

The [native router](ci-issue-refresh-router.md) uses only genuine approval-label
issue events and trusted default-branch code. Root permissions are empty; only
its router job requests contents/issues/pull-requests read and `actions: write`.
This is repository Actions capability, restricted by helper admission rather
than workflow-granular permissions. No settings change or broader credentials
are authorized. Complete discovery precedes dispatch; aggregate target errors
fail explicitly even when earlier native reruns were requested. No retries,
older-run fallback, check publication or broad CI reruns occur in this path.

After activation, rerun an eligible **Issue Approval Refresh Router** run to
rediscover affected current PRs. Its original trusted revision must contain all
helpers; every native target still needs original PR/run/source admission.
Inspect both exact native policy jobs on each current head after completion.
A requested rerun is not success, recovery or gate proof. Missing native PR
association/capability blocks recovery: stop and report, never substitute the
bootstrap publisher, an older run or larger credentials. A rerun does not upgrade
workflow code; no `workflow_dispatch` entry point exists. These are recovery
instructions, not authorization to mutate another probe or the retained fixture.

## Consistency and legacy-writer windows

- Event delivery, runner scheduling and API availability delay reconciliation;
  there is no fixed maximum delay. Unrelated issue labels do not request refresh.
- Current metadata is evaluated, but labels or heads can change after the last
  read. Concurrent refreshes can complete out of order. No atomic invalidation
  or strict no-stale-success guarantee is made; later successful reconciliation
  is required. Cancellation is not used to discard events.
- A fetch/validation/publication error never fabricates approval success. An
  already visible success may remain until reconciliation or manual recovery.
- Legacy PR Validation can finish or be rerun after the new publisher. During
  overlap, duplicate names from the same provider may affect gate selection;
  observe actual required-gate behavior instead of assuming newest-run selection.
  After late legacy completion, reconcile again with the trusted publisher and
  inspect both named checks on the current head.
- Refreshing a PR head does not atomically revalidate an already evaluated merge
  group. The new publisher does not run on merge-group events. Unchanged native
  PR Validation handles `checks_requested`; retain those jobs and the resolver
  in later PR-writer retirement too. Record queue checks separately.

## Historical bootstrap-only recovery (not native gate recovery)

After this workflow is live, use the Actions UI to locate an **Issue Validation
Refresh** run for the affected PR or a `status:approved` issue-label event. Check
its workflow revision is trusted and contains both helpers, then **Re-run all
jobs** for that metadata-only workflow. This reads current metadata again rather
than reusing stale approval conclusions. An issue-event rerun can reconcile all
currently affected open PRs. Do not rerun original **PR Validation** or broad CI
as an approval-refresh recovery; it includes unrelated work and legacy writers.

Inspect both exact check names on the PR's current head and verify current issue
labels, conclusions and App15368 binding. If the PR head changed during recovery,
reconcile again; if a legacy writer finishes later, repeat trusted reconciliation.
For a queue group, inspect its own head/checks as well; PR recovery is not queue
invalidation. If no eligible trusted run exists, report missing recovery evidence
and wait for a normal covered event; there is no `workflow_dispatch` entry point.
A rerun retains its original trusted workflow revision: it does not upgrade code.
If that revision lacks the helpers or capability, stop and report the blocker.

## Evidence required before legacy retirement

All replacement acceptance items remain unproved. The historical fork probe
published approval failure while the gate stayed CLEAN; publication is not
enforcement. Prior #1828 mutation allowances are spent; fresh probe/cleanup
consent is required and PR #1828 must not be merged/enqueued. Record run URLs,
trusted revisions/attempts, original PR associations, current heads/labels,
job/check IDs/App15368 and actual gate/queue outcomes for each case.

- [ ] Router GITHUB_TOKEN can rerun admitted native metadata runs; their two
      native jobs/checks retain original PR/head binding and App15368 identity.
- [ ] Existing six required contexts recognize both exact names/provider, including
      duplicate selection and late legacy completion/rerun followed by recovery.
- [ ] Approval and revocation reconcile affected open PRs; unrelated labels and
      unmatched references produce no policy writes.
- [ ] Fork PRs and normal opened/edited/labeled/unlabeled/synchronize/reopened
      events run only trusted code and bind checks to the current fetched head.
- [ ] Multiple references retain the exact closing regex and all-issues approval
      rule; rapid approve/revoke and changed-head cases document observed windows.
- [ ] Unchanged native merge-group `checks_requested` validation retains required-
      gate and queue compatibility; PR refresh does not invalidate a queue group.
- [ ] API/capability failures stay explicit; trusted manual recovery is observed.

Run the deterministic candidate checks with:

```bash
node --test .github/scripts/issue-check-publisher.test.mjs .github/scripts/issue-validation-workflow.test.mjs .github/scripts/issue-validation-composed.test.mjs
node --check .github/scripts/issue-check-publisher.mjs
node --test .github/scripts/issue-refresh-router.test.mjs .github/scripts/issue-refresh-dispatch.test.mjs .github/scripts/issue-refresh-router-workflow.test.mjs
node --check .github/scripts/issue-refresh-dispatch.mjs
```

These verify the public publisher contract, actual script-body composition with
fake APIs, and workflow structure, not live
GitHub behavior. Independent seam verification and CI belong to the parent;
rollout evidence and any later retirement require maintainer observation.

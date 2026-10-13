# Native issue-policy refresh router — activation candidate

PR #1838 integrated the native workflow/adapter at `2dfdca97`; PR #1843
integrated the inactive guarded rerun helper at `fc438498`. PR #1850 integrated
#1844's [inactive dispatcher](ci-issue-refresh-dispatch.md) on verified main at
`f14fc4f3`. The #1835 branch prepares workflow activation, not yet available on
trusted main. Its dispatcher prerequisite is integrated; opening the workflow
PR cannot activate issue events. Integration and local tests are not live required-gate proof;
#1802 stays open and #1810 retirement remains blocked.
Public runtime evidence: **pending fresh consented live acceptance**.

## Caller and public SDK contract

`rerunNativeIssuePolicy(github, repo, pullRequest, trustedSha)` accepts an Octokit
public `request` client, `{owner, repo}`, an affected open PR snapshot, and the
caller's immutable trusted upstream policy SHA. The caller must establish the
closing-reference match and trusted provenance; the helper never parses bodies
or discovers issue events. PR number/state/body/head SHA/ref/fork repository and
base ref/repository must be present and match two authoritative PR GETs. Both
head and base repository IDs must also be present and remain unchanged.

Reads use the upstream repository's REST endpoints: PR GET, workflow GET for
`issue-policy.yml`, paginated workflow runs filtered by event/branch/head SHA,
run GET, latest-attempt jobs GET, compare, and contents GET for both source and
trusted revisions. Admission requires `.github/workflows/issue-policy.yml`,
`pull_request_target`, exact run head/ref/upstream/fork binding and exactly
`Check Issue Reference` and `Check Issue Has status:approved`. Job heads must
match. Both jobs must expose a successful completed step named exactly
`Load trusted issue policy (<40 lowercase hex SHA>)` with the same resolved SHA.
Both the listed run and its authoritative GET must expose exactly one
`run.pull_requests` entry matching the intended PR number, head SHA/ref/repository
ID and base ref/repository ID. Empty, missing or ambiguous associations block
with `Issue refresh blocked: native run PR/event binding unavailable`; no ownership
is inferred from shared fork/head/SHA or current PR reads. Even if GitHub omits
this association, the helper must not rerun a potentially different original event.
The attempt-specific jobs endpoint supplies attempt binding; no undocumented
job attempt field or check-suite workflow association is used.

The source must be an upstream ancestor of `trustedSha`. Contents/blob bindings
must match at both revisions for `issue-policy.yml`, `issue-native-policy.mjs`
and `issue-validation.mjs` under their workflow/scripts directories. Unrelated
main changes are not frozen. The newest matching run is admitted or blocked;
there is no older-policy or older-run fallback when the newest run cannot prove
its target PR/event identity, including shared-head PRs targeting different bases. Only completed runs under 30 days old with
fewer than 50 attempts are admitted. API rejection remains explicit, with no retry.

The only write is one `POST /repos/{owner}/{repo}/actions/runs/{run_id}/rerun`.
A 201 returns `{status: 'requested', runId, headSha, sourceSha, trustedSha, attempt}`;
this acknowledges a queued request, not completed jobs, gate enforcement or live
capability. All missing/malformed/drift/API evidence throws `Issue refresh blocked:`.
No checks, statuses, issue/PR writes, broad CI, fallback token or automatic retry.
The candidate workflow requests job-local `actions: write`; that repository-wide
capability is constrained by trusted code, not workflow-granular permissions.
Requesting it is not proof of GITHUB_TOKEN capability or authorization for settings.

[GitHub rerun semantics](https://docs.github.com/en/actions/managing-workflow-runs-and-deployments/managing-workflow-runs/re-running-workflows-and-jobs)
retain the original actor's privileges, event, GITHUB_SHA and GITHUB_REF; a rerun
is not a new issue event. Scheduling/API delays and metadata changes after the
final read remain eventual-consistency windows. Existing or late legacy checks
and already evaluated merge groups remain unchanged; no atomic invalidation or
live recovery is claimed.

## Trusted issue-event workflow and recovery

`.github/workflows/issue-refresh-router.yml` subscribes only to `issues`
`labeled`/`unlabeled`. Its sole job filters `status:approved` and rejects
issue-as-PR payloads. Both addition and removal request reevaluation; payload
labels are not an approval verdict. The dispatcher completes paginated discovery
and current PR reads before requesting any admitted native rerun, using the
unchanged closing-reference parser. Unrelated events/PRs cause no writes.

Root permissions are `{}`. Only `router` requests contents/issues/pull-requests
read and `actions: write`. Pinned checkout loads `github.workflow_sha` with
credentials persistence disabled; issue/default-branch provenance makes that
revision trusted, not the SHA field alone. Pinned github-script calls only
`dispatchIssueApprovalRefresh(github, context, process.env.TRUSTED_SHA)`.
There is no PR-head checkout, untrusted-field script interpolation, API-created
check, broader credentials, cancellation or automatic retry (`retries: 0`).
The wrapper reports any error with `core.setFailed`; partial target success
cannot turn aggregate failure into workflow success or roll back earlier requests.

After activation, an eligible **Issue Approval Refresh Router** run can be
rerun from the Actions UI to rediscover currently affected PRs. Verify its
original trusted revision contains the dispatcher/helper and the admitted native
run targets each current PR. Inspect both exact native policy job results on the
current head afterwards; a queued rerun is not completed evaluation. A router
rerun keeps its original revision and cannot upgrade missing helpers or capability.
No eligible original native-run PR association or token capability means stop and
report, never broad PR Validation/CI, an older run or bootstrap-publisher fallback.
There is no manual dispatch trigger. Newly opened PRs, late legacy completion,
metadata races and already evaluated queue groups remain reconciliation windows;
no fixed delay or atomic invalidation is promised. See the
[rollout boundary and recovery evidence](ci-issue-refresh-rollout.md).

## Local evidence

```bash
node --test .github/scripts/issue-refresh-router.test.mjs .github/scripts/issue-refresh-dispatch.test.mjs .github/scripts/issue-refresh-router-workflow.test.mjs
node --check .github/scripts/issue-refresh-router.mjs
node --check .github/scripts/issue-refresh-dispatch.mjs
```

Structural tests pin event/permission/trusted-call boundaries and normalized
integrated baseline hashes without requiring Git history; they run in shallow
checkouts or archives. They neither execute Actions nor establish live capability.
Public SDK mocks assert exact request parameters, both PR reads, one allowed POST
and zero other writes, including valid fork associations, rejected empty/ambiguous
or other-PR/base associations, authoritative run drift, binding drift, missing
runs, checkout/source/job shape, stale policy and API errors. These tests never
use real tokens or network. Provider identity, duplicate selection, live approval
and revocation, queue compatibility and token capability need later rollout proof.

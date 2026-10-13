# Inactive native issue-policy rerun foundation

This inactive helper is the #1835 foundation slice, delivered separately from activation.
It is not an activated issue-label router or completion of #1802's rollout.
N4 owns issue discovery using the shared closing-reference parser and workflow
activation, only after #1834 is integrated into verified main. No workflow,
permissions, token, required context, queue rule or legacy writer is changed here.
The native workflow and adapter were integrated by #1838 at `2dfdca97`; their
presence on main does not establish live required-gate enforcement.
Public runtime evidence: **N/A — inactive helper**.

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
Future activation needs job-local `actions: write`; requesting that permission
is not proof of available GITHUB_TOKEN capability or authorization for settings.

[GitHub rerun semantics](https://docs.github.com/en/actions/managing-workflow-runs-and-deployments/managing-workflow-runs/re-running-workflows-and-jobs)
retain the original actor's privileges, event, GITHUB_SHA and GITHUB_REF; a rerun
is not a new issue event. Scheduling/API delays and metadata changes after the
final read remain eventual-consistency windows. Existing or late legacy checks
and already evaluated merge groups remain unchanged; no atomic invalidation or
live recovery is claimed.

## Local evidence

Run `node --test .github/scripts/issue-refresh-router.test.mjs` and
`node --check .github/scripts/issue-refresh-router.mjs`.
Public SDK mocks assert exact request parameters, both PR reads, one allowed POST
and zero other writes, including valid fork associations, rejected empty/ambiguous
or other-PR/base associations, authoritative run drift, binding drift, missing
runs, checkout/source/job shape, stale policy and API errors. These tests never
use real tokens or network. Provider identity, duplicate selection, live approval
and revocation, queue compatibility and token capability need later rollout proof.

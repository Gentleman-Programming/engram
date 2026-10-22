# Native issue-policy refresh router

PR #1838 integrated the native workflow/adapter at `2dfdca97`; PR #1843
integrated the inactive guarded rerun helper at `fc438498`. PR #1850 integrated
#1844's [inactive dispatcher](ci-issue-refresh-dispatch.md) on verified main at
`f14fc4f3`. PR #1855 integrated #1835's issue-event activation at `47911e25`.
The original-event identity correction below still requires delivery to trusted
code and a new covered PR event before it can provide live evidence.
Integration and local tests are not live required-gate proof;
#1802 stays open and #1810 retirement remains blocked.
Public runtime evidence is bounded by the
[canonical #1810 matrix](https://github.com/Gentleman-Programming/engram/issues/1810):
sequential/manual positives are bounded, rapid burst is **PARTIAL** (two failed
routers; aggregate errors did not expose underlying causes). This draft remains
**NO-GO**, not active retirement on main. Four premerge gaps remain: late-legacy
selection without a safe publication boundary, in-flight/superseded heads,
complete rapid-event admission/ordering, and post-merge-group revocation.
No new experiments or diagnostic changes are authorized.

When integrated, the #1810 candidate removes the transitional API publisher and
only the PR trigger from `pr-check.yml`. That original workflow keeps its complete
merge-group jobs, resolver/aggregation and Claude guard. A new PR-only
`pr-claude-plugin-version.yml` copies the original six actions, permissions and
Claude job with no merge-group trigger. Native PR jobs/router/helpers, six required
contexts, App15368 and queue settings remain unchanged; dormant publisher helpers
are retained, not a fallback.

Removal does not cancel old in-flight/queued runs or historical reruns using old
code. Late legacy writes still require current-head inspection and eligible native
metadata-router reconciliation only, never broad CI or API publication. There is
no fixed latency, atomic cancellation or automatic retry.

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
Both jobs must also expose exactly one successful, completed step named
`Original issue event v1 <JSON array>`, before policy evaluation so a failed
approval verdict does not suppress the identity proof. The ordered tuple is:

```text
[event, action, repositoryId, runId, attempt, prNumber,
 headRepositoryId, headSha, headRef, baseRepositoryId, baseRef]
```

IDs, PR number and attempt are JSON numbers; the remaining fields are strings.
Trusted workflow expressions read the original `github.event` and runner
context, using `toJSON` for string escaping. The step executes only a constant
no-op, not event data. Both markers must agree and match the intended current
PR, repository IDs and authoritative run/attempt; only the workflow's six PR
actions are accepted. Missing, malformed, duplicated, unsuccessful or mismatched
markers block with `Issue refresh blocked: native original PR/event binding unavailable`.
The protected workflow/source checks below are mandatory before any rerun; a
matching JSON string alone is not trusted provenance.

Both the listed run and authoritative GET must have a `pull_requests` array.
An empty array is permitted only with the independent original-event proof
above. A nonempty array must still contain exactly one matching PR number,
head SHA/ref/repository ID and base ref/repository ID; contradictory, malformed
or ambiguous associations block with
`Issue refresh blocked: native run PR/event binding unavailable`.
No ownership is inferred from shared fork/head/SHA, current PR reads, titles,
check suites or raw logs. The attempt-specific jobs endpoint and tuple bind the
attempt; no undocumented job attempt field or artifact download is used.

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

For aggregate target failures, the original `Issue refresh blocked for PRs: ...`
line is followed by `Issue refresh diagnostics: <JSON>`. It lists outcomes in
attempted-target order: `requested` with the admitted `runId` and `fromAttempt`,
or `failed` with an owned guard reason, validated HTTP status/method when
available, or `unknown`. `fromAttempt` is the original attempt, not proof of a
new attempt or completion. For example:

```text
Issue refresh blocked for PRs: 10
Issue refresh diagnostics: {"outcomes":[{"pullNumber":7,"outcome":"requested","runId":90,"fromAttempt":2},{"pullNumber":10,"outcome":"failed","reason":"guard","message":"native run PR/event binding unavailable"}],"omitted":0}
```

Only the first 20 outcomes are printed; `omitted` explicitly counts the rest.
The original full `results`, `failures`, and aggregate errors remain available
programmatically. Owned guard messages are recorded at the guard itself;
arbitrary error text, request URLs/headers, tokens, stacks, and response bodies
are not copied into these added records. Cause inspection stops at eight objects
or a cycle. Discovery errors outside aggregate target handling are unchanged.
A failed call can still have an ambiguous POST outcome: diagnostics do not prove
that GitHub rejected a request, explain an HTTP status beyond observed evidence,
retry a target, reconstruct old missing logs, or establish live gate selection.
Historical runs keep their original code; this output requires delivery to the
trusted workflow revision before a separately authorized future live test.

An eligible **Issue Approval Refresh Router** run can be
rerun from the Actions UI to rediscover currently affected PRs. Verify its
original trusted revision contains the dispatcher/helper and the admitted native
run targets each current PR. Inspect both exact native policy job results on the
current head afterwards; a queued rerun is not completed evaluation. A router
rerun keeps its original revision and cannot upgrade missing helpers or capability.
Runs created before the trusted workflow emitted the identity marker are
ineligible, even with a populated API association; rerunning them cannot add
new workflow code or retroactively reconstruct original-event proof. Wait for
a new normal covered event after the correction reaches trusted code, with
separate authorization for any live probe. The stopped #1859 fixture is not
reopened by this correction. No eligible original-event proof or token
capability means stop and report, never broad PR Validation/CI, an older run
or bootstrap-publisher fallback.
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
and zero other writes, including marker-backed empty fork associations,
rejected missing/ambiguous or other-PR/base associations, malformed or mismatched
original-event tuples, legacy marker absence, authoritative run drift, binding drift, missing
runs, checkout/source/job shape, stale policy and API errors. These tests never
use real tokens or network. Provider identity, duplicate selection, live approval
and revocation, queue compatibility and token capability need later rollout proof.

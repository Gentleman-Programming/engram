[← Codebase Guide](CODEBASE-GUIDE.md)

# Inactive issue-check publisher

This module is a tested foundation, **not an enabled refresh workflow**.
Legacy PR validation and native merge-group validation remain the active writers.
It does not execute PR code, change approvals, obtain credentials, or edit gates.

## Interface

`.github/scripts/issue-check-publisher.mjs` exports:

```js
await publishIssueChecks(github, { owner, repo }, evaluations);
```

`github` is an authenticated GitHub SDK client supplied by a future trusted caller.
`evaluations` uses the [read-only evaluator](ci-issue-validation.md) result contract:
each item has exactly `headSha`, `reference`, and `approved`.
Each result has exactly `conclusion` (`success` or `failure`) and a nonempty
`summary`; targets must be explicit full 40-character hexadecimal SHAs.
The repository object has exactly two nonempty string fields, `owner` and `repo`.
Extra fields, malformed results and unrelated batch shapes are rejected.

The function validates and snapshots the entire batch before the first write.
An empty batch makes no requests, even if the SDK has no Checks capability.
Later mutation of input objects cannot change snapshotted targets or conclusions.

## Publication and failure behavior

For each target, in order, it calls only `github.rest.checks.create`:
1. `Check Issue Reference`, using `reference`.
2. `Check Issue Has status:approved`, using `approved`.

Requests use `head_sha`, `status: completed`, and the result's conclusion;
output title is the exact check name and output summary is the result's summary.
Each response must confirm GitHub Actions App ID `15368`, the requested
head SHA, exact name, completed status and requested conclusion.

Missing Checks capability, API denial, other API errors or invalid response
binding throw an explicit blocker and stop remaining publication.
There is no retry, credential fallback, legacy-run update, cancellation,
rollback, or deletion. If a later write fails, earlier check runs may remain.
Repeated calls create new evidence rather than rewriting a legacy job's run.
Validation before writing does not make the asynchronous publication atomic,
nor can response validation undo a check that the API has already created.

## Permissions and evidence boundaries

A future trusted caller needs `checks: write` for these two check names.
PR/issues read scopes belong to metadata evaluation; contents read is needed
only to load trusted helper code. This module grants no scopes itself.
Do not broaden credentials when the existing token cannot create the checks.

Run the deterministic public-interface tests locally:

```sh
node --test .github/scripts/issue-check-publisher.test.mjs
```

Mocked responses establish requests, validation, binding rejection, snapshotting,
ordering and explicit partial-failure behavior. They **do not establish** live
token capability, actual App identity, required-check selection, fork behavior,
legacy/duplicate selection, or approval/revocation event reconciliation.
Those require the later trusted-workflow and observed rollout units.
This slice neither adds workflow triggers nor retires existing PR/queue writers.

# Contributing to Engram

Thanks for contributing. Engram enforces a strict **issue-first workflow** — every change starts with an approved issue.

[Contribution policy transition](docs/contribution-policy-transition.md) is **PRE-CUTOVER / NOT ACTIVE** target guidance; the active requirements below remain unchanged.

---

## Contribution Workflow

```
Search canonical issue → Investigate proportionally → Approve → Implement → Review & Merge
```

### Step 1: Open an Issue

Use the correct template:
- **Bug Report** — for bugs
- **Feature Request** — for new features or improvements
- **Documentation Improvement** — for missing, outdated, or unclear docs
- **Tracked Question** — for questions requiring maintainer investigation, a repository change, or a durable decision

> ⚠️ Blank issues are disabled. You must use a template.
> General questions and support belong in [Discussions](https://github.com/Gentleman-Programming/engram/discussions).

Search existing issues for a canonical tracker before opening a new one. Fill in all required fields; report symptoms, impact, release context, and logs without having to diagnose the root cause. Your issue receives `status:needs-review`.

For newly opened issues, the Issue Status Fallback workflow reads current API
labels and body. It adds `status:needs-review` only when no `status:*` exists.
Independently, when no `type:*` exists, it recovers `type:bug`, `type:feature`,
`type:docs`, or `type:question` only if the body uniquely contains a current form's
required field headings (outside fenced code). Unknown, incomplete, or ambiguous
structures remain for triage; titles and answer prose are never classifiers.
All existing labels are preserved, including existing types and statuses.
It does not infer approval, priority, or effort, backfill existing issues, or
respond to edits or later label removals. The API read and additive write are
not atomic. This fallback does not diagnose why native form labels were missing.

### Step 2: Investigation and Proportional Approval

A maintainer triages the report and assigns a real investigator/owner. Keep evidence on the canonical issue; cluster duplicates only when their root cause and affected contract match, not just their symptoms. Move useful occurrence evidence there before duplicate closure.

Use the smallest evidence set that establishes correctness:

| Change | Evidence before approval |
|--------|--------------------------|
| Trivial change | Clear scope and an applicable check |
| Bug | Reproduction against exact current remote `main` SHA, first violated repository-owned invariant (root cause), and a regression test plan |
| High-risk change (data, security, or shared contracts) | Applicable bug evidence, approved design, non-goals, and an independent review plan |

A short explanation in the issue is sufficient; no separate design document is required by default. Maintainers assess the reasoning, not CI. Replace the current `status:*` label with `status:approved` only after accepting the evidence appropriate to the change. For behavior changes, verify the regression tests before merge; for high-risk changes, complete independent review before merge.

**New work requires an approved issue and a real assignee before implementation.** Any issue assignee satisfies ownership; the PR author need not match. CI checks approval and assignment, not the quality of prose, and cannot prevent opening a noncompliant PR.

### Step 3: Open a Pull Request

Once the issue is approved:

1. Fork the repo and create a branch from the canonical `main`
2. Implement your change
3. Open a PR using the PR template — **link the approved issue** with `Closes #N`
4. Add exactly **one `type:*` label** to the PR (see label system below)

#### Forks and remotes

A pull request targets the canonical repository. A fork is where the branch lives, not where the pull request opens.

| Remote | Points at | Holds |
|---------|-----------|-------|
| `origin` | your fork | your branches |
| `upstream` | the canonical repository | the canonical `main` |

Verify both remotes before publishing, then branch from the canonical `main`:

```bash
# If your clone has no upstream remote yet, add the canonical repository:
git remote add upstream <canonical-repository-url>
# Ensure origin points at your fork:
git remote set-url origin <your-fork-url>
git remote -v                                   # origin must be your fork, upstream the canonical repository
git fetch upstream
git checkout -b feat/1821-short-description upstream/main
git push -u origin feat/1821-short-description
```

A plain `git clone` of your fork gives that clone only `origin`; forking on the GitHub web interface changes nothing in a local clone. `gh repo fork --clone` sets both up — the fork becomes `origin` and the previous `origin` is renamed to `upstream` — and `gh repo fork --remote` does the same in an existing clone. If `upstream` is absent, add it before running `git fetch upstream`; the failure is `fatal: 'upstream' does not appear to be a git repository`.

After `git fetch upstream`, branching from `upstream/main` uses the current canonical `main`; this workflow does not require synchronizing your fork's `main`. Branching from a stale local or fork `main` instead may omit recent canonical changes, but does not necessarily produce an unclean diff.

Open the pull request against the canonical repository explicitly, so the destination is never inferred from the clone you happen to be in:

```bash
gh pr create --repo <canonical-owner>/<canonical-repo> \
  --head <your-fork-owner>:<branch> --base main \
  --title "feat(scope): description" --body-file pull_request_body.md
```

The `--head <owner>:<branch>` form supports user-owned forks only; GitHub CLI does not support an organization as `<owner>`. For an organization-owned fork, use GitHub's web pull-request flow and explicitly select the canonical base repository and branch, the fork, and the head branch before submitting.

#### Authorizations are separate

| Action | Who performs it |
|--------|-----------------|
| Create or use a fork | you, once authorized to create or use that fork |
| Push commits to a branch in your fork | you, once separately authorized to push those commits there |
| Open or update a pull request in the canonical repository | you, once you are authorized to publish there |
| Apply the `type:*` label to a pull request in the canonical repository | a maintainer with triage access. A contributor without it receives `does not have the correct permissions to execute AddLabelsToLabelable`; that is a maintainer step, not something to retry |
| Merge a pull request | a maintainer |

Request the specific action you need. Authorization to create or use a fork does not authorize pushing commits to it. Being authorized to open a pull request is not authorization to label it or to merge it.

#### When a push to the canonical repository is rejected

Do not push branches to the canonical repository. It fails in one of two ways, depending on your access:

- **Without push access** — the remote refuses the push: `ERROR: Permission to <canonical-owner>/<canonical-repo>.git denied to <you>`.
- **With push access** — the repository's ruleset rejects the new branch: the push fails with `GH013` and the required checks are reported as expected.

Both are the controls working as designed.

- Do not retry the push, silently switch the destination, use an exempt branch namespace, fabricate checks, or change the ruleset.
- Push the branch to your fork and open the pull request from there, as described above.
- If neither the fork nor the label step is authorized for your account, stop and ask a maintainer for an authorized route. Record the rejection on the issue: it is useful evidence, not an obstacle to work around.

#### Dependent pull requests

A dependent pull request is a second pull request whose base branch is another contributor's branch. **This repository has no validated procedure for delivering one from a fork, so treat it as requiring maintainer guidance rather than something to work around.**

For a pull request targeting the canonical repository, the base branch must exist in the canonical repository. A fork-only branch can supply the head, not the base. Targeting a fork instead is a distinct, unvalidated workflow requiring maintainer guidance. The diff is computed against the selected base, but that alone does not require the parent pull request to merge first. Who may create the canonical base branch and how to sequence merges are maintainer workflow decisions that this guide does not make for you.

Ask before you start one. For a pull request targeting the canonical repository, do not use a base branch that does not exist in that repository. Pushing a branch to your fork does not make it a valid base for that pull request.

### Step 4: Automated PR Checks

The active required contexts run automatically on every PR and merge queue group:

| Check | What it verifies |
|-------|-----------------|
| **Check Issue Reference** | PR body contains `Closes #N`, `Fixes #N`, or `Resolves #N` |
| **Check Issue Has status:approved** | Every linked issue has `status:approved` and at least one assignee |
| **Check PR Has type:* Label** | PR labels use the canonical vocabulary and cardinality |
| **Unit Tests** | First runs non-mutating `go mod tidy -diff` with the release Go toolchain on PRs, merge groups and main pushes; runs `go test ./...` (except `//go:build e2e`) and `make deadcode-check` |
| **E2E Tests** | `go test -tags e2e ./internal/server/...` — end-to-end integration tests |
| **Plugin Tests** | The Pi plugin test suite runs from a clean checkout |

PR Validation runs on PR opening, edits, label addition/removal, synchronization,
and reopening. Reopening requests a fresh **Check Issue Reference** and
**Check Issue Has status:approved** evaluation under the existing rules.
Changes to labels on a linked issue do not themselves trigger PR Validation;
the approval check fetches current issue labels and assignees when the workflow runs. PR label events
and linked-issue label events are distinct.

**Issue-refresh bootstrap (not yet live proof):** the candidate **Issue Validation
Refresh** workflow adds trusted metadata-only publication for approval-label
addition/removal and the same PR events via `pull_request_target`, not merge-group
events. Native PR Validation merge-group jobs and their resolver remain unchanged
and must remain intact even in later PR-writer retirement. It becomes
available only after integration on the trusted branch; do not assume this
introducing PR can run it. Original PR Validation and its issue jobs remain
unchanged until live provider/required-gate compatibility is observed.
See the [rollout and trusted manual recovery guide](docs/ci-issue-refresh-rollout.md)
before relying on refresh or retiring legacy writers. The publisher uses only
`checks: write`, PR/issues read access and contents read access for trusted helpers;
it never executes PR-head code or changes approvals, settings or required gates.
Its issue/default and PR-target/base triggers establish trusted provenance;
`workflow_sha` alone is not a universal trust marker.
Refresh is eventual, not atomic: scheduling/API delays, metadata changes after
reads, concurrent or late legacy writes and already evaluated merge groups leave
consistency windows. Existing visible checks may remain after an error. Recover
by rerunning an eligible **Issue Validation Refresh** metadata-only run, not
original PR Validation or broad CI; verify current-head results afterwards.

**Approved native-gating amendment:** #1802 now permits `actions: write` only
on the selective issue-label router job, never globally. Native metadata
jobs remain read-only. #1834 / PR #1838 integrated the native jobs and head-bound
adapter; PR #1843 integrated the inactive guarded rerun helper on trusted main.
PR #1850 integrated #1844's inactive dispatcher on verified main at `f14fc4f3`.
The #1835 branch candidate adds approval-label issue routing, not yet available
on trusted main. Its dispatcher prerequisite is integrated; workflow delivery
and activation remain separate from local preparation. Repository Actions
capability is constrained by trusted code, not workflow-granular permissions.
Missing native-run PR association or token capability blocks refresh explicitly.
Partial target failures can leave earlier reruns requested; this is not atomic
reconciliation. Local tests are not live required-gate proof. Legacy writers,
the queue, six contexts and App15368 remain intact. #1802 stays open and #1810
retirement stays blocked: fresh T4 probe consent and live evidence are required
before rollout claims or retirement. See the
[native-policy scope and rollout boundary](docs/ci-issue-native-policy.md).

Closing references also support `Closes OWNER/REPO#N`, `Fixes OWNER/REPO#N`
and `Resolves OWNER/REPO#N` for the current repository or
`Gentleman-Programming/engram` (repository identity is case-insensitive).
Every recognized reference must be approved and assigned in its own repository;
a recognized reference to another repository rejects both issue checks before
any issue lookup for that PR, even when mixed with trusted or bare references.
Unsupported syntax remains ignored. Validation supports qualified references,
but automatic issue-label refresh discovery remains bare-only (`#N`) in the
current repository; qualified-only links do not expand discovery or rerun routing.

All required checks must pass before a PR can be merged. Issue API errors fail closed.

### Linked Issue Assignment

The independent **Linked Issue Assignment** metadata workflow handles PR opening,
body edits, reopening and ready-for-review events. It fetches the current PR and
assigns its human author (never the event actor) to open, `status:approved`,
unassigned issues referenced by bare `Closes #N`, `Fixes #N` or `Resolves #N`.
References are case-insensitive and deduplicated; plain numbers, qualified
references and URLs are ignored. Drafts, bots and references to PRs are skipped.
Existing assignees are always preserved; there are no comments, labels, approvals,
unassignments, historical scans or required-check/settings changes.

This is a convenience, **not a replacement for approved, assigned ownership before
implementation**. Required validation and refresh publishers remain independent;
rerun native PR validation after assignment changes. In this trusted
`pull_request_target` workflow, `github.workflow_sha` supplies the helper from the
executing workflow revision even when the historical PR base lacks it, never
PR-head code or candidate bootstrap. `workflow_sha` is not a universal trust
marker. This correction takes effect only after integration on the trusted
branch. Local tests do not establish hosted old-base, token or event behavior;
a real fork run remains pending.

Runs are serialized repository-wide with cancellation disabled for running jobs.
GitHub may replace/coalesce pending runs: this is best-effort scheduling, not a
FIFO queue or a guarantee that every event runs. Issues are reread before writes
and assignments verified afterwards, with assignments/skips in the run summary
and API/verification errors failing automation. Partial progress is possible.
Manual changes between the final read and write still race: GitHub provides no
atomic conditional assignment. Inspect current ownership before retrying a failed
run; automation never removes an existing assignee.

Run `node --test .github/scripts/issue-auto-assign.test.mjs` for local reference,
eligibility, race, failure and trusted-workflow contract regressions.

### Existing PR Transition

The original #1293 audit found 23 open PRs needing migration; that is a historical snapshot, not a permanent count. Activation does not wait for the whole backlog to be remediated. Review existing PRs when they become active again or before merge: confirm proportional evidence, replace outdated approval where needed, and assign a real owner. Close obsolete work through normal maintainer review. Do not assign a placeholder solely to turn CI green.

**Every PR, including existing PRs, must comply before merge.** There is no date cutoff or silent grandfathering; automatic assignment is limited to the eligible event-driven convenience above, not backlog migration. Re-run native PR validation after issue approval/assignment changes before merging; an old green check is not current readiness evidence.

> **Repo admin note:** The active `main` ruleset requires exactly these six contexts: `E2E Tests`, `Unit Tests`, `Plugin Tests`, `Check Issue Has status:approved`, `Check Issue Reference`, and `Check PR Has type:* Label`.

The transient-artifact workflow subscribes to opening, edits, synchronization
and reopening, but not label changes. Its job runs for base edits and skips
title/body-only edits before checkout or API reads. Base edits remain relevant
to both the diff and its trusted base policy. Both this check and PR Size Notice
retain their trusted `pull_request_target` checkout boundaries; neither executes
PR-head code. Local event-matrix tests do not prove hosted event delivery or
measured savings; verify actual runs after integration on the trusted branch.

Non-required PR checks include the Claude plugin version guard, lint, Windows setup and wrapper coverage, transient-artifact validation, **Policy Helper Tests** (the unchanged merge-queue, label-policy, transient-artifacts and PR-size-notice command, plus explicit `.github/scripts/ci-technical-contracts.test.mjs` execution and a separate step discovering available `.github/scripts/issue-*.test.mjs` regressions), and **Obsidian Build** (tests, typecheck, and build after dependency installation). Policy Helper Tests, Obsidian Build, and the Claude plugin version guard also run on merge groups; lint, Windows checks, and transient-artifact validation do not run as merge-group CI jobs. None of these are active `main` required contexts; a maintainer must explicitly add the Claude plugin version guard to the ruleset before it becomes required.

### CI job execution ceilings

The [accepted #2020 execution contract](https://github.com/Gentleman-Programming/engram/issues/2020#issuecomment-6423158281)
sets these initial job-level `timeout-minutes` ceilings in
[CI](.github/workflows/ci.yml):

| Job ID | Outer timeout (minutes) |
|---|---:|
| `lint` | 15 |
| `policy-helper-tests` | 10 |
| `unit-tests` | 20 |
| `e2e-tests` | 15 |
| `windows-setup-test` | 20 |
| `wrapper-tests-windows` | 15 |
| `obsidian-build` | 10 |
| `perf-ratchet` | 20 |

Each ceiling includes the entire job, including checkout, tool/dependency setup
and every existing check. Unit Tests' 20 minutes includes administrative helper
preparation and the selected proof wait (up to five minutes), setup, tidy,
generated-output and deadcode checks, the Go suite, build and quick self-test.
The proof-branch bootstrap's 15-second limit is not a universal administrative
fetch bound. Go's default 10-minute test timeout applies per test binary, not to
the whole suite.

These are initial operational ceilings, not validated cold/failure-path SLOs.
Exhaustion remains a failure; retain original diagnostics without masking errors,
automatically increasing budgets or weakening coverage. Hosted timeout and
cleanup behavior is not established by structural tests. Existing bounded jobs,
inner deadlines, commands, conditions, required contexts and queue stay unchanged,
including Race's 30-minute outer and 20-minute per-binary limits.

Run `node --test .github/scripts/issue-ci-timeouts.test.mjs` for the ceiling map,
missing/wrong/step-only negative cases, preserved budgets and documentation checks.
Existing issue-helper discovery includes this suite; no new required check is added.

### CI concurrency

CI uses a workflow-namespaced PR-number concurrency group with cancellation
only for ordinary `pull_request` runs: the head branch must not start with
`ci/proof-` (case-insensitive), and raw `vars.ISSUE_CI_PROOF_CI` must be empty.
Every other run uses a workflow-namespaced `run_id`/`run_attempt` group with
cancellation disabled. This isolates protected pending slots as well as running
work: disabling cancellation alone would not prevent pending-run replacement.
Merge groups, main pushes, reserved proof branches and PRs with any nonempty
selector (including OFF, malformed or unmatched values) are isolated conservatively.
Ordinary PR reruns share their PR group; protected reruns get a distinct attempt
group. No newest-commit ordering is guaranteed.

This is workflow admission, not the job's parsed controller selection. Controllers
and their bootstrap remain unchanged and OFF. Fork/rerun variable visibility,
control transitions and scheduling by commit recency are not hosted-qualified;
no savings have been measured. See [proof boundaries](docs/ci-proof-controls.md#workflow-admission-concurrency-2096).
Local technical checks (no proof helpers or qualification data):
`node --test .github/scripts/ci-technical-contracts.test.mjs`.
This suite owns ordinary PR/main/merge-group routing, race/toolchain/failure
contracts, existing verification commands and their ordering, permissions,
events and test discovery. Proof-specific admission remains covered by
`node --test .github/scripts/issue-ci-concurrency.test.mjs`.
Hosted experiments require separate authorization.

### Go race detection (initially non-required)

The candidate **Go Race Tests** job runs the full default-tag Go suite with
`go test -race -timeout=20m ./...` on Linux, independently of existing suites.
CGO and GCC are enabled; test failures remain failures. The 20-minute per-binary
budget follows a corrected local store race run of 14m04s. The successful
[hosted full-suite run](https://github.com/Gentleman-Programming/engram/actions/runs/40147453688)
at head `b64205b3` took 19m04s; the separate 30-minute job budget stays provisional.
No required-check settings change. See [scope, local reproduction and failure
classification](docs/ci-go-race.md).
Local structural checks: `node --test .github/scripts/ci-technical-contracts.test.mjs`.
These inspect workflow contracts; they do not execute the Go race suite.

### CI scanners (initially non-required)

**Go Vulnerability Check** and **Workflow Validation** run independently on PRs,
merge groups and main pushes. They preserve finding and tooling-failure exits;
non-required does not mean failures are ignored. Existing required gates remain
unchanged. See [scanner setup, real-tool tests and result interpretation](docs/ci-scanners.md)
for pinned Go 1.26.10, govulncheck v1.7.0 and actionlint v1.7.12 commands.
Mocked wrapper tests are distinct from opt-in real-tool integration tests; neither
is hosted rollout evidence. Review findings without blanket suppressions or
unapproved dependency upgrades.

### Merge Queue Activation (administrators)

The `main` merge queue is already active. Contributors submit eligible PRs to the
queue; the six required contexts above also run on each merge group.

The effective queue rule was observed on **2026-10-09** with these settings:

| Setting | Observed value |
|---------|----------------|
| Grouping strategy | `ALLGREEN` |
| Merge method | `SQUASH` |
| Check response timeout | 60 minutes |
| Maximum entries to build (`max_entries_to_build`) | 5 |
| Minimum entries to merge (`min_entries_to_merge`) | 1 |
| Maximum entries to merge (`max_entries_to_merge`) | 5 |
| Minimum-group wait (`min_entries_to_merge_wait_minutes`) | 5 minutes |

This is an observed configuration snapshot, not a recommendation to change
settings or a guarantee that they remain unchanged. Consult the
[effective `main` rules](https://api.github.com/repos/Gentleman-Programming/engram/rules/branches/main)
for current settings; the [audit in #1736](https://github.com/Gentleman-Programming/engram/issues/1736#issuecomment-6089006647)
records the supporting evidence.

Any queue-policy change requires explicit owner/admin approval and an authorized
administrator. If rollback is approved, disable or remove only the queue rule;
retain the existing required contexts and unrelated rules. This documentation
correction does not authorize settings changes.

## Advisory AI Review

CodeRabbit uses the versioned root [`.coderabbit.yaml`](.coderabbit.yaml). Its
baseline is the [resolved Repository UI configuration](https://github.com/Gentleman-Programming/engram/pull/1728#issuecomment-6084792433);
existing path instructions, generated-file exclusions and other settings are
preserved. The only noise adjustments are the balanced `chill` profile and
turning off the numeric docstring coverage warning. Documentation correctness
still matters and remains part of review.

JS/TS test suites have additional path instructions alongside the preserved
Go-test and thin-adapter guidance:

| Test surface | Instruction glob |
|--------------|------------------|
| Repository policy scripts | `.github/scripts/*.test.mjs` |
| Obsidian | `plugin/obsidian/test/*.test.mjs` |
| OpenCode JavaScript | `plugin/opencode/*.test.mjs` |
| OpenCode TypeScript | `plugin/opencode/*.test.mts` |
| Pi, including nested suites | `plugin/pi/test/**/*.test.mjs` |

These instructions prioritize deterministic setup/cleanup, relevant success,
error and boundary cases, and observable effects: returned values, outgoing
requests, persisted files, warnings and prohibited side effects. Useful
isolated mocks, stubs and internal seams remain valid when they protect real
contracts, as do workflow, schema and package structural-contract checks.
Assertions that merely mirror implementation details or fixtures deserve scrutiny;
this is not a blanket integration-test or style-rewrite requirement. Helper-only
files retain generic/plugin review without a dedicated test-suite instruction.
These globs add guidance, not review exclusions or new required checks.

GitHub Actions workflows have dedicated advisory instructions:

| Workflow surface | Instruction glob |
|------------------|------------------|
| Workflow YAML | `.github/workflows/*.yml` |
| Alternative YAML extension | `.github/workflows/*.yaml` |

Review guidance considers event context, minimum necessary job/token permissions,
secret exposure and checkout/execution provenance. It distinguishes trusted-base
or trusted-workflow metadata/label checks from executing attacker-controlled PR
code with privileged credentials. Fork inputs, PR metadata and downloaded
artifacts remain untrusted data. Require justification for legitimate write scopes
(releases, package publishing, OIDC and repository maintenance), rather than
blanket `contents: read` or bans on privileged events. This adds no workflow,
permission or required-check changes and does not replace actionlint or zizmor.

Sync/import paths have targeted advisory instructions:

| Sync surface | Instruction glob |
|--------------|------------------|
| Chunk export/import and replay | `internal/sync/**/*.go` |
| Wire encoding | `internal/cloud/chunkcodec/**/*.go` |
| Automatic push/pull orchestration | `internal/cloud/autosync/**/*.go` |
| Transport | `internal/cloud/remote/transport.go` |
| Chunk and mutation handlers | `internal/cloud/cloudserver/cloudserver.go`, `internal/cloud/cloudserver/mutations.go` |

The existing `internal/store/**/*.go` and
`internal/cloud/cloudstore/**/*.go` ownership instructions are retained, with
additional guidance conditional on sync/import changes only. No duplicate
persistence rules are added or instruction-overlap precedence assumed.

Review record/identity and deletion preservation, and the existing `review_after`
contract: omitted fields, explicit null and valid dates are not interchangeable.
Check idempotent replay, transaction boundaries and recovery from partial
failures; do not assume whole-sync atomicity. Check that push acknowledgements
and pull cursors reflect successfully accepted/persisted work according to
existing contracts. Policy rejection must stay explicit without bypassing
enrollment or cloud pause. Seek deterministic regression evidence for changed
observable behavior, including rejected-input side effects, replay and recovery.
This guidance does not prescribe new sync algorithms, ownership/conflict rules
or review scheduling, change data formats, permissions or required CI checks,
or guarantee protection against data loss.

CodeRabbit does not automatically approve PRs (`request_changes_workflow: false`),
skips drafts, and does not replace human review or the required checks above.
Generated outputs are excluded only from AI review, not from generation checks.
Existing manually requested code-generation/fix features remain unchanged.

Changes to this file require normal issue-first review. Run `go test . -run
TestCodeRabbit -count=1` to check YAML parsing, the two adjustments, JS/TS and
workflow and sync instruction content/scope, documentation and baseline preservation.
The original export hash is retained: only the five exact JS/TS instruction
blocks, two exact workflow instruction blocks and six exact sync instruction
blocks are removed, and the two conditional persistence additions are reverted
before comparing all other settings. Deliberate future settings changes must
update the baseline test with their rationale. Local path examples exercise a
limited plain-glob contract, not CodeRabbit's matcher, schema validation or live
instruction overlap. To pause automatic reviews on one PR, comment
`@coderabbitai pause`; to disable them through configuration, change
`reviews.auto_review.enabled` to `false` in a reviewed PR. Reverting the initial
configuration-file addition restores selection of the existing UI configuration,
provided that UI configuration has not changed.

Installation permissions could not be audited with the available credentials.
Live review behavior and noise reduction must be observed after rollout; local
policy tests do not establish those results. Inspect representative live feedback
on these test surfaces and record useful findings, noise and limitations before
claiming improved test-review quality. For workflow guidance, observe representative
safe/unsafe review feedback without executing unsafe examples; record useful
findings, false positives and limitations before claiming improved security review.
For sync guidance, inspect representative live feedback for metadata preservation,
replay and partial-failure changes; record useful findings, false positives and
limitations after rollout. These local configuration tests do not prove live
review quality or sync correctness; deterministic sync tests and human review
remain necessary.

## Claude Plugin Version Rule

When changing any file under `plugin/claude-code/` (including renames or deletions), increase the semantic version in both `plugin/claude-code/.claude-plugin/plugin.json` and the `engram` entry of `.claude-plugin/marketplace.json` to the same higher version. The PR and merge queue guard compare against the event's base commit; malformed, missing, or inconsistent versions fail. Changes outside the Claude plugin subtree need no version bump. The guard runs the policy from the trusted base whenever it exists. On the first PR introducing the policy, the base has no script, so CI must temporarily run the candidate's policy; this bootstrap cannot prevent that PR from weakening its own guard. After merge, subsequent PRs use the base policy.

## Transient Artifact Policy

PR validation inspects the complete changed-file set. Deleted artifacts are allowed; enforcement applies to added, modified, copied, and renamed destination paths. It rejects the following transient artifacts unless a path is explicitly described as repository-root-only:

| Enforced class | Forbidden paths and variants |
|---|---|
| Agent-tool state | Any directory named `.atl` (at any depth) and `**/engram-dev/**` |
| Generated agent links | Repository-root `.claude/skills/**`, `.codex/skills/**`, `.github/skills/**`, and `.gemini/skills/**` |
| Transient development documents | Repository-root `plan.md`, `agent-report.md`, `agent-handoff.md`, and `handoff.md` |
| Transient process artifacts | Repository-root `openspec/changes/**` and `sdd/changes/**` |
| Release metadata | `**/.release-notes-beta.md` |
| Local data | `**/*.db`, `**/*.db-wal`, `**/*.db-shm`, and `**/engram-export.json` |
| Binaries | Repository-root `engram`, `cmd/engram/main`, `cmd/engram/gentle-creation`, `cmd/engram/engram`, and `**/*.exe` |
| OS metadata | `**/.DS_Store` and `**/Thumbs.db` |
| Editor metadata | `**/.idea/**` and `**/.vscode/**` |
| Editor and backup files | `**/*.swp`, `**/*.swo`, and `**/*~` |

Canonical, reviewable documentation is allowed. For example, `docs/plan.md` and `specs/transient-artifact-policy.md` are documentation, not root transient development documents. The four transient document names above are forbidden only at the repository root; do not use documentation paths to retain ephemeral local notes.

### Quality Ratchets

Every PR and push to `main` runs `make deadcode-check`. It analyzes all module
packages with `golang.org/x/tools/cmd/deadcode@v0.30.0` and compares stable
`file<TAB>symbol` identities with `.deadcode-baseline.txt`. New unreachable
functions fail CI. Removed entries pass and report that the debt tightened;
review and deliberately refresh the baseline with `make deadcode-baseline` in
the same change. Do not update a baseline merely to accept new debt.

The default analyzer runs with command-scoped `GOTOOLCHAIN=go<version>+auto`,
where `<version>` comes from the `go` directive in the repository's `go.mod`,
not its optional `toolchain` directive or your ambient `GOTOOLCHAIN`. Go may
automatically download that toolchain (or a newer required toolchain); the default
analyzer requires a Go 1.21 or newer launcher for toolchain selection. Allow
download access or provide the toolchain locally. Missing or invalid module
minimums and analyzer/toolchain failures stop the check; a failed analyzer never
refreshes the baseline.

`DEADCODE_RATCHET_ANALYZER` bypasses this selection entirely: owners of that
explicit executable override are responsible for its compatible toolchain and
analyzer behavior. `--compare <baseline> <candidate>` only compares identity
files and does not invoke Go.

Patchless minimums from Go 1.21 onward select the `.0` release (for example,
`go 1.24` selects `go1.24.0+auto`). Explicit patches and historical release
names through Go 1.20 are preserved.

### Performance Ratchet

Pushes to `main` compare the store search and scan benchmarks with the exact
previous `main` SHA from the push event on the same runner. This catches
statistically significant slowdowns greater than the configured threshold
without treating timing as a unit-test assertion.

Run `make perf-check` against the committed baseline on a matching local
configuration. To refresh that reviewed baseline deliberately after an accepted
performance tradeoff, run `make perf-baseline` and include the baseline change
with its justification. The baseline records its producing OS, architecture,
and CPU and is only comparable on a matching host configuration; it is not a
cross-host latency budget. CI instead compares the event's previous SHA and the
new revision on one runner.

For a repository's first main push, or when that previous revision has only a
strict subset of the current benchmark suite, CI enters an explicit bootstrap
mode. It verifies that the candidate benchmark names exactly match the versioned
baseline, then deliberately skips a cross-host timing comparison. Later pushes
must pair every benchmark from both revisions; an empty, renamed, partial, or
configuration-split comparison fails.

### Lint Ratchet

CI runs golangci-lint v2.13.2 with the `errcheck`, `staticcheck`, and `unused`
linters. It reports only findings introduced by the pull request or the pushed
main revision, so existing debt does not block adoption while new debt fails
the check. When local lint is warranted by risk or the absence of PR CI, install
golangci-lint v2.13.2 and run `make lint`; the target requires that exact version
on `PATH` and fails before linting if it is missing or different. It reports
findings in staged, unstaged, untracked, and latest committed changes compared
with `HEAD~`.

---

## Label System

### Type Labels (required on every PR — pick exactly one)

| Label | Color | Use for |
|-------|-------|---------|
| `type:bug` | 🔴 | Bug fixes |
| `type:feature` | 🔵 | New features |
| `type:question` | 🟣 | Questions requiring tracked work |
| `type:docs` | 🔵 | Documentation-only changes |
| `type:refactor` | 🟣 | Code refactoring with no behavior change |
| `type:chore` | ⚪ | Maintenance, tooling, dependencies |
| `type:breaking-change` | 🔴 | Breaking changes (requires major version bump) |

### Status Labels (set by maintainers)

| Label | Meaning |
|-------|---------|
| `status:needs-review` | Awaiting maintainer review (auto-applied to new issues) |
| `status:approved` | Maintainer accepted proportional evidence; bugs include current-main reproduction and root cause |
| `status:in-progress` | Actively being worked on — auto-exempt from stale bot |
| `status:blocked` | Blocked by another issue or external dependency |
| `status:stale` | No activity for 30 days — auto-applied by stale bot |
| `status:wontfix` | Closed without implementation — applied to stale, rejected, or duplicate items |
| `status:possible-duplicate` | Potential duplicate under evaluation |

### Resolution Labels (set after closure)

| Label | Meaning |
|-------|---------|
| `resolution:duplicate` | Confirmed duplicate after closure |

Replace the current `status:*` label with `status:possible-duplicate` while evaluating a report. After confirming and closing the duplicate, replace `status:possible-duplicate` with `status:wontfix` and apply `resolution:duplicate`.

### Priority Labels (set by maintainers)

`priority:critical`, `priority:high`, `priority:medium`, `priority:low`

> Issues with `priority:critical`, `priority:high`, and `status:approved` are never auto-closed by the stale bot.

### Effort Labels (set by maintainers, for contributor guidance)

| Label | Meaning |
|-------|---------|
| `effort:small` | < 1 hour — good starting point for new contributors |
| `effort:medium` | 1–4 hours |
| `effort:large` | > 4 hours or spans multiple files |

### PR Size Exception (maintainers only)

`size:exception` records explicit maintainer approval for a pull request to exceed the 400-line review budget. It applies only to pull requests and does not replace the requirement for a focused, reviewable change.

The advisory **PR Size Notice** counts the current GitHub-reported additions plus
deletions, without file exclusions or logical/authored-line adjustments. Exactly
400 changed lines is within budget; 401 or more produces a warning. For example,
200 additions plus 200 deletions is 400; 200 plus 201 is 401. Deleted files,
generated output, and lockfiles are not excluded. Binary changes have only the
line counts reported by GitHub; this is not a byte-size metric.

The notice respects `size:exception` and never applies it automatically. Label
presence represents the existing maintainer-owned policy; automated validation
does not verify who applied the label. Maintainers remain responsible for grants.

One consistently named informational check writes a fresh job summary on PR
opening, base-branch edits, reopening, synchronization, and addition/removal of
`size:exception`, including drafts. Title/body-only edits and unrelated label
changes skip the job before checkout or API reads; subscribed events may still
create workflow runs with a skipped job.
It refreshes when the diff shrinks or the exception changes; it does not post PR
comments or run on merge groups. Actions retains historical runs, not one mutable
summary across all revisions. Unavailable or invalid current metadata produces
**Size unavailable**, never a claim that the PR is within budget. Summary-write
errors are reported as warnings.

The notice is non-required and non-blocking: a successful job means the advisory
completed, not that the PR is under budget. No required contexts or rulesets are
changed. It reads current PR metadata and checks out `github.workflow_sha`, the
immutable commit of the executing trusted workflow, so its companion script is
available even when a pull request's historical base predates the notice. It
never checks out or executes pull-request head code, and retains read-only
permissions and no persisted checkout credentials. The workflow becomes
available after it is merged into the base branch; local tests do not prove live
fork events, summaries, or cancellation behavior.

Run `node --test .github/scripts/pr-size-notice.test.mjs` for the boundary,
exception, refresh, unavailable-data, and workflow regression checks.

### Namespace Contract

All label namespaces are owned by maintainers. Their cardinality depends on the GitHub surface:

| Namespace | Issues | Pull requests |
|-----------|--------|---------------|
| `type:*` | Exactly one | Exactly one |
| `status:*` | Exactly one | At most one |
| `priority:*` | At most one | Not applicable |
| `resolution:*` | At most one | Not applicable |
| `effort:*` | Multiple allowed | Not applicable |
| `size:*` | Not applicable | At most one |

The only unnamespaced labels are maintainer-owned protected exceptions: `good first issue` and `help wanted`.

### Deprecated Label Migration (maintainers only)

| Deprecated label | Canonical label | Precedence | Conflicting canonical value |
|------------------|-----------------|------------|-----------------------------|
| `bug` | `type:bug` | Canonical label wins | Stop for manual review |
| `enhancement` | `type:feature` | Canonical label wins | Stop for manual review |
| `question` | `type:question` | Canonical label wins | Stop for manual review |
| `documentation` | `type:docs` | Canonical label wins | Stop for manual review |
| `up for grabs` | `help wanted` | Canonical label wins | Stop for manual review |

Preview a label migration locally before changing an issue or pull request:

```bash
node .github/scripts/label-policy.mjs --migrate --labels-json '["bug","type:bug"]'
```

The command is a dry run: it prints the canonical label list as JSON and never writes to GitHub. A singleton conflict returns a non-zero exit code and leaves the original list unchanged; stop and resolve that ambiguity manually. Otherwise, apply only the reported replacements with `gh issue edit` or `gh pr edit`, then rerun the command until it reports `"changed":false`.

---

## PR Rules

- Keep PR scope focused — one logical change per PR
- Use [conventional commits](https://www.conventionalcommits.org/) format
- For behavior changes, run a focused regression test and the affected package(s) locally before proposing the change; record the commands and outcomes. Use targeted coverage when useful to find missed behavior, without a numeric target or per-PR total module coverage requirement.
- Verification is candidate evidence; GitHub CI is the broad automated execution venue once the candidate is pushed to a PR. CI runs the full unit suite (`go test ./...`), E2E suite (`go test -tags e2e ./internal/server/...`), lint, and applicable platform checks. Do not claim these results until they run.
- For candidates without a PR or not yet pushed, and for justified high-risk changes, run additional applicable checks locally and report any missing CI evidence. Do not duplicate the broad CI suite locally by default when PR CI will run it.
- Update docs in the same PR when behavior changes
- Do not reference endpoints/scripts that do not exist in code
- Do not include `Co-Authored-By` trailers in commits
- Do not include paths prohibited by the [Transient Artifact Policy](#transient-artifact-policy)

### Focused Verification Map

Use the row that matches the change, not every row. These are existing entry points,
not a new required local gate. Run commands from the listed directory; paths are
relative to the repository root.

| Changed area | Working directory | Prerequisites | Existing commands | What they verify |
|--------------|-------------------|---------------|-------------------|------------------|
| Go behavior | Repository root | Go version from [go.mod](go.mod) and module dependencies | `go test ./internal/setup -run '^TestStaleMCPCommands$' -count=1`, then `go test ./internal/setup -count=1` | Example focused regression and affected-package suite. Substitute the actual changed package and regression; this is not the full module suite. |
| Pi plugin | `plugin/pi` | Node.js 24, npm, and Go from [go.mod](go.mod) for Go-backed fixtures; run `npm ci --ignore-scripts --legacy-peer-deps` for the locked real-TypeBox test dependency (no Pi SDK peers) | `npm test` | Pi plugin suite from a Go checkout, including fixture-based workflows; not the full installed-host lifecycle. See [scripts](plugin/pi/package.json) and [real HTTP test prerequisites](plugin/pi/README.md#testing-the-real-http-workflows). |
| Obsidian plugin | `plugin/obsidian` | Node.js 24, npm, and dependencies installed with `npm ci` under the existing [dependency policy](#npm-dependency-hygiene) | `npm test`, `npm run typecheck`, `npm run build` | Plugin tests, TypeScript checking, and bundle generation; not an interactive Obsidian smoke test. See [scripts](plugin/obsidian/package.json). |
| PR policy helpers | Repository root | Node.js 24; no npm install step | `node --test .github/scripts/merge-queue.test.mjs .github/scripts/label-policy.test.mjs .github/scripts/transient-artifacts.test.mjs .github/scripts/pr-size-notice.test.mjs` | Merge-queue, label, artifact and PR-size helper logic; not live GitHub permissions or event delivery. |
| Technical CI contracts | Repository root | Node.js 24; no npm install step | `node --test .github/scripts/ci-technical-contracts.test.mjs` | Proof-independent workflow commands, ordering, race, permissions, events, discovery and ordinary concurrency; not hosted execution/cancellation proof. |
| Issue policy helpers | Repository root | Node.js 24; no npm install step | `node --test .github/scripts/issue-*.test.mjs` | Issue-policy and proof-specific helper suites; not live approval-refresh behavior. |

The **Unit Tests** job provides Node.js 24 for the Go-owned Core lifecycle tests.
The `tools/release-candidate` Core lifecycle tests require Node.js >=24 in PATH;
when Node is missing, cannot run, or is older, only the execution-dependent cases
skip with a prerequisite message. Non-runtime admission cases still run. With
Node.js <24, a separate regression verifies that Core is not started. To exercise
all supported lifecycle paths locally, run `go test ./tools/release-candidate`
with Node.js 24 in PATH.

The [CI workflow](.github/workflows/ci.yml) is authoritative for hosted commands,
setup steps and platform coverage; the [Makefile](Makefile) defines the existing
lint and quality-ratchet targets. This map does not replace either definition.
Record which commands actually ran and their results; distinguish structural
command checks from execution. A green local subset is **not** a full CI or release
verdict. Hosted full-unit, E2E, plugin and applicable platform results remain
pending until GitHub runs them. Documentation-only changes can use proportional
link/command-definition checks without executing unrelated suites.

### PostgreSQL integration CI

The candidate **PostgreSQL Integration Tests** job runs the existing cloudstore,
cloudserver and CLI runtime suites against a disposable PostgreSQL 16 service.
It verifies database-backed execution rather than treating missing-DSN skips as
coverage. It does not activate a required check. See the
[scope, evidence and local cleanup guide](docs/postgres-ci.md) for commands,
intentional exclusions and the pending hosted-proof boundary.

### Claude jq 1.6 response compatibility

The Linux **Claude jq 1.6 Compatibility** job verifies the SHA-256 and version of
an official jq 1.6 binary in runner-temporary storage, then runs only the HTTP
response-validation cases and the selector's wrong-version regression. It does
not replace the system jq or change native Windows coverage.

To select a real jq 1.6 delegate locally on POSIX systems:

```bash
ENGRAM_TEST_JQ16=/absolute/path/to/jq-1.6 go test ./plugin -run '^(TestClaudeHookResponseValidation|TestClaudeJQ16SelectorRejectsWrongVersion)$' -count=1 -v -timeout=120s
```

A nonempty selector must be an absolute executable path reporting `jq-1.6`;
missing or wrong-version delegates fail rather than skip. When unset, the wrapper
still delegates to `/usr/bin/jq` and emulates only jq 1.6's empty-input `-e`
behavior, not full jq 1.6 compatibility. The POSIX wrapper still skips on Windows.

### Conventional Commit Format

```
<type>(<scope>): <short description>

[optional body]

[optional footer]
```

**Examples:**

```
feat(cli): add --json flag to session list command

fix(store): prevent duplicate observation insert on retry

docs(contributing): add label system documentation

refactor(internal): extract search query sanitizer

chore(deps): bump github.com/charmbracelet/bubbletea to v0.26

fix!: change session ID format (breaking change)
BREAKING CHANGE: session IDs are now UUIDs instead of integers
```

Types map to labels: `feat` → `type:feature`, `fix` → `type:bug`, `docs` → `type:docs`, `refactor` → `type:refactor`, `chore` → `type:chore`.

---

## npm Dependency Hygiene

When adding npm dependencies to `plugin/pi` or `plugin/obsidian`:

### Use `npq` for inspection

Install once:

```bash
npm i -g npq
```

Then install new deps via:

```bash
npq install <package>
```

`npq` runs pre-flight checks (typosquats, install scripts, known vulns) before delegating to npm.

### Honor the `.npmrc` defaults

The repo `.npmrc` enforces:

- `ignore-scripts=true` — third-party lifecycle scripts do NOT run on install
- `allow-git=none` — git URLs as deps are rejected
- `min-release-age=3` — packages newer than 3 days old are rejected

If you have a legitimate reason to override these for local dev (e.g. `esbuild` postinstall), use a flag for that specific command — DO NOT edit `.npmrc`:

```bash
npm install --ignore-scripts=false esbuild
```

### Consult Snyk before merging

For every new dep added in a PR, paste the Snyk Advisor link in the PR description:

```
https://snyk.io/advisor/npm-package/<name>
```

See [SECURITY.md](./SECURITY.md#vetting-new-dependencies) for the maintainer-side vetting checklist (provenance, transitive deps, install scripts).

---

## Skill Authoring Standard

Repository skills live in `skills/`.

Use a **hybrid format**:

1. Structured base (purpose, when to use, critical rules, checklists)
2. Cookbook section (`If / Then / Example`) for repetitive actions

Why hybrid:
- Structured base protects correctness and architecture intent
- Cookbook improves execution consistency for common flows

---

## Automated Dependency Updates

Dependabot preserves the existing Go, GitHub Actions and Docker coverage and
checks Obsidian's `plugin/obsidian/package.json` and tracked lockfile every
Monday at 09:00 UTC. The npm job allows at most three open version-update PRs
and has a three-day cooldown for new versions. The cooldown does not apply to
security updates and does not replace dependency vetting or npm install safety.
Pi coverage is deferred; its manifest includes the locked test-only TypeBox dev dependency alongside runtime peer dependencies.

Bot-authored PRs are not exempt from the approved-issue checks:

1. A maintainer creates or reuses an issue covering the specific proposed update
   and approves it with `status:approved`.
2. Add `Closes #N`, `Fixes #N`, or `Resolves #N` to the bot PR body, preserving
   the generated update details. Do not reuse the configuration setup issue as
   authorization for subsequent dependency upgrades.
3. Keep exactly one canonical `type:chore` label. The PR remains blocked until
   the required issue checks pass; CI and human review still decide merge.

No automatic issue approval, check bypass or auto-merge is configured.
The npm-hygiene audit is separate: preserving the root `.npmrc` is not proof
that independent nested-package installs enforce its controls.

Run `go test . -run '^TestDependabot' -count=1` to check the versioned policy
and preservation of the other ecosystems. After merge to the default branch,
inspect Dependabot's update logs and an actual Obsidian update PR for
manifest/lockfile consistency, labels and passing checks. Configuration tests
do not prove vendor-schema validation, bot execution or successful rollout.

---

## Maintainer Triage Cadence

Engram uses a lightweight, regular cadence so contributors know what to expect.

| Activity | Frequency | What Happens |
|----------|-----------|-------------|
| New issue triage | Within 2 days | Maintainer labels + approves or closes |
| PR review | Within 7 days | Maintainer reviews + requests changes or merges |
| Backlog sweep | Weekly (Monday) | Stale bot runs; approved/blocked issues reassessed |
| Label audit | Monthly | Orphan labels removed; accuracy check |
| Dependabot PRs | Weekly | Review merged or deferred |

If you haven't received a response within 7 days on a PR or issue, a single ping comment is welcome.

---

## What Gets Closed Without Merging

- PRs opened without an approved issue
- PRs that fail CI and aren't updated within 30 days
- Issues that are vague, a duplicate, or belong in [Discussions](https://github.com/Gentleman-Programming/engram/discussions)
- Issues with no response to a maintainer question after 14 days

---

## Agent Skill Linking

Run:

```bash
./setup.sh
```

This links repo `skills/*` into project-local:
- `.claude/skills/*`
- `.codex/skills/*`
- `.gemini/skills/*`

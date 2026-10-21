---
name: engram-branch-pr
description: >
  PR creation workflow for Engram following the issue-first enforcement system.
  Trigger: When creating a pull request, opening a PR, or preparing changes for review.
license: Apache-2.0
metadata:
  author: gentleman-programming
  version: "2.0"
---

## When to Use

Use this skill when:
- Creating a pull request for any change
- Preparing a branch for submission
- Helping a contributor open a PR

---

## Critical Rules

1. **Every PR MUST link an approved issue** — no exceptions
2. **Every PR MUST have exactly one `type:*` label**
3. **All required automated checks must pass** before merge is possible
4. **Blank PRs without issue linkage will be blocked** by GitHub Actions
5. **The PR targets the canonical repository, the branch lives in your fork** — `origin` is your fork, `upstream` is the canonical repository. A push straight to the canonical repository is refused, either by permissions or by the ruleset with `GH013`; do not retry it or switch destinations silently
6. **Applying the `type:*` label needs maintainer triage access** on the canonical repository. When the API refuses, ask a maintainer instead of retrying

---

## Workflow

```
1. Verify every linked issue has `status:approved`, a real assignee, and the proportional evidence defined in CONTRIBUTING.md; the PR author need not match the assignee
2. Create branch from the canonical `main` (`upstream/main`), with `origin` pointing at your fork: feat/*, fix/*, docs/*, refactor/*, chore/*
3. Implement changes
4. For behavior changes, run a focused regression test and affected package tests locally; record commands and outcomes. For docs-only changes, record N/A for Go tests and verify the documentation.
5. Check every changed path against the [Transient Artifact Policy](../../CONTRIBUTING.md#transient-artifact-policy)
6. Push the branch to your fork, then open the PR against the canonical repository using the template
7. Add exactly one type:* label — this needs a maintainer with triage access on the canonical repository, so ask for it rather than retrying
8. After pushing/opening the PR, wait for GitHub CI's full unit/E2E/lint and applicable platform checks; report results only after they run
```

---

## Branch Naming (enforced by GitHub ruleset)

Branch names are validated by a GitHub ruleset. Pushes that don't match **will be rejected**.

**Pattern:** `^(feat|fix|chore|docs|style|refactor|perf|test|build|ci|revert)\/[a-z0-9._-]+$`

| Type | Branch pattern | Example |
|------|---------------|---------|
| Feature | `feat/<description>` | `feat/json-export-command` |
| Bug fix | `fix/<description>` | `fix/duplicate-observation-insert` |
| Chore | `chore/<description>` | `chore/bump-bubbletea-v0.26` |
| Docs | `docs/<description>` | `docs/api-reference-update` |
| Style | `style/<description>` | `style/fix-tui-alignment` |
| Refactor | `refactor/<description>` | `refactor/extract-query-sanitizer` |
| Performance | `perf/<description>` | `perf/optimize-fts5-queries` |
| Test | `test/<description>` | `test/add-sync-coverage` |
| Build | `build/<description>` | `build/update-go-toolchain` |
| CI | `ci/<description>` | `ci/split-e2e-job` |
| Revert | `revert/<description>` | `revert/broken-migration` |

**Rules:**
- Description MUST be lowercase
- Only `a-z`, `0-9`, `.`, `_`, `-` allowed in description
- No uppercase, no spaces, no special characters

---

## PR Body Format

The PR template is at `.github/PULL_REQUEST_TEMPLATE.md`. Every PR body MUST contain:

### 1. Linked Issue (REQUIRED)

```markdown
Closes #<issue-number>
```

Valid keywords: `Closes #N`, `Fixes #N`, `Resolves #N` (case insensitive).
Every linked issue MUST have `status:approved` and a real assignee. Approval means a maintainer accepted proportional evidence: scope/checks for trivial changes; current-remote-main SHA, reproduction, root cause, and regression tests for bugs; approved design, non-goals, and independent review for high-risk changes. Evidence can live in the issue; no separate design document is required by default.

Existing PRs must comply before merge, without waiting for backlog-wide migration. Re-run native validation after issue approval/assignment changes; do not rely on stale green checks.

### 2. PR Type (REQUIRED)

Check exactly ONE in the template and add the matching label:

| Checkbox | Label to add |
|----------|-------------|
| Bug fix | `type:bug` |
| New feature | `type:feature` |
| Documentation only | `type:docs` |
| Code refactoring | `type:refactor` |
| Maintenance/tooling | `type:chore` |
| Breaking change | `type:breaking-change` |

### 3. Summary

1-3 bullet points of what the PR does.

### 4. Changes Table

```markdown
| File | Change |
|------|--------|
| `path/to/file` | What changed |
```

### 5. Test Plan

```markdown
- [ ] Focused regression (behavior change): `<actual command>` — `<actual outcome or N/A: documentation-only>`
- [ ] Affected package (behavior change): `<actual command>` — `<actual outcome or N/A: documentation-only>`
- [ ] GitHub CI full unit/E2E/lint and applicable platform checks — pending until they run
```

Replace example commands and outcomes with actual evidence. For docs-only changes, record `N/A — documentation-only; no Go behavior changed` for local Go tests and list the documentation checks actually run. Targeted coverage can help find missed branches; no total module coverage or per-PR numeric gate applies. Without a PR (or before pushing), and for high-risk changes, run additional applicable local checks and identify any missing CI evidence.

### 6. Contributor Checklist

All boxes must be checked:
- Linked an approved issue
- Added exactly one `type:*` label
- Recorded actual focused regression and affected package test commands/outcomes for behavior changes, or N/A for docs-only changes
- Ran additional applicable local checks without a PR or for high-risk changes; identified missing CI evidence
- Docs updated if behavior changed
- Conventional commit format
- No `Co-Authored-By` trailers
- Every changed path complies with the [Transient Artifact Policy](../../CONTRIBUTING.md#transient-artifact-policy)

---

## Automated Checks

The six required contexts are listed in [CONTRIBUTING.md](../../CONTRIBUTING.md#step-4-automated-pr-checks); all six must pass before merge. Lint, transient-artifact checks, `Windows Setup Test`, and `Cloud Sync Wrapper Tests (Windows)` run for PRs but not merge groups, and are not among the six required contexts. Report their actual outcomes without treating pending checks as passed.

| Check | Job name | What it verifies |
|-------|----------|-----------------|
| PR Validation | `Check Issue Reference` | Body contains `Closes/Fixes/Resolves #N` |
| PR Validation | `Check Issue Has status:approved` | Every linked issue is approved and assigned |
| PR Label Policy | `Check PR Has type:* Label` | PR has exactly one `type:*` label |
| Transient Artifact Check | `Check PR Has No Transient Artifacts` | PR files comply with the [Transient Artifact Policy](../../CONTRIBUTING.md#transient-artifact-policy) |
| CI | `Unit Tests` | `go test ./...` passes |
| CI | `E2E Tests` | `go test -tags e2e ./internal/server/...` passes |
| CI | `Plugin Tests` | `npm test` passes in `plugin/pi` |
| CI | `Lint` | golangci-lint reports no new findings in Go changes |
| CI | `Windows Setup Test` | Windows setup preserves absolute paths and MCP job-object parent-lifetime tests pass |
| CI | `Cloud Sync Wrapper Tests (Windows)` | Cloud sync wrapper and missing-PowerShell-Engram tests pass on Windows |

---

## Conventional Commits (enforced by GitHub ruleset)

Commit messages are validated by a GitHub ruleset. Commits that don't match **will be rejected**.

**Pattern:** `^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test)(\([a-z0-9\._-]+\))?!?: .+`

**Format:**
```
<type>(<optional-scope>): <description>
<type>(<optional-scope>)!: <description>   ← breaking change
```

### Allowed types

| Type | Purpose | PR label |
|------|---------|----------|
| `feat` | New feature | `type:feature` |
| `fix` | Bug fix | `type:bug` |
| `docs` | Documentation only | `type:docs` |
| `refactor` | Code refactoring | `type:refactor` |
| `chore` | Maintenance, deps | `type:chore` |
| `style` | Formatting, whitespace | `type:chore` |
| `perf` | Performance improvement | `type:refactor` |
| `test` | Adding/fixing tests | `type:chore` |
| `build` | Build system changes | `type:chore` |
| `ci` | CI/CD changes | `type:chore` |
| `revert` | Revert previous commit | *(match original type)* |
| `feat!` / `fix!` | Breaking change | `type:breaking-change` |

### Rules
- Type MUST be one of the listed values
- Scope is optional, lowercase, allows `a-z`, `0-9`, `.`, `_`, `-`
- `!` before `:` marks a breaking change
- Description MUST start after `: ` (colon + space)

### Examples
```
feat(cli): add --json flag to session list command
fix(store): prevent duplicate observation insert on retry
docs(contributing): update workflow documentation
refactor(internal): extract search query sanitizer
chore(deps): bump github.com/charmbracelet/bubbletea to v0.26
style(tui): fix alignment in session detail view
perf(store): optimize FTS5 query for large datasets
test(sync): add coverage for conflict resolution
ci(workflows): split e2e into separate job
fix!: change session ID format
```

### Invalid examples (will be rejected)
```
Fix bug                          ← no type prefix
feat: Add login                  ← description should be lowercase
FEAT(cli): add flag              ← type must be lowercase
feat (cli): add flag             ← no space before scope
feat(CLI): add flag              ← scope must be lowercase
update docs                      ← no conventional commit format
```

---

## Commands

```bash
# Identify the remotes before publishing. origin must be your fork and upstream
# the canonical repository; verify with git remote -v before pushing anything.
# A plain clone of your fork has only origin, and forking on the GitHub web
# interface changes nothing in a local clone. If upstream is absent, add it:
git remote add upstream <canonical-repository-url>
# Ensure origin points at your fork:
git remote set-url origin <your-fork-url>
git remote -v
git fetch upstream

# Branch from the canonical main, not from a local main that may be stale.
git checkout -b feat/my-feature upstream/main

# For behavior changes, run actual focused regression and affected package commands:
# go test ./internal/store -run '<actual regression test name>'
# go test ./internal/store
# Record actual commands and outcomes in the PR test plan.
# For docs-only changes, record N/A for Go tests and the documentation checks run.
# Without a PR or for high-risk changes, run additional applicable local checks.
# After pushing, GitHub CI runs broad unit/E2E/lint and applicable platform checks;
# do not claim their results until the checks actually pass.

# Push to your fork, never to the canonical repository. A direct push to the
# canonical repository is refused, by permissions or by its ruleset with GH013.
git push -u origin feat/my-feature

# Open the PR against the canonical repository, explicitly, so the destination is
# never inferred from the clone you are in.
gh pr create --repo <canonical-owner>/<canonical-repo> \
  --head <your-fork-owner>:feat/my-feature --base main \
  --title "feat(scope): description" --body-file pull_request_body.md

# The type:* label needs triage access to the canonical repository. A contributor
# without it gets "does not have the correct permissions to execute
# AddLabelsToLabelable": ask a maintainer to apply it, and do not retry.
#   Maintainer step (not a contributor step). Name the repository explicitly:
#   with both remotes present, GitHub CLI resolves a bare pull request number
#   against its configured default repository, which may be the fork.
#   gh pr edit <pr-number> --repo <canonical-owner>/<canonical-repo> \
#     --add-label "type:feature"
```

## Forks, Remotes, and Publication

A pull request targets the canonical repository. A fork is where the branch lives, not where the pull request opens.

| Remote | Points at | Holds |
|---------|-----------|-------|
| `origin` | your fork | your branches |
| `upstream` | the canonical repository | the canonical `main` |

After `git fetch upstream`, branching from `upstream/main` uses the current canonical `main`; this workflow does not require synchronizing your fork's `main`. Branching from a stale local or fork `main` instead may omit recent canonical changes, but does not necessarily produce an unclean diff.

### Authorizations are separate

| Action | Who performs it |
|--------|-----------------|
| Create or use a fork | you, once authorized to create or use that fork |
| Push commits to a branch in your fork | you, once separately authorized to push those commits there |
| Open or update a pull request in the canonical repository | you, once authorized to publish there |
| Apply the `type:*` label to a pull request in the canonical repository | a maintainer with triage access |
| Merge a pull request | a maintainer |

Request the specific action you need. Authorization to create or use a fork does not authorize pushing commits to it. Being authorized to open a pull request is not authorization to label it or to merge it.

### When a push to the canonical repository is rejected

Do not push branches to the canonical repository. It fails in one of two ways, depending on your access:

- **Without push access** — the remote refuses the push: `ERROR: Permission to <canonical-owner>/<canonical-repo>.git denied to <you>`.
- **With push access** — the repository's ruleset rejects the new branch: the push fails with `GH013` and the required checks are reported as expected.

Both are the controls working as designed.

- Do not retry the push, silently switch the destination, use an exempt branch namespace, fabricate checks, or change the ruleset.
- Push the branch to your fork and open the pull request from there.
- If neither the fork nor the label step is authorized for your account, stop and ask a maintainer for an authorized route, and record the rejection on the issue as evidence.

### Dependent pull requests

A dependent pull request is a second pull request whose base branch is another contributor's branch. **This repository has no validated procedure for delivering one from a fork, so treat it as requiring maintainer guidance rather than something to work around.**

For a pull request targeting the canonical repository, the base branch must exist in the canonical repository. A fork-only branch can supply the head, not the base. Targeting a fork instead is a distinct, unvalidated workflow requiring maintainer guidance. The diff is computed against the selected base, but that alone does not require the parent pull request to merge first. Who may create the canonical base branch and how to sequence merges are maintainer workflow decisions that this skill does not make for you.

Ask before you start one. For a pull request targeting the canonical repository, do not use a base branch that does not exist in that repository. Pushing a branch to your fork does not make it a valid base for that pull request.

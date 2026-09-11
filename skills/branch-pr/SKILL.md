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

---

## Workflow

```
1. Verify issue has `status:approved` label
2. Create branch: feat/*, fix/*, docs/*, refactor/*, chore/*
3. Implement changes
4. Run tests locally (unit + e2e)
5. Check every changed path against the [Transient Artifact Policy](../../CONTRIBUTING.md#transient-artifact-policy)
6. Open PR using the template
7. Add exactly one type:* label
8. Wait for all required automated checks to pass
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
The linked issue MUST have the `status:approved` label.

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
- [x] Unit tests pass locally: `go test ./...`
- [x] E2E tests pass locally: `go test -tags e2e ./internal/server/...`
- [x] Manually tested the affected functionality
```

### 6. Contributor Checklist

All boxes must be checked:
- Linked an approved issue
- Added exactly one `type:*` label
- Ran unit tests locally
- Ran e2e tests locally
- Docs updated if behavior changed
- Conventional commit format
- No `Co-Authored-By` trailers
- Every changed path complies with the [Transient Artifact Policy](../../CONTRIBUTING.md#transient-artifact-policy)

---

## Automated Checks (all required checks must pass)

| Check | Job name | What it verifies |
|-------|----------|-----------------|
| PR Validation | `Check Issue Reference` | Body contains `Closes/Fixes/Resolves #N` |
| PR Validation | `Check Issue Has status:approved` | Linked issue has `status:approved` |
| PR Validation | `Check PR Has type:* Label` | PR has exactly one `type:*` label |
| Transient Artifact Check | `Check PR Has No Transient Artifacts` | PR files comply with the [Transient Artifact Policy](../../CONTRIBUTING.md#transient-artifact-policy) |
| CI | `Unit Tests` | `go test ./...` passes |
| CI | `E2E Tests` | `go test -tags e2e ./internal/server/...` passes |
| CI | `Plugin Tests` | `npm test` passes in `plugin/pi` |
| CI | `Lint` | golangci-lint reports no new findings in Go changes |

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
# Create branch
git checkout -b feat/my-feature main

# Run tests before pushing
go test ./...                                    # unit tests
go test -tags e2e ./internal/server/...          # e2e tests

# Push and create PR
git push -u origin feat/my-feature
gh pr create --title "feat(scope): description" --body "Closes #N"

# Add type label to PR
gh pr edit <pr-number> --add-label "type:feature"
```

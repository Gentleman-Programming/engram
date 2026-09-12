---
name: engram-commit-hygiene
description: >
  Commit and branch naming standards for Engram contributors, enforced by GitHub rulesets.
  Trigger: Any commit creation, review, or branch cleanup.
license: Apache-2.0
metadata:
  author: gentleman-programming
  version: "2.0"
---

## When to Use

Use this skill when:
- Creating commits
- Creating or naming branches
- Reviewing commit history in a PR
- Cleaning up staged changes

---

## Critical Rules

1. **Commit messages MUST follow Conventional Commits** — enforced by GitHub ruleset, rejected on push if invalid
2. **Branch names MUST follow `type/description` format** — enforced by GitHub ruleset, rejected on push if invalid
3. Keep one logical change per commit
4. Message should explain **why**, not only what
5. **NEVER** include `Co-Authored-By` trailers
6. **NEVER** commit paths prohibited by the [Transient Artifact Policy](../../CONTRIBUTING.md#transient-artifact-policy)

---

## Commit Message Format (enforced by GitHub ruleset)

**Regex:** `^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test)(\([a-z0-9\._-]+\))?!?: .+`

```
<type>(<optional-scope>): <description>
<type>(<optional-scope>)!: <description>   ← breaking change
```

### Allowed Types

| Type | Purpose |
|------|---------|
| `feat` | New feature |
| `fix` | Bug fix |
| `docs` | Documentation only |
| `refactor` | Code refactoring (no behavior change) |
| `chore` | Maintenance, dependencies |
| `style` | Formatting, whitespace |
| `perf` | Performance improvement |
| `test` | Adding or fixing tests |
| `build` | Build system changes |
| `ci` | CI/CD pipeline changes |
| `revert` | Revert a previous commit |

### Rules
- Type MUST be lowercase, one of the listed values
- Scope is optional, lowercase, allows `a-z`, `0-9`, `.`, `_`, `-`
- `!` before `:` marks a breaking change
- Description MUST start after `: ` (colon + space)
- Description should be imperative mood ("add", not "added" or "adds")

### Valid Examples
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

### Invalid Examples (push will be rejected)
```
Fix bug                          ← no type prefix
feat: Add login                  ← description should be lowercase
FEAT(cli): add flag              ← type must be lowercase
feat (cli): add flag             ← no space before scope
feat(CLI): add flag              ← scope must be lowercase
update docs                      ← no conventional commit format
```

---

## Branch Naming Format (enforced by GitHub ruleset)

**Regex:** `^(feat|fix|chore|docs|style|refactor|perf|test|build|ci|revert)\/[a-z0-9._-]+$`

```
<type>/<description>
```

### Allowed Types

`feat`, `fix`, `chore`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `revert`

### Rules
- Type MUST be one of the listed values
- Description MUST be lowercase
- Only `a-z`, `0-9`, `.`, `_`, `-` allowed in description
- No uppercase, no spaces, no special characters

### Valid Examples
```
feat/json-export-command
fix/duplicate-observation-insert
docs/api-reference-update
refactor/extract-query-sanitizer
chore/bump-bubbletea-v0.26
ci/split-e2e-job
```

### Invalid Examples (push will be rejected)
```
feature/add-login                ← "feature" not allowed, use "feat"
fix/Add-Login                    ← uppercase not allowed
my-branch                        ← no type prefix
fix_something                    ← missing "/" separator
```

---

## Pre-Commit Checklist

- [ ] Commit message matches conventional commits format
- [ ] Branch name matches `type/description` format
- [ ] Diff matches commit scope (no unrelated changes)
- [ ] No secrets, credentials, or `.env` files
- [ ] Every changed path complies with the [Transient Artifact Policy](../../CONTRIBUTING.md#transient-artifact-policy)
- [ ] No `Co-Authored-By` trailers
- [ ] Tests relevant to the change pass

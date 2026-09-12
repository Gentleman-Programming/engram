---
name: engram-issue-creation
description: >
  Issue creation workflow for Engram following the issue-first enforcement system.
  Trigger: When creating a GitHub issue, reporting a bug, or requesting a feature.
license: Apache-2.0
metadata:
  author: gentleman-programming
  version: "1.0"
---

## When to Use

Use this skill when:
- Creating a GitHub issue (bug report, feature request, docs improvement, or tracked question)
- Helping a contributor file an issue
- Triaging or approving issues as a maintainer

---

## Critical Rules

1. **Blank issues are disabled** — MUST use the matching bug, feature, docs, or tracked-question template
2. **Every issue gets `status:needs-review` automatically** on creation
3. **A maintainer MUST replace `status:needs-review` with `status:approved`** before any PR can be opened
4. **General questions go to [Discussions](https://github.com/Gentleman-Programming/engram/discussions)**; questions requiring tracked work use `type:question`

---

## Workflow

```
1. Consider searching existing issues for duplicates
2. Choose the correct template (Bug Report, Feature Request, Documentation Improvement, or Tracked Question)
3. Fill in ALL required fields
4. Submit → issue gets status:needs-review automatically
5. Wait for a maintainer to replace status:needs-review with status:approved
6. Only then open a PR linking this issue
```

---

## Issue Templates

### Bug Report

Template: `.github/ISSUE_TEMPLATE/bug_report.yml`
Auto-labels: `type:bug`, `status:needs-review`

#### Required Fields

| Field | Description |
|-------|-------------|
| **Bug Description** | Clear description of the bug |
| **Steps to Reproduce** | Numbered steps to reproduce |
| **Expected Behavior** | What should have happened |
| **Actual Behavior** | What happened instead (include errors/logs) |
| **Operating System** | Dropdown: macOS, Linux variants, Windows, WSL |
| **Engram Version** | Output of `engram version` |
| **Agent / Client** | Dropdown: Claude Code, OpenCode, Gemini CLI, Cursor, Windsurf, Other |

#### Optional Fields

| Field | Description |
|-------|-------------|
| **Relevant Logs** | Log output (auto-formatted as code block) |
| **Additional Context** | Screenshots, workarounds, extra info |

#### Example — Bug Report via CLI

```bash
gh issue create --template "bug_report.yml" \
  --title "fix(store): duplicate observations on concurrent saves" \
  --body "
### Bug Description
When two agents save observations concurrently, duplicates are created.

### Steps to Reproduce
1. Start two engram instances pointing to the same DB
2. Both save an observation with the same title simultaneously
3. Query observations — duplicates appear

### Expected Behavior
The second save should upsert, not insert a duplicate.

### Actual Behavior
Two identical observations exist with different IDs.

### Operating System
macOS

### Engram Version
0.3.1

### Agent / Client
Claude Code

### Relevant Logs
\`\`\`
UNIQUE constraint failed: observations.title
\`\`\`
"
```

---

### Feature Request

Template: `.github/ISSUE_TEMPLATE/feature_request.yml`
Auto-labels: `type:feature`, `status:needs-review`

#### Required Fields

| Field | Description |
|-------|-------------|
| **Problem Description** | The pain point this feature solves |
| **Proposed Solution** | How it should work from the user's perspective |
| **Affected Area** | Dropdown: CLI, MCP Server, TUI, Store, Sync, Skills, Documentation, Other |

#### Optional Fields

| Field | Description |
|-------|-------------|
| **Alternatives Considered** | Other approaches or workarounds |
| **Additional Context** | Mockups, examples, references |

#### Example — Feature Request via CLI

```bash
gh issue create --template "feature_request.yml" \
  --title "feat(cli): add --json flag to mem search" \
  --body "
### Problem Description
When scripting with engram, parsing the human-readable output of mem search is fragile. There's no machine-readable output format.

### Proposed Solution
Add a \`--json\` flag to \`engram mem search\` that outputs results as JSON.

Example:
\`\`\`bash
engram mem search \"auth middleware\" --json
\`\`\`

Expected output:
\`\`\`json
[{\"id\": 42, \"title\": \"JWT auth middleware\", \"type\": \"decision\", ...}]
\`\`\`

### Affected Area
CLI (commands, flags)

### Alternatives Considered
Using \`jq\` to parse the current output, but it's unreliable since the format isn't structured.
"
```

---

### Tracked Question

Template: `.github/ISSUE_TEMPLATE/tracked_question.yml`
Auto-labels: `type:question`, `status:needs-review`

Use this form only when the answer requires maintainer investigation, a repository change, or a durable decision. Send general questions and support to Discussions.

Required fields: the question, why issue tracking is needed, and the affected area. Additional context is optional.

---

## Label System

### Applied Automatically on Issue Creation

| Template | Labels added |
|----------|-------------|
| Bug Report | `type:bug`, `status:needs-review` |
| Feature Request | `type:feature`, `status:needs-review` |
| Documentation Improvement | `type:docs`, `status:needs-review` |
| Tracked Question | `type:question`, `status:needs-review` |

### Applied by Maintainers

| Label | When to apply |
|-------|--------------|
| `status:approved` | Issue accepted for implementation — PRs can now be opened |
| `priority:critical` | Critical bug or urgent feature |
| `priority:high` | High priority |
| `priority:medium` | Important but not blocking |
| `priority:low` | Nice to have |
| `status:possible-duplicate` | Duplicate under evaluation |
| `status:wontfix` | Closed without implementation, including confirmed duplicates |
| `resolution:duplicate` | Confirmed duplicate after closure |

---

## Maintainer Approval Workflow

```
1. New issue arrives with status:needs-review
2. Review the issue — is it valid, clear, and in scope?
3. If YES → replace status:needs-review with status:approved
4. If NO → comment with reason, close if needed
5. Contributor can now open a PR linking this issue
```

---

## Decision Tree

```
Is it a bug?                    → Use Bug Report template
Is it a new feature/improvement? → Use Feature Request template
Is it a general question?       → Use Discussions, NOT issues
Is it a tracked question?       → Use the Tracked Question template
Is it a possible duplicate?     → Replace the current status with status:possible-duplicate
Is it a confirmed duplicate?    → Close it, replace evaluation status with status:wontfix, then add resolution:duplicate
```

---

## Commands

```bash
# Consider searching existing issues before creating to avoid duplicates
gh issue list --search "keyword"

# Create bug report
gh issue create --template "bug_report.yml" --title "fix(scope): description"

# Create feature request
gh issue create --template "feature_request.yml" --title "feat(scope): description"

# Create documentation improvement
gh issue create --template "docs_improvement.yml" --title "docs(scope): description"

# Create tracked question
gh issue create --template "tracked_question.yml" --title "question(scope): description"

# Maintainer: approve an issue
gh issue edit <number> --remove-label "status:needs-review" --add-label "status:approved"

# Maintainer: add priority
gh issue edit <number> --add-label "priority:high"

# Maintainer: replace every active status while evaluating a possible duplicate
set -euo pipefail

other_statuses="$(gh issue view <number> --json labels --jq '.labels[].name | select(startswith("status:") and . != "status:possible-duplicate")')"
status_args=(--add-label "status:possible-duplicate")
if [[ -n "$other_statuses" ]]; then
  status_args+=(--remove-label "${other_statuses//$'\n'/,}")
fi
gh issue edit <number> "${status_args[@]}"

updated_statuses="$(gh issue view <number> --json labels --jq '.labels[].name | select(startswith("status:"))')"
if [[ "$updated_statuses" != "status:possible-duplicate" ]]; then
  echo "status replacement could not be confirmed; stop without retrying" >&2
  exit 1
fi

# Maintainer: close a confirmed duplicate, then replace evaluation status with terminal status and resolution
gh issue close <number> --reason "not planned" --comment "Closing as duplicate of #<canonical>."
gh issue edit <number> --remove-label "status:possible-duplicate" --add-label "status:wontfix,resolution:duplicate"
```

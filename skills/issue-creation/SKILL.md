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
- Creating a GitHub issue (bug report or feature request)
- Helping a contributor file an issue
- Triaging or approving issues as a maintainer

---

## Critical Rules

1. **Blank issues are disabled** — MUST use a template (bug report or feature request)
2. **Every issue gets `status:needs-review` automatically** on creation
3. **A maintainer MUST add `status:approved`** before any PR can be opened
4. **Questions go to [Discussions](https://github.com/Gentleman-Programming/engram/discussions)**, not issues

---

## Workflow

```
1. Consider searching existing issues for duplicates
2. Choose the correct template (Bug Report or Feature Request)
3. Fill in ALL required fields
4. Submit → issue gets status:needs-review automatically
5. Wait for maintainer to add status:approved
6. Only then open a PR linking this issue
```

---

## Issue Templates

### Bug Report

Template: `.github/ISSUE_TEMPLATE/bug_report.yml`
Auto-labels: `bug`, `status:needs-review`

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
Auto-labels: `enhancement`, `status:needs-review`

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

## Label System

### Applied Automatically on Issue Creation

| Template | Labels added |
|----------|-------------|
| Bug Report | `bug`, `status:needs-review` |
| Feature Request | `enhancement`, `status:needs-review` |

### Applied by Maintainers

| Label | When to apply |
|-------|--------------|
| `status:approved` | Issue accepted for implementation — PRs can now be opened |
| `priority:high` | Critical bug or urgent feature |
| `priority:medium` | Important but not blocking |
| `priority:low` | Nice to have |

---

## Maintainer Approval Workflow

```
1. New issue arrives with status:needs-review
2. Review the issue — is it valid, clear, and in scope?
3. If YES → add status:approved label
4. If NO → comment with reason, close if needed
5. Contributor can now open a PR linking this issue
```

---

## Decision Tree

```
Is it a bug?                    → Use Bug Report template
Is it a new feature/improvement? → Use Feature Request template
Is it a question?               → Use Discussions, NOT issues
Is it a duplicate?              → Link to existing issue, close
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

# Maintainer: approve an issue
gh issue edit <number> --add-label "status:approved"

# Maintainer: add priority
gh issue edit <number> --add-label "priority:high"
```

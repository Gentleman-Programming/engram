---
name: engram-backlog-triage
description: >
  Backlog triage protocol for Engram: audit open issues and PRs, classify each item,
  infer maintainer ideology from comments, and produce an actionable disposition report.
  Trigger: Auditing open issues or PRs, triaging the backlog, reviewing contributor
  submissions as a maintainer, or applying triage to any GitHub repo.
license: Apache-2.0
metadata:
  author: gentleman-programming
  version: "1.0"
---

## When to Use

Use this skill when:
- Running a full backlog audit on Engram (or any repo)
- Deciding merge / request-changes / close / needs-design / approve-issue
- Cleaning noise from the issue tracker
- Prioritizing what to act on next

---

## Maintainer Philosophy (Engram)

These are the non-negotiable product values. Every triage decision is filtered through them.

| Principle | What it means in practice |
|-----------|--------------------------|
| **Zero-config** | Works out of the box. No required flags, env vars, or setup beyond install. |
| **Local-first** | Data lives in `~/.engram/engram.db`. No cloud dependency by default. |
| **Single binary** | One `engram` binary. No daemon, no service, no secondary processes needed. |
| **Terminal-first** | CLI and TUI are the primary UX. No web dashboard, no Electron. |
| **Thin adapters** | Plugin scripts (Claude, OpenCode, Gemini, Codex) are thin shims — logic lives in Go core. |
| **Issue-first** | Every PR must link a `status:approved` issue. No approved issue → no PR. |
| **Evidence-based reviews** | Request changes with specific, actionable items. No vague "needs improvement". |
| **Tight scope** | Reject features that expand Engram's surface area without a compelling case. |
| **Small focused contributions** | Prefer 50-line PRs solving one problem over 500-line PRs solving five. |
| **Reject vague/scope-breaking work** | Close scope-creep issues and PRs that turn Engram into something else. |

---

## Disposition Classification

Assign exactly ONE disposition to each issue or PR:

| Disposition | When to use |
|-------------|-------------|
| **MERGE** | PR is correct, scoped, tests pass, linked approved issue. Merge immediately. |
| **REQUEST CHANGES** | PR has the right idea but needs specific fixes. List each item. |
| **CLOSE** | Noise, duplicate, vague, scope-breaking, no approved issue, or stale with no activity. |
| **NEEDS DESIGN** | Idea is valid but architectural decision required before any PR is welcome. Open discussion or design issue first. |
| **APPROVE ISSUE** | Issue is valid, clear, reproducible/specific, and in scope. Add `status:approved` label. |
| **REJECT ISSUE** | Vague, duplicate, scope-breaking, or belongs in Discussions. Close with explanation. |

---

## Operating Phases

### Phase 1 — Fetch the Backlog

```bash
# All open issues with labels and comments
gh issue list --repo <owner/repo> --state open \
  --json number,title,labels,author,comments,body \
  --limit 100

# All open PRs with labels and review state
gh pr list --repo <owner/repo> --state open \
  --json number,title,labels,author,body,reviews,commits \
  --limit 50

# Check a specific issue in detail
gh issue view <number> --repo <owner/repo> --json number,title,body,labels,comments

# Check a specific PR in detail
gh pr view <number> --repo <owner/repo> --json number,title,body,labels,files,reviews
```

### Phase 2 — Classify Each Item

For every issue, answer:

```
1. Is it a real bug with reproduction steps? → candidate for APPROVE ISSUE
2. Is it a clear feature with a problem statement? → candidate for APPROVE ISSUE
3. Is it vague, a question, or a discussion? → REJECT ISSUE (redirect to Discussions)
4. Is it a duplicate? → REJECT ISSUE (link original, close)
5. Does it break zero-config / local-first / single-binary? → REJECT ISSUE
6. Does it need architectural decision before a PR? → NEEDS DESIGN
```

For every PR, answer:

```
1. Does it link a status:approved issue? → if not → CLOSE (process violation)
2. Does it have exactly one type:* label? → if not → REQUEST CHANGES
3. Do all 5 CI checks pass? → if not → REQUEST CHANGES (list failures)
4. Is the scope tight (one issue, minimal diff)? → if sprawling → REQUEST CHANGES
5. Does it follow conventional commits + branch naming? → if not → REQUEST CHANGES
6. Is the change correct and well-tested? → if yes → MERGE
```

### Phase 3 — Infer Ideology from Maintainer Comments

Look for maintainer responses (MEMBER or OWNER association) in issue comments. Extract:

- What the maintainer approved and how they framed it
- What the maintainer redirected (Questions → Discussions, etc.)
- What the maintainer scoped down ("Best place for it: add a section in DOCS.md")
- What the maintainer welcomed vs deferred ("I'll keep this issue open to track that")

Use these patterns to calibrate your triage against the actual maintainer stance, not just the written philosophy.

```bash
# Filter for maintainer responses
gh issue view <number> --repo <owner/repo> --json comments \
  --jq '.comments[] | select(.authorAssociation == "MEMBER" or .authorAssociation == "OWNER") | {author: .author.login, body: .body}'
```

### Phase 4 — Prioritize

Rank items within each disposition bucket:

**Quick wins (act immediately):**
- Real bugs with clear reproduction steps + no linked PR yet
- PRs that are correct, scoped, and just need a label or minor fix
- Docs PRs that are accurate and unambiguous

**Process blockers (fix the pipeline):**
- PRs missing `status:approved` linkage (close + explain)
- PRs missing `type:*` label (request changes)
- Issues with `status:needs-review` that have been waiting > 7 days

**Real bugs (high priority issues):**
- Reproducible crashes, data loss, or broken core workflows
- Issues with multiple confirming comments from different users

**Architectural proposals (schedule separately):**
- Issues tagged NEEDS DESIGN
- Proposals that affect the binary's interface, sync protocol, or plugin contract

**Noise (close immediately):**
- Issues without reproduction steps
- Feature requests that expand scope without evidence of demand
- Questions that belong in Discussions
- Vague reports ("it doesn't work")

### Phase 5 — Produce the Report

Output a structured triage report:

```markdown
## Triage Report — <repo> — <date>

### Summary
- Open issues: N | Open PRs: N
- To merge: N | To request changes: N | To close: N
- To approve: N | To reject: N | Needs design: N

### PRs

| # | Title | Disposition | Reason |
|---|-------|-------------|--------|
| 89 | fix(mcp): update config example | MERGE | Correct, scoped, CI pending only |
| 98 | feat(sync): selective export | REQUEST CHANGES | No approved issue linked |
| 80 | feat(nix): Introduce flake.nix | CLOSE | No approved issue; nix out of scope for single-binary |

### Issues

| # | Title | Disposition | Reason |
|---|-------|-------------|--------|
| 93 | Windows false positive (Defender) | APPROVE ISSUE | Real user-facing bug, 3 confirmations |
| 99 | FTS5 trigram SQL logic error | APPROVE ISSUE | Specific bug, reproducible |
| 104 | Project aliasing system | NEEDS DESIGN | Scope question: conflicts with local-first project name resolution |
| 97 | Auto-generate docs from memory | REJECT ISSUE | Vague scope, no concrete problem statement |
| 81 | Remove Projects / local-only notes | REJECT ISSUE | Ambiguous, belongs in Discussions |

### Suggested Comments

#### PR #N — REQUEST CHANGES
> Thanks for this! A few items before this can merge:
> - [ ] Link an approved issue (`Closes #N`) — no PR can merge without one
> - [ ] Add exactly one `type:*` label
> - [ ] Rebase on `main` to resolve the failing CI check

#### Issue #N — APPROVE ISSUE
> This is clear, reproducible, and in scope. Adding `status:approved` — feel free to open a PR linking this issue.

#### Issue #N — REJECT ISSUE
> Thanks for the report! This looks more like a question/discussion topic than a bug or feature request.
> Please continue the conversation in [Discussions](https://github.com/<owner/repo>/discussions).
> Closing this issue — feel free to re-open if you can reproduce it as a concrete bug with steps.

#### Issue #N — NEEDS DESIGN
> Good idea, but this touches the <area> architecture. Before a PR makes sense here,
> let's nail down the design. I'll leave this open for discussion — feel free to propose
> an approach in the comments.
```

---

## Quick-Action Commands

```bash
# Approve an issue (add status:approved)
gh issue edit <number> --repo <owner/repo> --add-label "status:approved"

# Add priority to an issue
gh issue edit <number> --repo <owner/repo> --add-label "priority:high"

# Close an issue with a comment
gh issue close <number> --repo <owner/repo> \
  --comment "Thanks! This looks like a discussion topic rather than a bug. Please continue in Discussions: https://github.com/<owner/repo>/discussions"

# Request changes on a PR
gh pr review <number> --repo <owner/repo> --request-changes \
  --body "Please link an approved issue (Closes #N) and add exactly one type:* label."

# Approve a PR
gh pr review <number> --repo <owner/repo> --approve \
  --body "Looks good — scoped, tested, and linked to the approved issue."

# Merge a PR (squash)
gh pr merge <number> --repo <owner/repo> --squash --delete-branch

# Add a label to a PR
gh pr edit <number> --repo <owner/repo> --add-label "type:bug"

# List issues needing review (no status:approved yet)
gh issue list --repo <owner/repo> --label "status:needs-review" --state open
```

---

## Reusable Prompt Template

Copy this block to apply the triage workflow in any repository:

```
You are a maintainer triage agent. Your job is to audit the full open issue and PR backlog
for <owner/repo> and produce a disposition report.

## Maintainer Philosophy
<paste the philosophy table from this skill, adapted to the target repo>

## Steps
1. Fetch all open issues: `gh issue list --repo <owner/repo> --state open --json number,title,labels,author,comments,body --limit 100`
2. Fetch all open PRs: `gh pr list --repo <owner/repo> --state open --json number,title,labels,author,body,reviews --limit 50`
3. For each item, check for maintainer comments (authorAssociation: MEMBER or OWNER) to infer current stance.
4. Classify each item as: MERGE / REQUEST CHANGES / CLOSE / NEEDS DESIGN / APPROVE ISSUE / REJECT ISSUE
5. Prioritize within buckets: quick wins → process blockers → real bugs → architectural proposals → noise
6. Output a structured markdown report with a table per section (PRs, Issues) and suggested comment text for each action item.

## Constraints
- Never approve issues that are vague, duplicates, or belong in Discussions
- Never approve PRs without a linked status:approved issue
- Prefer closing noise over leaving it open and unresolved
- Be specific in REQUEST CHANGES — list each item as a checkbox
- Proposed comments must be warm but clear: acknowledge the contribution, explain the reason, offer a path forward
```

---

## Tradeoffs and Assumptions

- **Ideology is inferred, not computed.** Maintainer comments are the ground truth — written philosophy is secondary. When they conflict, follow the comments.
- **Issue-first is non-negotiable for PRs.** PRs without a `status:approved` issue always get CLOSE, not REQUEST CHANGES, because the contributor skipped the process entirely.
- **Noise should be closed, not left open.** An open issue with no actionable content trains contributors to expect low quality to be tolerated.
- **NEEDS DESIGN is a valid, non-blocking action.** It signals "good idea, wrong time" without rejecting the contributor.
- **This skill targets Engram's workflow.** Adapt the philosophy table and label system when applying the reusable prompt to a different repository.

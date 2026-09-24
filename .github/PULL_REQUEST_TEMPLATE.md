<!-- 
  ⚠️ READ BEFORE SUBMITTING
  
  Every PR must:
  1. Link an approved issue (with status:approved label)
  2. Have exactly one type:* label and only canonical labels
  3. Pass all required automated checks
  
  See CONTRIBUTING.md for the full workflow.
-->

## 🔗 Linked Issue

<!-- REQUIRED: Replace the # below with the issue number. -->
<!-- Automated check: "Check Issue Reference" verifies this exists. -->
<!-- Automated check: "Check Issue Has status:approved" verifies the issue is approved. -->

Closes #

## Lifecycle Prerequisites

- [ ] Canonical closing issue: exactly one approved issue is linked above.
- [ ] Approved investigation and design: the issue records accepted design and non-goals.
- [ ] Issue assignee: the approved issue has an owner.
- [ ] Root-level tests: tests cover the accepted root-level contract.
- [ ] Non-goals and scope: this PR stays within the approved design.
- [ ] Current-main evidence for bugs: the issue records reproduction against remote `main` and its SHA.

---

## 🏷️ PR Type

<!-- REQUIRED: Check exactly ONE type below, then add the matching label to the PR. -->
<!-- Automated check: "Check PR Has type:* Label" verifies canonical labels and cardinality. -->

- [ ] `type:bug` — Bug fix
- [ ] `type:feature` — New feature
- [ ] `type:question` — Question requiring tracked work
- [ ] `type:docs` — Documentation only
- [ ] `type:refactor` — Code refactoring (no behavior change)
- [ ] `type:chore` — Maintenance, dependencies, tooling
- [ ] `type:breaking-change` — Breaking change

---

## 📝 Summary

<!-- What does this PR do? Be concise — 1-3 bullet points. -->

- 

## 📂 Changes

<!-- Key files changed and what was modified in each. -->

| File | Change |
|------|--------|
| `path/to/file` | What changed |

## 🧪 Test Plan

<!-- How did you verify this works? -->

- [ ] Unit tests pass locally: `go test ./...`
- [ ] E2E tests pass locally: `go test -tags e2e ./internal/server/...`
- [ ] Lint passes locally: `make lint`
- [ ] Manually tested the affected functionality

<!-- Describe any manual testing steps: -->

---

## 🤖 Required Checks

These six authoritative contexts must pass before merge:

| Check | What it verifies | Status |
|-------|-----------------|--------|
| **Check Issue Reference** | PR body contains `Closes #N` / `Fixes #N` / `Resolves #N` | ⏳ |
| **Check Issue Has status:approved** | Linked issue has `status:approved` label | ⏳ |
| **Check PR Has type:* Label** | Canonical labels, applicability, and cardinality | ⏳ |
| **Unit Tests** | `go test ./...` passes | ⏳ |
| **E2E Tests** | `go test -tags e2e ./internal/server/...` passes | ⏳ |
| **Plugin Tests** | `npm test` passes in `plugin/pi` | ⏳ |

`Check PR Has No Transient Artifacts` and `Lint` are useful non-required PR checks; complete their guidance before requesting review.

---

## ✅ Contributor Checklist

- [ ] I linked an approved issue above (`Closes #N`)
- [ ] I added exactly **one** `type:*` label to this PR
- [ ] I ran unit tests locally: `go test ./...`
- [ ] I ran e2e tests locally: `go test -tags e2e ./internal/server/...`
- [ ] I ran lint locally: `make lint`
- [ ] Docs updated (if behavior changed)
- [ ] Commits follow [conventional commits](https://www.conventionalcommits.org/) format
- [ ] No `Co-Authored-By` trailers in commits
- [ ] I checked every changed path against the [Transient Artifact Policy](https://github.com/Gentleman-Programming/engram/blob/main/CONTRIBUTING.md#transient-artifact-policy)

---

## 💬 Notes for Reviewers

<!-- Optional: anything the reviewer should know — context, tradeoffs, open questions. -->

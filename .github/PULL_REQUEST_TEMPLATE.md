<!-- 
  ⚠️ READ BEFORE SUBMITTING
  
  Every PR must:
  1. Link an approved issue (with status:approved label)
  2. Have exactly one type:* label
  3. Pass all 5 automated checks
  
  See CONTRIBUTING.md for the full workflow.
-->

## 🔗 Linked Issue

<!-- REQUIRED: Replace the # below with the issue number. -->
<!-- Automated check: "Check Issue Reference" verifies this exists. -->
<!-- Automated check: "Check Issue Has status:approved" verifies the issue is approved. -->

Closes #

---

## 🏷️ PR Type

<!-- REQUIRED: Check exactly ONE type below, then add the matching label to the PR. -->
<!-- Automated check: "Check PR Has type:* Label" verifies the label exists. -->

- [ ] `type:bug` — Bug fix
- [ ] `type:feature` — New feature
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
- [ ] Manually tested the affected functionality

<!-- Describe any manual testing steps: -->

---

## 🤖 Automated Checks

These run automatically and **all must pass** before merge:

| Check | What it verifies | Status |
|-------|-----------------|--------|
| **Check Issue Reference** | PR body contains `Closes #N` / `Fixes #N` / `Resolves #N` | ⏳ |
| **Check Issue Has status:approved** | Linked issue has `status:approved` label | ⏳ |
| **Check PR Has type:\* Label** | PR has exactly one `type:*` label | ⏳ |
| **Unit Tests** | `go test ./...` passes | ⏳ |
| **E2E Tests** | `go test -tags e2e ./internal/server/...` passes | ⏳ |
| **Plugin Tests** | `npm test` passes in `plugin/pi` | ⏳ |

---

## ✅ Contributor Checklist

- [ ] I linked an approved issue above (`Closes #N`)
- [ ] I added exactly **one** `type:*` label to this PR
- [ ] I ran unit tests locally: `go test ./...`
- [ ] I ran e2e tests locally: `go test -tags e2e ./internal/server/...`
- [ ] Docs updated (if behavior changed)
- [ ] Commits follow [conventional commits](https://www.conventionalcommits.org/) format
- [ ] No `Co-Authored-By` trailers in commits

---

## 💬 Notes for Reviewers

<!-- Optional: anything the reviewer should know — context, tradeoffs, open questions. -->

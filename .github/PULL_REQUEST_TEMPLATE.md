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

<!-- Record actual commands and outcomes, not assumed passes. For behavior changes, run a focused regression and affected package tests locally. For docs-only changes, mark Go tests N/A and list documentation checks. Targeted coverage is informative, not a total module coverage or per-PR numeric gate. Without a PR (or before pushing), and for high-risk changes, run additional applicable local checks and report missing CI evidence. -->

- [ ] Focused regression (behavior change): `<actual command>` — `<outcome or N/A: documentation-only>`
- [ ] Affected package tests (behavior change): `<actual command>` — `<outcome or N/A: documentation-only>`
- [ ] Other applicable local checks (including documentation/manual checks): `<actual command or steps>` — `<outcome or N/A with reason>`

<!-- Describe any manual testing steps and checks that could not run: -->

---

## 🤖 Automated Checks

After pushing/opening the PR, GitHub CI runs the broad unit/E2E/lint and applicable platform checks. These automated statuses remain pending until the actual checks run; do not claim a pass from local evidence. Windows checks run for PRs but not merge groups, and are not among the six required contexts. All required checks must pass before merge:

| Check | What it verifies | Status |
|-------|-----------------|--------|
| **Check Issue Reference** | PR body contains `Closes #N` / `Fixes #N` / `Resolves #N` | ⏳ |
| **Check Issue Has status:approved** | Linked issue has `status:approved` label | ⏳ |
| **Check PR Has type:* Label** | Canonical labels, applicability, and cardinality | ⏳ |
| **Check PR Has No Transient Artifacts** | PR files comply with the [Transient Artifact Policy](https://github.com/Gentleman-Programming/engram/blob/main/CONTRIBUTING.md#transient-artifact-policy) | ⏳ |
| **Unit Tests** | `go test ./...` passes | ⏳ |
| **E2E Tests** | `go test -tags e2e ./internal/server/...` passes | ⏳ |
| **Plugin Tests** | `npm test` passes in `plugin/pi` | ⏳ |
| **Lint** | golangci-lint reports no new findings | ⏳ |
| **Windows Setup Test** | Windows setup preserves absolute paths and MCP job-object parent-lifetime tests pass | ⏳ |
| **Cloud Sync Wrapper Tests (Windows)** | Cloud sync wrapper and missing-PowerShell-Engram tests pass on Windows | ⏳ |

---

## ✅ Contributor Checklist

- [ ] I linked an approved issue above (`Closes #N`)
- [ ] I added exactly **one** `type:*` label to this PR
- [ ] I recorded actual focused regression and affected package test commands/outcomes for behavior changes, or N/A for docs-only changes
- [ ] I recorded additional applicable local checks for an unpushed/no-PR or high-risk change, and identified any missing CI evidence
- [ ] Docs updated (if behavior changed)
- [ ] Commits follow [conventional commits](https://www.conventionalcommits.org/) format
- [ ] No `Co-Authored-By` trailers in commits
- [ ] I checked every changed path against the [Transient Artifact Policy](https://github.com/Gentleman-Programming/engram/blob/main/CONTRIBUTING.md#transient-artifact-policy)

---

## 💬 Notes for Reviewers

<!-- Optional: anything the reviewer should know — context, tradeoffs, open questions. -->

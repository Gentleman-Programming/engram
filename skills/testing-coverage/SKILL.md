---
name: engram-testing-coverage
description: >
  TDD and coverage standards for Engram.
  Trigger: When implementing behavior changes in any package.
license: Apache-2.0
metadata:
  author: gentleman-programming
  version: "1.0"
---

## When to Use

Use this skill when:
- Adding new behavior
- Fixing a bug
- Refactoring logic with branch complexity

---

## TDD Loop

1. Write a failing test for the target behavior.
2. Implement the smallest code to pass.
3. Refactor while keeping tests green.
4. Add edge/error-path tests before closing.

---

## Coverage Rules

- Cover happy path + error paths + edge cases.
- Prefer deterministic tests over flaky integration paths.
- Add seams only when branches are impossible to trigger naturally.
- Keep runtime behavior unchanged when adding seams.

---

## Verification Ownership

Verification is evidence about the candidate; CI is the automated execution venue, not a substitute for local focused evidence.

- For behavior changes, run a focused regression test and tests for the affected package(s) locally before proposing the change. Report the commands and outcomes.
- Use targeted coverage when useful to find missed behavior or branches. Do not require a numeric coverage target or total module coverage for every PR.
- After the candidate is pushed to a PR, GitHub CI owns the full unit suite (`go test ./...`), E2E suite (`go test -tags e2e ./internal/server/...`), lint, and applicable platform checks. Do not report CI evidence until those checks actually run.
- When there is no PR or the candidate is unpushed, or when risk justifies more verification, run the additional applicable checks locally. Report checks that could not run as missing evidence rather than claiming CI covered them. Avoid duplicating the broad suite locally by default when PR CI will run it.

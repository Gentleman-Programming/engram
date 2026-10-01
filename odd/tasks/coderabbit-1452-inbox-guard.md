# Feature: coderabbit-1452-inbox-guard

Locator: `odd/tasks/coderabbit-1452-inbox-guard.md` — Engram mirror topic `odd/coderabbit-1452-inbox-guard/tasks` (project `engram`).
Branch: `refresh-1452-merge` (PR #1452).

## Objective

Close the single CodeRabbit actionable finding on PR #1452: `MergeProjectsByName` must refuse the reserved inbox project as a merge destination, using the same error as every other merge path.

## Problem

`mergeProjects` and the explicit-variant path both reject `canonical == ReservedInboxProjectName` with `reserved inbox project cannot be a merge destination` (store.go ~9043, ~8995). `MergeProjectsByName` (store.go ~8873) validates empty names and same-normalized pairs but never checks the reserved target, so an operator-driven by-name merge can move records onto the cloud inbox project.

## Scope

- Add the reserved-inbox rejection to `MergeProjectsByName` right after the same-normalized refusal.
- Regression test: a by-name merge targeting `inbox` is rejected with the exact error; a control by-name merge to a normal target still succeeds.

## Non-goals

- No changes to `mergeProjects`, `MergeExplicitProjectVariants`, or admission rules.
- No schema or migration changes.

## Tasks

- [x] T1 — Reserved-inbox destination guard in `MergeProjectsByName` with RED→GREEN tests. Route: delegated (writer; store.go + store_test.go). Commit: `aa0bff5`.

Follow-up: CodeRabbit re-review (23:45) confirmed the guard and asked for
an exact-match error assertion. Tightened in `2a0eda0`; focused test green, gofmt/vet clean.

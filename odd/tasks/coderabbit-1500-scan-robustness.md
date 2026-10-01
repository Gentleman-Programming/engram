# Feature: coderabbit-1500-scan-robustness

Locator: `odd/tasks/coderabbit-1500-scan-robustness.md` — Engram mirror topic `odd/coderabbit-1500-scan-robustness/tasks` (project `engram`).
Branch: `refresh-1500-merge` (PR #1500).

## Objective

Close the three CodeRabbit actionable findings on PR #1500: non-directory root coverage, monotonic close-event handling, and oversized-line survival in `mcplogs.ScanLifecycles`.

## Problem

1. `ScanLifecycles` rejects a regular-file root (`!info.IsDir()` → error) but no test pins that contract (mcplogs_test.go ~10-14).
2. `applyLine`'s close case overwrites `ClosedAt`/`ClosedAfterSec` on every close event regardless of timestamp (mcplogs.go close case), so reverse-chronological fragments report the oldest close and a stale duration.
3. `scanFile` uses `bufio.Scanner` capped at 1MB; a longer line aborts the whole scan with `ErrTooLong`, discarding lifecycles parsed before it (mcplogs.go ~113).

## Scope

- Test pinning the regular-file-root error (coverage only; behavior already correct).
- Close events update `ClosedAt`+`ClosedAfterSec` together only when the incoming close timestamp is strictly newer than the stored one; first close still lands. Regression test with reverse-chronological fragments asserting the newest close and its duration.
- Replace the Scanner with a `bufio.Reader`-based line loop that tolerates arbitrarily long lines, skipping malformed ones, preserving lifecycles before and after. Test with a >1MB malformed line between valid records.

## Non-goals

- No public API or output-shape changes; classification slice untouched.

## Tasks

- [ ] T1 — Regular-file-root error test (no RED: coverage pin on existing behavior). Route: delegated (writer; mcplogs_test.go).
- [ ] T2 — Monotonic close-event update with RED→GREEN regression test. Route: delegated (writer; mcplogs.go + mcplogs_test.go).
- [ ] T3 — Oversized-line-surviving reader with RED→GREEN test. Route: delegated (writer; mcplogs.go + mcplogs_test.go).

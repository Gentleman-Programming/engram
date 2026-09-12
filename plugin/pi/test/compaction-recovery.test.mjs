import test from "node:test";
import assert from "node:assert/strict";
import { ArchiveOutcome, buildRecoveryNotice, extractCompactedSummary, recoveryInstruction } from "../compaction-recovery.js";

test("extractCompactedSummary returns undefined for unsupported event shapes", () => {
  assert.equal(extractCompactedSummary(null), undefined);
  assert.equal(extractCompactedSummary({}), undefined);
  assert.equal(extractCompactedSummary({ payload: { unrelated: "value" } }), undefined);
  assert.equal(extractCompactedSummary({ summary: "   " }), undefined);
});

test("extractCompactedSummary supports top-level and nested summary fields", () => {
  assert.equal(extractCompactedSummary({ compactedSummary: "summary text" }), "summary text");
  assert.equal(extractCompactedSummary({ payload: { summary: " nested summary " } }), "nested summary");
  assert.equal(extractCompactedSummary({ compaction: { content: "content summary" } }), "content summary");
});

test("extractCompactedSummary prefers Pi's current entry summary and falls through blanks", () => {
  assert.equal(
    extractCompactedSummary({ compactionEntry: { summary: "current summary" }, summary: "stale legacy summary" }),
    "current summary",
  );
  assert.equal(
    extractCompactedSummary({ compactionEntry: { summary: "  " }, summary: "legacy fallback" }),
    "legacy fallback",
  );
});

test("recoveryInstruction keeps manual FIRST ACTION REQUIRED fallback", () => {
  const notice = recoveryInstruction("engram");
  assert.match(notice, /FIRST ACTION REQUIRED/);
  assert.match(notice, /mem_session_summary/);
  assert.match(notice, /gentle-engram and the Engram MCP tools are installed and active/);
  assert.match(notice, /If mem_session_summary is unavailable/);
});

test("buildRecoveryNotice prefixes context when available", () => {
  assert.equal(buildRecoveryNotice("engram", "existing context").startsWith("existing context\n\nCRITICAL"), true);
  assert.equal(buildRecoveryNotice("engram", "").startsWith("CRITICAL"), true);
});

test("buildRecoveryNotice maps archive outcomes to safe next-turn guidance", () => {
  const confirmed = buildRecoveryNotice("engram", "context", ArchiveOutcome.Confirmed);
  assert.doesNotMatch(confirmed, /FIRST ACTION REQUIRED/);
  assert.match(confirmed, /already saved/);
  assert.match(confirmed, /No manual mem_session_summary/);

  const failed = buildRecoveryNotice("engram", undefined, ArchiveOutcome.Failed);
  assert.match(failed, /FIRST ACTION REQUIRED/);
  assert.match(failed, /mem_session_summary/);

  const unknown = buildRecoveryNotice("engram", undefined, ArchiveOutcome.Unknown);
  assert.match(unknown, /could not confirm/);
  assert.match(unknown, /verify/i);
  assert.match(unknown, /Do NOT retry/);

  const unavailable = buildRecoveryNotice("unknown", undefined, ArchiveOutcome.Unavailable);
  assert.match(unavailable, /did not archive/);
  assert.match(unavailable, /runtime session and project/);
  assert.match(unavailable, /verify/i);
});

import test from "node:test";
import assert from "node:assert/strict";
import { buildRecoveryNotice, extractCompactedSummary, recoveryInstruction } from "../compaction-recovery.js";

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

test("extractCompactedSummary reads the current Pi session_compact event shape", () => {
  // Documented Pi event: { compactionEntry: CompactionEntry, ... } where
  // CompactionEntry.summary holds the summary text.
  assert.equal(
    extractCompactedSummary({ compactionEntry: { summary: "native pi summary" } }),
    "native pi summary",
  );
  assert.equal(
    extractCompactedSummary({
      compactionEntry: { summary: "  extension summary  " },
      fromExtension: true,
      reason: "threshold",
      willRetry: false,
    }),
    "extension summary",
  );
  // Empty compactionEntry.summary is ignored (falls through / returns undefined).
  assert.equal(extractCompactedSummary({ compactionEntry: { summary: "   " } }), undefined);
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

test("buildRecoveryNotice drops the manual save instruction once persisted", () => {
  // Default (persisted=false): manual save instruction is present.
  const fallback = buildRecoveryNotice("engram", undefined);
  assert.match(fallback, /FIRST ACTION REQUIRED/);

  // After a successful archive: no manual save instruction, context still surfaced.
  const persisted = buildRecoveryNotice("engram", "recent project memory", true);
  assert.doesNotMatch(persisted, /FIRST ACTION REQUIRED/);
  assert.match(persisted, /recent project memory/);
  assert.match(persisted, /already saved to Engram/);
});

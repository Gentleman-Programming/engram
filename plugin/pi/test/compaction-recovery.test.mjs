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

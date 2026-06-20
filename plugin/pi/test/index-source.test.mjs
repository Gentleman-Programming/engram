import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const source = readFileSync(new URL("../index.ts", import.meta.url), "utf8");

test("mem_session_summary accepts explicit project fallback", () => {
  assert.match(source, /mem_session_summary: Type\.Object\(\{[\s\S]*project: optionalString\("Optional project to use when automatic detection is unavailable"\)/);
  assert.match(source, /case "mem_session_summary":[\s\S]*if \(!requestedProject\) requireResolvedProject\(\);[\s\S]*ensureSession\(activeSessionId, activeProject\)[\s\S]*project: activeProject/);
});

test("project detection 404 falls back to local config or diagnostic", () => {
  assert.match(source, /function detectLocalConfigProject\(cwd: string\)/);
  assert.match(source, /project_name/);
  assert.match(source, /error\.status === 404[\s\S]*detectLocalConfigProject\(cwd\) \|\| projectCurrentUnsupportedError\(cwd\)/);
  assert.match(source, /does not support \/project\/current/);
});

test("ambiguous_project error maps to actionable status label, not generic 'error'", () => {
  // The status bar must NOT show the generic 'error' label for ambiguous project conditions.
  // Instead it should show an actionable label such as 'ambiguous project'.
  assert.match(source, /function errorStatusLabel\(/);
  // Verify the function maps ambiguous project messages to the actionable label
  assert.match(source, /ambiguous project/);
  // Verify executeMemoryTool uses errorStatusLabel instead of the bare 'error' string
  assert.match(source, /errorStatusLabel\(message\)/);
  // The bare '· error' hardcoded string should no longer be present in the catch block
  assert.doesNotMatch(source, /setStatus\?\.\("engram",\s*`🧠 \$\{project\} · error`\)/);
});

test("mem_review is registered as a Pi-native executable memory tool", () => {
  assert.match(source, /const ENGRAM_TOOLS = \[[\s\S]*"mem_review"/);
  assert.match(source, /mem_review: Type\.Object\(\{[\s\S]*action: Type\.String\(\{ description: "Action: list \| mark_reviewed" \}\)/);
  assert.match(source, /mem_review: Type\.Object\(\{[\s\S]*observation_id: optionalNumber\("Observation id for action=mark_reviewed"\)/);
  assert.match(source, /mem_review: Type\.Object\(\{[\s\S]*id: optionalNumber\("Alias for observation_id"\)/);
  assert.match(source, /case "mem_review":[\s\S]*action === "list"[\s\S]*engramFetch\(`\/review\$\{queryString\(\{ project: params\.project, limit: params\.limit \}\)\}`\)/);
  assert.match(source, /case "mem_review":[\s\S]*action === "mark_reviewed"[\s\S]*engramFetch\("\/review\/mark_reviewed"/);
  assert.match(source, /case "mem_review":[\s\S]*body: \{ observation_id: params\.observation_id \|\| params\.id \}/);
  assert.match(source, /for \(const toolName of ENGRAM_TOOLS\)[\s\S]*executeMemoryTool\(toolName/);
});

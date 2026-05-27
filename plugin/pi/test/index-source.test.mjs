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

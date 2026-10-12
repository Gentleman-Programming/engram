import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const source = readFileSync(new URL("../index.ts", import.meta.url), "utf8").replaceAll("\r\n", "\n");
const match = source.match(/const MEMORY_INSTRUCTIONS = `([\s\S]*?)\n`;/);
assert.ok(match, "Pi-owned injected memory instructions must exist");
const instructions = match[1].replaceAll("\\`", "`");
const close = instructions.split("### SESSION CLOSE PROTOCOL\n\n")[1]?.split("\n### AFTER COMPACTION")[0];
assert.ok(close, "session-close section must exist");

// These pin the injected instruction contract, not real model compliance.
test("ordinary task completion and saying done do not require a session summary", () => {
  assert.match(close, /Ordinary task completion or merely saying "done" does not require a session summary\./);
  assert.doesNotMatch(close, /Before ending a session or saying "done", call/);
});

test("explicit session closure or handoff still requires a structured summary", () => {
  assert.match(close, /Before explicitly closing or handing off the session, call `mem_session_summary`/);
  assert.match(close, /with Goal, Instructions, Discoveries, Accomplished, Next Steps, and Relevant Files\./);
  assert.match(close, /If `mem_session_summary` fails because Engram cannot detect a project, ask the user\nwhich project should receive the summary, then retry with `project: "<name>"`\./);
});

test("important learnings still require the canonical topic workflow", () => {
  const workflow = instructions.split("### CANONICAL TOPIC WORKFLOW (mandatory — not optional)\n\n")[1]?.split("\n### DELIVERY GUARANTEE")[0];
  assert.ok(workflow, "mandatory canonical topic workflow must exist");
  assert.match(workflow, /SEARCH first[\s\S]*`mem_search`[\s\S]*READ in full[\s\S]*`mem_get_observation`/);
  assert.match(workflow, /SAME bounded topic[\s\S]*`mem_update`[\s\S]*Genuinely new topic[\s\S]*`mem_save`/);
  assert.match(workflow, /Save when one of these happens:\n- Bug fix completed\n- Architecture or design decision made\n- Non-obvious discovery about the codebase\n- Configuration change or environment setup\n- Pattern established \(naming, structure, convention\)\n- User preference or constraint learned/);
  assert.doesNotMatch(workflow, /Call `mem_save` IMMEDIATELY/);
});

test("compaction recovery retains its separate outcome guidance and fallback", () => {
  assert.match(instructions, /### AFTER COMPACTION\n\nWhen outcome-specific compaction recovery guidance is present, follow it\. If a\ncompacted summary appears without that guidance, save it immediately with\n`mem_session_summary`, then call `mem_context` before continuing\./);
});

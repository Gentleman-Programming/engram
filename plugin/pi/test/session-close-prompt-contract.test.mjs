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

test("significant explicit session closure or handoff still requires a structured summary", () => {
  assert.match(close, /significant[\s\S]{0,100}(explicit[\s\S]{0,50}(clos|handoff)|clos[\s\S]{0,50}handoff)/i);
  assert.match(close, /mem_session_summary/);
  assert.match(close, /with Goal, Instructions, Discoveries, Accomplished, Next Steps, and Relevant Files\./);
  assert.match(close, /If `mem_session_summary` fails because Engram cannot detect a project, ask the user\nwhich project should receive the summary, then retry with `project: "<name>"`\./);
});

test("important learnings still require the canonical topic workflow", () => {
  const workflow = instructions.split("### CANONICAL TOPIC WORKFLOW (mandatory — not optional)\n\n")[1]?.split("\n### DELIVERY GUARANTEE")[0];
  assert.ok(workflow, "mandatory canonical topic workflow must exist");
  assert.match(workflow, /SEARCH first[\s\S]*`mem_search`[\s\S]*READ in full[\s\S]*`mem_get_observation`/);
  assert.match(workflow, /SAME bounded topic[\s\S]*`mem_update`[\s\S]*Genuinely new topic[\s\S]*`mem_save`/);
  assert.match(workflow, /verified[\s\S]{0,100}durable[\s\S]{0,100}costly to reconstruct/i);
  for (const rule of [/decisions?[\s\S]{0,40}(rationale|reasons)/i, /non-obvious root causes?/i, /real constraints/i, /resumption-relevant pending work/i]) {
    assert.match(workflow, rule);
  }
  assert.match(workflow, /group related conclusions/i);
  assert.doesNotMatch(workflow, /Save when one of these happens|After EVERY task/i);
  assert.doesNotMatch(workflow, /Call `mem_save` IMMEDIATELY/);
});

test("selective workflow preserves privacy, topic safety and native passive boundaries", () => {
  assert.doesNotMatch(instructions, /^(<<<<<<<|=======|>>>>>>>)/m);
  for (const safeguard of [/secrets/i, /private (content|data)/i, /speculation/i, /raw (source )?dumps/i, /routine progress/i, /preserve every still-valid fact/i, /superseded facts as history/i, /Ambiguous identity or failed lookup/i, /duplicate save/i, /Never merge across projects or scopes/i]) {
    assert.match(instructions, safeguard);
  }
  assert.match(instructions, /not every (task|response|event)|no per-(task|event)/i);
  assert.doesNotMatch(instructions, /If yes, call `mem_save` now/);
  assert.match(instructions, /### NATIVE SESSION ATTRIBUTION[\s\S]*Do not invent or override `session_id`/);
  assert.match(instructions, /confirmed Engram registration/);
  assert.match(instructions, /resumed continuations and explicit cross-project targets/);
  assert.match(instructions, /`mem_session_end` accepts only the current Pi host session ID/);
  assert.match(instructions, /Ordinary task completion does not end it/);
  assert.match(instructions, /eligible non-Engram tool outputs after redaction/);
  assert.match(instructions, /### PASSIVE CAPTURE[\s\S]*Final assistant responses are not automatically captured/);
  assert.match(instructions, /significant learnings[\s\S]{0,180}canonical topic workflow/);
  assert.match(instructions, /a final `## Key Learnings:` section alone is not a persistence guarantee/);
});

test("recall remains progressive and selective without project-mention ceremony", () => {
  const recall = instructions.split("### WHEN TO SEARCH MEMORY\n\n")[1]?.split("\n### SESSION CLOSE PROTOCOL")[0];
  assert.ok(recall);
  assert.match(recall, /materially useful[\s\S]{0,100}(recall|related work)/i);
  assert.match(recall, /(mere|ordinary) project mentions?[\s\S]{0,100}(not|no|never)/i);
  assert.match(recall, /mem_context[\s\S]*mem_search[\s\S]*mem_get_observation/);
  assert.match(recall, /match_mode:"all"/);
  assert.match(recall, /match_mode:"any"[\s\S]*all_projects:true/);
  assert.match(recall, /If a scoped search is empty, retry once/);
  assert.match(recall, /narrow follow-up searches by project, type/);
});

test("bookkeeping failures require honest evidence, not blind repeated writes", () => {
  assert.match(instructions, /honest[\s\S]{0,100}(unavailable|failed)[\s\S]{0,100}(evidence|proof)|(unavailable|failed)[\s\S]{0,100}(evidence|proof)[\s\S]{0,100}(honest|report|claim)/i);
  assert.match(instructions, /(do not|never|no)[\s\S]{0,40}blind[\s\S]{0,50}(repeat|retr|writ)/i);
  assert.match(instructions, /If memory work fails or needs follow-up, still send the answer/);
});

test("compaction recovery retains its separate outcome guidance and fallback", () => {
  assert.match(instructions, /### AFTER COMPACTION\n\nWhen outcome-specific compaction recovery guidance is present, follow it\. If a\ncompacted summary appears without that guidance, save it immediately with\n`mem_session_summary`, then call `mem_context` before continuing\./);
});

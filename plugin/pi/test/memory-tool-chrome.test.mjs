import test from "node:test";
import assert from "node:assert/strict";
import {
  SUPPORTED_MEMORY_TOOLS,
  compactResultStatus,
  compactToolArg,
  humanToolName,
  renderCallText,
  renderResultText,
} from "../memory-tool-chrome.js";

const existingTools = [
  "mem_search",
  "mem_save",
  "mem_update",
  "mem_delete",
  "mem_suggest_topic_key",
  "mem_save_prompt",
  "mem_session_summary",
  "mem_context",
  "mem_stats",
  "mem_timeline",
  "mem_get_observation",
  "mem_session_start",
  "mem_session_end",
];

test("supported memory tools all have chrome metadata", () => {
  assert.deepEqual([...SUPPORTED_MEMORY_TOOLS].sort(), [...existingTools].sort());
  for (const tool of existingTools) {
    assert.notEqual(humanToolName(tool), tool);
    assert.match(renderCallText(tool, {}), /^🧠 /);
  }
});

test("compactToolArg prefers short meaningful identifiers", () => {
  assert.equal(compactToolArg("mem_search", { query: "auth model" }), "“auth model”");
  assert.equal(compactToolArg("mem_save", { title: "Fixed the session recovery issue" }), "“Fixed the session recovery issue”");
  assert.equal(compactToolArg("mem_get_observation", { id: 42 }), "#42");
  assert.equal(compactToolArg("mem_context", { project: "engram" }), "“engram”");
});

test("compactToolArg truncates long text", () => {
  const arg = compactToolArg("mem_save_prompt", { content: "a".repeat(120) });
  assert.ok(arg.length < 60);
  assert.ok(arg.endsWith("…”"));
});

test("compactResultStatus summarizes common Engram results", () => {
  assert.equal(compactResultStatus("mem_search", { details: { data: [{ id: 1 }, { id: 2 }] } }), "✓ 2 results");
  assert.equal(compactResultStatus("mem_save", { details: { data: { id: 7 } } }), "✓ saved #7");
  assert.equal(compactResultStatus("mem_context", { details: { data: { context: "recent memory" } } }), "✓ loaded");
  assert.equal(compactResultStatus("mem_suggest_topic_key", { details: { data: { topic_key: "auth-model" } } }), "✓ auth-model");
});

test("renderResultText keeps collapsed output compact and expanded output detailed", () => {
  const result = {
    content: [{ type: "text", text: "full details\nwith more content" }],
    details: { data: [{ id: 1 }] },
  };

  assert.equal(renderResultText("mem_search", result, { expanded: false }), "↳ ✓ 1 result");
  assert.equal(renderResultText("mem_search", result, { expanded: true }), "↳ ✓ 1 result\n\nfull details\nwith more content");
});

test("renderResultText shows running and error states compactly", () => {
  assert.equal(renderResultText("mem_search", {}, { isPartial: true }), "↳ search…");
  assert.equal(renderResultText("mem_save", { content: [{ type: "text", text: "server down" }] }, { isError: true }), "↳ ✗ server down");
});

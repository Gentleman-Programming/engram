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
  "mem_current_project",
  "mem_doctor",
  "mem_capture_passive",
  "mem_judge",
  "mem_compare",
  "mem_review",
  "mem_list_projects",
  "mem_pin",
  "mem_unpin",
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
  assert.equal(compactToolArg("mem_compare", { memory_id_a: 42, memory_id_b: 43 }), "#42");
  assert.equal(compactToolArg("mem_judge", { judgment_id: "rel-abc", relation: "related" }), "“rel-abc”");
  assert.equal(compactToolArg("mem_review", { action: "list", project: "engram", limit: 5 }), "list “engram” limit 5");
  assert.equal(compactToolArg("mem_review", { action: "mark_reviewed", observation_id: 42 }), "mark_reviewed #42");
  assert.equal(compactToolArg("mem_review", { action: "mark_reviewed", id: 43 }), "mark_reviewed #43");
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
  assert.equal(compactResultStatus("mem_current_project", { details: { data: { project: "engram" } } }), "✓ engram");
  assert.equal(compactResultStatus("mem_doctor", { details: { data: { status: "ok" } } }), "✓ ok");
  assert.equal(compactResultStatus("mem_capture_passive", { details: { data: { saved: 2 } } }), "✓ captured 2");
  assert.equal(compactResultStatus("mem_judge", { details: { data: { relation: { sync_id: "rel-1" } } } }), "✓ judged rel-1");
  assert.equal(compactResultStatus("mem_compare", { details: { data: { sync_id: "rel-2" } } }), "✓ rel-2");
  assert.equal(compactResultStatus("mem_review", { details: { data: { observations: [{ id: 1 }, { id: 2 }] } } }), "✓ 2 need review");
  assert.equal(compactResultStatus("mem_review", { details: { data: { results: [{ id: 1 }] } } }), "✓ 1 needs review");
  assert.equal(compactResultStatus("mem_review", { details: { data: { count: 0 } } }), "✓ 0 need review");
  assert.equal(compactResultStatus("mem_review", { details: { data: { id: 42, state: "active" } } }), "✓ reviewed #42");
});

test("renderResultText keeps collapsed output compact and expanded output detailed", () => {
  const result = {
    content: [{ type: "text", text: "full details\nwith more content" }],
    details: { data: [{ id: 1 }] },
  };

  assert.equal(renderResultText("mem_search", result, { expanded: false }), "↳ ✓ 1 result");
  assert.equal(renderResultText("mem_search", result, { expanded: true }), "↳ ✓ 1 result\n\nfull details\nwith more content");
});

test("renderCallText and renderResultText summarize mem_review clearly", () => {
  assert.equal(renderCallText("mem_review", { action: "list", project: "engram", limit: 3 }), "🧠 review list “engram” limit 3 …");
  assert.equal(renderCallText("mem_review", { action: "mark_reviewed", observation_id: 99 }), "🧠 review mark_reviewed #99 …");

  assert.equal(
    renderResultText("mem_review", { details: { data: { observations: [{ id: 1 }, { id: 2 }, { id: 3 }] } } }, { expanded: false }),
    "↳ ✓ 3 need review",
  );
  assert.equal(
    renderResultText("mem_review", { details: { data: { id: 99, state: "active" } } }, { expanded: false }),
    "↳ ✓ reviewed #99",
  );
});

test("renderResultText shows running and error states compactly", () => {
  assert.equal(renderResultText("mem_search", {}, { isPartial: true }), "↳ search…");
  assert.equal(renderResultText("mem_save", { content: [{ type: "text", text: "server down" }] }, { isError: true }), "↳ ✗ server down");
});

// ---------------------------------------------------------------------------
// Box chrome (opt-in via ENGRAM_CHROME=box in index.ts; renderCallText and
// renderResultText themselves only ever act on an explicit `width` argument).

// Local, test-only helpers, kept independent of memory-tool-chrome.js's own
// (unexported) stripAnsi/visibleWidth so the assertions below don't just
// restate the implementation under test. hasAnsi uses a fresh non-global
// regex literal per call rather than reusing one with the "g" flag, since a
// shared global regex's .test() carries lastIndex state across calls.
function stripAnsiOf(text) {
  return text.replaceAll(/\x1b\[[0-9;]*m/g, "");
}
function hasAnsi(text) {
  return /\x1b\[[0-9;]*m/.test(text);
}
const wideGraphemeRe = /\p{Extended_Pictographic}|\p{Emoji_Presentation}/u;
const segmenter = new Intl.Segmenter("en", { granularity: "grapheme" });
function visibleWidthOf(text) {
  let width = 0;
  for (const { segment } of segmenter.segment(stripAnsiOf(text))) width += wideGraphemeRe.test(segment) ? 2 : 1;
  return width;
}

const searchResult = { details: { data: [{ id: 1 }, { id: 2 }] } };
const PINK_ESCAPE = "\x1b[38;5;205m";

test("with no width argument, box chrome functions still return today's exact compact strings", () => {
  assert.equal(renderCallText("mem_search", { query: "auth model" }), '🧠 search “auth model” …');
  assert.equal(renderResultText("mem_search", searchResult, {}), "↳ ✓ 2 results");
});

test("a numeric width draws a closed four-sided box split across call and result", () => {
  const callLines = renderCallText("mem_search", { query: "auth model" }, 60).split("\n");
  const resultLines = renderResultText("mem_search", searchResult, {}, 60).split("\n");

  assert.equal(callLines.length, 2);
  assert.equal(resultLines.length, 2);

  const [top, callText] = callLines.map(stripAnsiOf);
  const [resultText, bottom] = resultLines.map(stripAnsiOf);

  assert.ok(top.startsWith("╔") && top.endsWith("╗"), top);
  assert.ok(callText.startsWith("║") && callText.endsWith("║"), callText);
  assert.ok(resultText.startsWith("║") && resultText.endsWith("║"), resultText);
  assert.ok(bottom.startsWith("╚") && bottom.endsWith("╝"), bottom);
  assert.match(callText, /🧠/);
});

test("every box line measures exactly the requested width (ANSI-stripped), including the emoji line", () => {
  const width = 60;
  const lines = [
    ...renderCallText("mem_search", { query: "auth model" }, width).split("\n"),
    ...renderResultText("mem_search", searchResult, {}, width).split("\n"),
  ];
  for (const line of lines) assert.equal(visibleWidthOf(line), width, line);
});

test("top and bottom borders have the same codepoint count as each other once ANSI is stripped", () => {
  const width = 60;
  const top = renderCallText("mem_search", { query: "auth model" }, width).split("\n")[0];
  const bottom = renderResultText("mem_search", searchResult, {}, width).split("\n")[1];
  // Raw codepoint counts differ here because the call border is bold (a
  // longer escape sequence) and the result border isn't -- escapes carry no
  // visible width, so the comparison that matters is on the stripped string.
  assert.equal([...stripAnsiOf(top)].length, [...stripAnsiOf(bottom)].length);
});

test("box lines carry the pink 205 escape while the default compact path carries none", () => {
  const width = 60;
  const boxedCall = renderCallText("mem_search", { query: "auth model" }, width);
  const boxedResult = renderResultText("mem_search", searchResult, {}, width);
  assert.ok(boxedCall.includes(PINK_ESCAPE), boxedCall);
  assert.ok(boxedResult.includes(PINK_ESCAPE), boxedResult);
  assert.ok(hasAnsi(boxedCall));
  assert.ok(hasAnsi(boxedResult));

  const compactCall = renderCallText("mem_search", { query: "auth model" });
  const compactResult = renderResultText("mem_search", searchResult, {});
  assert.equal(hasAnsi(compactCall), false);
  assert.equal(hasAnsi(compactResult), false);
});

test("too-small or absent width falls back to the compact single-line form without throwing", () => {
  const compactCall = '🧠 search “auth model” …';
  const compactResult = "↳ ✓ 2 results";

  assert.equal(renderCallText("mem_search", { query: "auth model" }), compactCall);
  assert.equal(renderCallText("mem_search", { query: "auth model" }, 2), compactCall);
  assert.equal(renderResultText("mem_search", searchResult, {}), compactResult);
  assert.equal(renderResultText("mem_search", searchResult, {}, 2), compactResult);

  assert.doesNotThrow(() => renderCallText("mem_search", { query: "auth model" }, 0));
  assert.doesNotThrow(() => renderResultText("mem_search", searchResult, {}, -5));
});

test("box chrome preserves expanded and partial result behavior", () => {
  const expandedResult = {
    content: [{ type: "text", text: "full details\nwith more content" }],
    details: { data: [{ id: 1 }] },
  };
  const width = 60;

  const framedExpanded = renderResultText("mem_search", expandedResult, { expanded: true }, width);
  assert.ok(framedExpanded.endsWith("\n\nfull details\nwith more content"));
  const framedLines = framedExpanded.split("\n").slice(0, 2).map(stripAnsiOf);
  assert.ok(framedLines[1].startsWith("╚") && framedLines[1].endsWith("╝"));

  // isPartial keeps its exact status wording ("search…"); the box itself
  // still closes because a result component is mounted -- only a call with
  // no result component at all stays open at the bottom (see the box-chrome
  // comment above renderCallText).
  const framedPartial = renderResultText("mem_search", {}, { isPartial: true }, width).split("\n").map(stripAnsiOf);
  assert.equal(framedPartial.length, 2);
  assert.match(framedPartial[0], /search…/);
  assert.ok(framedPartial[1].startsWith("╚") && framedPartial[1].endsWith("╝"));
  assert.equal(renderResultText("mem_search", {}, { isPartial: true }), "↳ search…");
});

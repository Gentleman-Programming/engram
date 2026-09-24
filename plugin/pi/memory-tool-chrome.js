const TOOL_LABELS = {
  mem_search: "search",
  mem_save: "save",
  mem_update: "update",
  mem_delete: "delete",
  mem_suggest_topic_key: "suggest topic",
  mem_save_prompt: "save prompt",
  mem_session_summary: "session summary",
  mem_context: "context",
  mem_stats: "stats",
  mem_timeline: "timeline",
  mem_get_observation: "get observation",
  mem_session_start: "start session",
  mem_session_end: "end session",
  mem_current_project: "current project",
  mem_doctor: "doctor",
  mem_capture_passive: "capture passive",
  mem_judge: "judge",
  mem_compare: "compare",
  mem_review: "review",
  mem_list_projects: "list projects",
  mem_pin: "pin",
  mem_unpin: "unpin",
};

const ARG_KEYS = {
  mem_search: ["query"],
  mem_save: ["title", "type"],
  mem_update: ["id", "title"],
  mem_delete: ["id"],
  mem_suggest_topic_key: ["title", "type"],
  mem_save_prompt: ["content"],
  mem_session_summary: ["content"],
  mem_context: ["project", "scope"],
  mem_stats: ["project"],
  mem_timeline: ["observation_id"],
  mem_get_observation: ["id"],
  mem_session_start: ["id"],
  mem_session_end: ["id"],
  mem_current_project: ["cwd"],
  mem_doctor: ["check", "project"],
  mem_capture_passive: ["source", "content"],
  mem_judge: ["judgment_id", "relation"],
  mem_compare: ["memory_id_a", "memory_id_b"],
  mem_review: ["action", "project", "limit", "observation_id", "id"],
  mem_list_projects: [],
  mem_pin: ["id"],
  mem_unpin: ["id"],
};

export const SUPPORTED_MEMORY_TOOLS = Object.freeze(Object.keys(TOOL_LABELS));

export function humanToolName(toolName) {
  return TOOL_LABELS[toolName] ?? toolName.replace(/^mem_/, "").replace(/_/g, " ");
}

export function truncateText(value, max = 48) {
  const text = String(value ?? "").replace(/\s+/g, " ").trim();
  if (text.length <= max) return text;
  return `${text.slice(0, Math.max(0, max - 1))}…`;
}

function quote(value) {
  const text = truncateText(value);
  return text ? `“${text}”` : "";
}

export function compactToolArg(toolName, args = {}) {
  if (toolName === "mem_review") return compactReviewArg(args);

  const keys = ARG_KEYS[toolName] ?? [];
  for (const key of keys) {
    const value = args?.[key];
    if (value === undefined || value === null || value === "") continue;
    if (key === "id" || key === "observation_id" || key === "memory_id_a" || key === "memory_id_b") return `#${value}`;
    return quote(value);
  }
  return "";
}

function compactReviewArg(args = {}) {
  const parts = [];
  if (args.action !== undefined && args.action !== null && args.action !== "") parts.push(String(args.action));

  const id = args.observation_id ?? args.id;
  if (id !== undefined && id !== null && id !== "") parts.push(`#${id}`);

  if (args.project !== undefined && args.project !== null && args.project !== "") parts.push(quote(args.project));
  if (args.limit !== undefined && args.limit !== null && args.limit !== "") parts.push(`limit ${args.limit}`);

  return parts.join(" ");
}

function firstTextContent(result) {
  const block = result?.content?.find?.((entry) => entry?.type === "text" && typeof entry.text === "string");
  return block?.text ?? "";
}

function resultData(result) {
  return result?.details?.data ?? result?.details ?? result;
}

function countItems(value) {
  if (Array.isArray(value)) return value.length;
  if (Array.isArray(value?.results)) return value.results.length;
  if (Array.isArray(value?.observations)) return value.observations.length;
  if (Array.isArray(value?.sessions)) return value.sessions.length;
  if (Array.isArray(value?.prompts)) return value.prompts.length;
  if (typeof value?.count === "number") return value.count;
  return undefined;
}

export function compactResultStatus(toolName, result, options = {}) {
  if (options.isPartial) return `${humanToolName(toolName)}…`;
  if (options.isError || result?.isError) {
    const text = truncateText(firstTextContent(result) || result?.details?.error || "error", 64);
    return `✗ ${text}`;
  }

  const data = resultData(result);
  const count = countItems(data);
  if (toolName === "mem_search") return `✓ ${count ?? 0} result${count === 1 ? "" : "s"}`;
  if (toolName === "mem_context") return `✓ ${firstTextContent(result) || data?.context ? "loaded" : "empty"}`;
  if (toolName === "mem_stats") return "✓ loaded";
  if (toolName === "mem_timeline") return `✓ ${count ?? "timeline"}`;
  if (toolName === "mem_get_observation") return data?.id ? `✓ observation #${data.id}` : "✓ loaded";
  if (toolName === "mem_save" || toolName === "mem_session_summary") return data?.id ? `✓ saved #${data.id}` : "✓ saved";
  if (toolName === "mem_update") return data?.id ? `✓ updated #${data.id}` : "✓ updated";
  if (toolName === "mem_delete") return data?.id ? `✓ deleted #${data.id}` : "✓ deleted";
  if (toolName === "mem_suggest_topic_key") return data?.topic_key ? `✓ ${data.topic_key}` : "✓ suggested";
  if (toolName === "mem_save_prompt") return data?.id ? `✓ prompt #${data.id}` : "✓ prompt saved";
  if (toolName === "mem_session_start") return "✓ started";
  if (toolName === "mem_session_end") return "✓ ended";
  if (toolName === "mem_current_project") return data?.project ? `✓ ${data.project}` : "✓ detected";
  if (toolName === "mem_doctor") return data?.status ? `✓ ${data.status}` : "✓ checked";
  if (toolName === "mem_capture_passive") return `✓ captured ${data?.saved ?? count ?? 0}`;
  if (toolName === "mem_judge") return data?.relation?.sync_id ? `✓ judged ${data.relation.sync_id}` : "✓ judged";
  if (toolName === "mem_compare") return data?.sync_id ? `✓ ${data.sync_id}` : "✓ compared";
  if (toolName === "mem_review") {
    if (count !== undefined) return `✓ ${count} need${count === 1 ? "s" : ""} review`;
    const id = data?.id ?? data?.observation_id ?? data?.observation?.id;
    return id ? `✓ reviewed #${id}` : "✓ reviewed";
  }
  return "✓ done";
}

// ---------------------------------------------------------------------------
// Box chrome (opt-in via ENGRAM_CHROME=box, see index.ts)
//
// renderCallText/renderResultText below accept an optional `width`: when it
// is not a number (the default -- see index.ts, which only ever passes one
// when ENGRAM_CHROME=box), they return exactly the compact single-line form
// above, unchanged. That keeps this an additive, opt-in code path: nothing
// about a caller that never passes `width` changes.
//
// When `width` is a number, index.ts is driving a width-aware duck-typed Pi
// component instead of a fixed Text() (Text never learns the terminal width,
// so it can only ever hug its own string). renderCallText then draws the
// TOP half of a box (border + its own text line) and renderResultText draws
// the BOTTOM half (text line + border); Pi keeps the call component mounted
// and appends the result component under it, so a completed call+result
// pair reads as one closed four-sided box:
//
//   ╔════════════════════════════════════════╗
//   ║ 🧠 search "auth model" …               ║
//   ║ ✓ 2 results                            ║
//   ╚════════════════════════════════════════╝
//
// While a call has no result yet, only the top half above is mounted --
// that box is legitimately open at the bottom and must not be closed early.

const BOX_TOP_LEFT = "╔";
const BOX_TOP_RIGHT = "╗";
const BOX_BOTTOM_LEFT = "╚";
const BOX_BOTTOM_RIGHT = "╝";
const BOX_SIDE = "║";
const BOX_FILL = "═";

// Hot pink (256-color 205), defined once and reused by both the call frame
// (bold) and the result frame (plain) -- this is the whole visual point of
// the box chrome, not a cosmetic extra: it is what distinguishes the boxed
// mem_* chrome from the plain compact form. Only ever emitted when `width`
// is a number, i.e. only when ENGRAM_CHROME=box is set -- the default
// compact strings above carry no ANSI at all.
const PINK = "\x1b[38;5;205m";
const PINK_BOLD = "\x1b[38;5;205m\x1b[1m";
const RESET = "\x1b[0m";

function stripAnsi(text) {
  return text.replace(/\x1b\[[0-9;]*m/g, "");
}

// Naive `.length` counts UTF-16 code units, which misreports a line
// containing an emoji: a surrogate-pair emoji like 🧠 is 2 units (also 2
// terminal columns -- right only by accident), while other multi-unit
// clusters render narrower than their unit count suggests. Segmenting into
// graphemes and classifying each one is the only reliable way to measure a
// line's true visible width. ANSI color escapes are stripped first so they
// never count as visible columns.
//
// This duplicates @earendil-works/pi-tui's own visibleWidth rather than
// importing it: pi-tui is only an OPTIONAL peer dependency of this package
// (see package.json), so this file must keep working in any host that pulls
// gentle-engram in without pi-tui installed. The two agree on the case that
// matters here -- a standalone pictographic emoji like 🧠 is 2 columns under
// both pi-tui's own emoji classification and this file's
// Extended_Pictographic/Emoji_Presentation check below.
const graphemeSegmenter =
  typeof Intl !== "undefined" && typeof Intl.Segmenter === "function"
    ? new Intl.Segmenter("en", { granularity: "grapheme" })
    : undefined;

function graphemes(text) {
  return graphemeSegmenter ? Array.from(graphemeSegmenter.segment(text), (entry) => entry.segment) : Array.from(text);
}

const WIDE_GRAPHEME_RE = /\p{Extended_Pictographic}|\p{Emoji_Presentation}/u;

function visibleWidth(text) {
  let width = 0;
  for (const grapheme of graphemes(stripAnsi(text))) width += WIDE_GRAPHEME_RE.test(grapheme) ? 2 : 1;
  return width;
}

// A pure horizontal border: `<color><capLeft><fill of ═...><capRight><reset>`,
// padded to exactly `width` visible columns, with NO text and NO emoji
// inside it. Every codepoint on a border (color escapes aside, which
// visibleWidth strips) is a single-column box-drawing character, so a
// border's codepoint count IS its visible width -- the top and bottom
// borders built for the same `width` and color are therefore identical in
// codepoint count and can never drift apart under different terminal font
// metrics the way a text-and-emoji line can (a font that advances 🧠 by one
// cell instead of two would paint a border carrying that emoji one column
// short; a pure `═` run has no wide grapheme for a font to misjudge).
// Returns undefined when there is no room for at least one fill column, so
// callers fall back to the compact single-line form instead of overflowing.
function borderLine(color, capLeft, capRight, width) {
  const fillWidth = width - visibleWidth(capLeft) - visibleWidth(capRight);
  if (fillWidth < 0) return undefined;
  return `${color}${capLeft}${BOX_FILL.repeat(fillWidth)}${capRight}${RESET}`;
}

// One text line inside the box: `<color>║ <content><fill of spaces>║<reset>`,
// padded to exactly `width` visible columns. Unlike a border, this line can
// carry the brain emoji or other wide graphemes, so it is measured with the
// same grapheme-aware visibleWidth() as everything else here. Returns
// undefined when the content itself does not fit, so callers fall back to
// the compact form instead of truncating or overflowing.
function textLine(color, content, width) {
  const left = `${BOX_SIDE} `;
  const right = BOX_SIDE;
  const fillWidth = width - visibleWidth(left) - visibleWidth(content) - visibleWidth(right);
  if (fillWidth < 0) return undefined;
  return `${color}${left}${content}${" ".repeat(fillWidth)}${right}${RESET}`;
}

export function renderCallText(toolName, args = {}, width) {
  const arg = compactToolArg(toolName, args);
  const inner = `🧠 ${humanToolName(toolName)}${arg ? ` ${arg}` : ""} …`;
  if (typeof width !== "number") return inner;

  const top = borderLine(PINK_BOLD, BOX_TOP_LEFT, BOX_TOP_RIGHT, width);
  const text = textLine(PINK_BOLD, inner, width);
  return top && text ? `${top}\n${text}` : inner;
}

export function renderResultText(toolName, result, options = {}, width) {
  const status = compactResultStatus(toolName, result, options);
  const frame = (() => {
    if (typeof width !== "number") return `↳ ${status}`;
    const text = textLine(PINK, status, width);
    const bottom = borderLine(PINK, BOX_BOTTOM_LEFT, BOX_BOTTOM_RIGHT, width);
    return text && bottom ? `${text}\n${bottom}` : `↳ ${status}`;
  })();
  if (!options.expanded || options.isPartial) return frame;

  // The expanded body stays OUTSIDE the box entirely -- unframed, exactly as
  // it renders in the default compact chrome.
  const text = firstTextContent(result);
  if (text) return `${frame}\n\n${text}`;

  const data = resultData(result);
  return `${frame}\n\n${truncateText(JSON.stringify(data, null, 2), 2000)}`;
}

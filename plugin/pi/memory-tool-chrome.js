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
  const keys = ARG_KEYS[toolName] ?? [];
  for (const key of keys) {
    const value = args?.[key];
    if (value === undefined || value === null || value === "") continue;
    if (key === "id" || key === "observation_id") return `#${value}`;
    return quote(value);
  }
  return "";
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
  return "✓ done";
}

export function renderCallText(toolName, args = {}) {
  const arg = compactToolArg(toolName, args);
  return `🧠 ${humanToolName(toolName)}${arg ? ` ${arg}` : ""} …`;
}

export function renderResultText(toolName, result, options = {}) {
  const status = compactResultStatus(toolName, result, options);
  if (!options.expanded || options.isPartial) return `↳ ${status}`;

  const text = firstTextContent(result);
  if (text) return `↳ ${status}\n\n${text}`;

  const data = resultData(result);
  return `↳ ${status}\n\n${truncateText(JSON.stringify(data, null, 2), 2000)}`;
}

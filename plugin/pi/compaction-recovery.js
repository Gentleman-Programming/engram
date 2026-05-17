const SUMMARY_FIELD_PATHS = [
  ["summary"],
  ["compactedSummary"],
  ["compacted_summary"],
  ["compactSummary"],
  ["compact_summary"],
  ["content"],
  ["text"],
  ["message"],
  ["compacted", "summary"],
  ["compacted", "content"],
  ["compaction", "summary"],
  ["compaction", "content"],
  ["output", "summary"],
  ["output", "content"],
  ["payload", "summary"],
  ["payload", "content"],
  ["data", "summary"],
  ["data", "content"],
];

function getPath(root, path) {
  let current = root;
  for (const key of path) {
    if (!current || typeof current !== "object" || !(key in current)) return undefined;
    current = current[key];
  }
  return current;
}

function normalizeSummary(value) {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  return trimmed.length > 0 ? trimmed : undefined;
}

/**
 * Best-effort extraction for Pi compaction event shapes. Unsupported shapes
 * intentionally return undefined instead of throwing.
 */
export function extractCompactedSummary(event) {
  if (!event || typeof event !== "object") return undefined;
  for (const path of SUMMARY_FIELD_PATHS) {
    const summary = normalizeSummary(getPath(event, path));
    if (summary) return summary;
  }
  return undefined;
}

export function recoveryInstruction(project) {
  return (
    `CRITICAL INSTRUCTION FOR COMPACTED SUMMARY:\n` +
    `The agent has access to Engram persistent memory via MCP tools when gentle-engram and the Engram MCP tools are installed and active.\n` +
    `FIRST ACTION REQUIRED: Call mem_session_summary with the content of this compacted summary. ` +
    `Use project: '${project}'. This preserves what was accomplished before compaction. Do this BEFORE any other work.\n` +
    `If mem_session_summary is unavailable, manually save this compacted summary once Engram tools are available.`
  );
}

export function buildRecoveryNotice(project, context) {
  const instruction = recoveryInstruction(project);
  const trimmedContext = typeof context === "string" ? context.trim() : "";
  return trimmedContext ? `${trimmedContext}\n\n${instruction}` : instruction;
}

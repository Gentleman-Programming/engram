const SUMMARY_FIELD_PATHS = [
  // Current Pi session_compact event shape:
  //   { compactionEntry: CompactionEntry, fromExtension, reason, willRetry }
  // where CompactionEntry.summary holds the summary text. Listed first so the
  // documented shape wins over generic legacy fallbacks.
  ["compactionEntry", "summary"],
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

/**
 * Manual fallback used when the compaction summary could NOT be archived to
 * Engram during the session_compact handler (no summary extracted, no session,
 * or the Engram write failed). Asks the agent to save it on the next turn.
 */
export function recoveryInstruction(project) {
  return (
    `CRITICAL INSTRUCTION FOR COMPACTED SUMMARY:\n` +
    `The agent has access to Engram persistent memory via MCP tools when gentle-engram and the Engram MCP tools are installed and active.\n` +
    `FIRST ACTION REQUIRED: Call mem_session_summary with the content of this compacted summary. ` +
    `Use project: '${project}'. This preserves what was accomplished before compaction. Do this BEFORE any other work.\n` +
    `If mem_session_summary is unavailable, manually save this compacted summary once Engram tools are available.`
  );
}

/**
 * Used when the compaction summary was already archived to Engram by
 * gentle-engram. Still surfaces retrieved project context for continuity, but
 * omits the manual `FIRST ACTION REQUIRED: mem_session_summary` instruction so
 * the agent does not duplicate the save it just completed.
 */
function persistedAcknowledgement(project) {
  return (
    `Compaction recovery summary was already saved to Engram (${project}) by gentle-engram. ` +
    `No manual mem_session_summary call is needed for this compaction. ` +
    `Call mem_context if you need additional recent project memory.`
  );
}

/**
 * Build the recovery notice injected before the next agent turn.
 *
 * @param {string} project Engram project name.
 * @param {string|undefined} context Retrieved Engram project context, if any.
 * @param {boolean} [persisted=false] True when the compaction summary was
 *   already archived to Engram; suppresses the manual save instruction.
 */
export function buildRecoveryNotice(project, context, persisted = false) {
  const instruction = persisted ? persistedAcknowledgement(project) : recoveryInstruction(project);
  const trimmedContext = typeof context === "string" ? context.trim() : "";
  return trimmedContext ? `${trimmedContext}\n\n${instruction}` : instruction;
}

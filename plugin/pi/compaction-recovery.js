const SUMMARY_FIELD_PATHS = [
  // Current Pi session_compact payloads put the authoritative summary here.
  // Keep it before legacy shapes so conflicting compatibility fields cannot win.
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

export const ArchiveOutcome = Object.freeze({
  Confirmed: "confirmed",
  Failed: "failed",
  Unknown: "unknown",
  Unavailable: "unavailable",
});

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

function persistedAcknowledgement(project) {
  return (
    `Compaction recovery summary was already saved to Engram (${project}) by gentle-engram. ` +
    `No manual mem_session_summary call is needed for this compaction. ` +
    `Call mem_context if you need additional recent project memory.`
  );
}

function unknownArchiveInstruction(project) {
  return (
    `Compaction recovery could not confirm whether the summary was saved to Engram (${project}). ` +
    `Do NOT retry or call mem_session_summary yet, because that could duplicate the summary. ` +
    `First verify with mem_search or mem_doctor; save the summary once only if it is absent.`
  );
}

function unavailableRecoveryInstruction() {
  return (
    "CRITICAL INSTRUCTION FOR COMPACTED SUMMARY:\n" +
    "gentle-engram could not safely confirm the runtime session and project, so it did not archive the summary. " +
    "Before saving manually, verify the active Engram session and project with mem_current_project or mem_doctor. " +
    "Save the summary once only after that verification."
  );
}

export function buildRecoveryNotice(project, context, outcome = ArchiveOutcome.Failed) {
  const normalizedOutcome = outcome === true ? ArchiveOutcome.Confirmed : outcome;
  const instruction = normalizedOutcome === ArchiveOutcome.Confirmed
    ? persistedAcknowledgement(project)
    : normalizedOutcome === ArchiveOutcome.Unknown
      ? unknownArchiveInstruction(project)
      : normalizedOutcome === ArchiveOutcome.Unavailable
        ? unavailableRecoveryInstruction()
        : recoveryInstruction(project);
  const trimmedContext = typeof context === "string" ? context.trim() : "";
  return trimmedContext ? `${trimmedContext}\n\n${instruction}` : instruction;
}

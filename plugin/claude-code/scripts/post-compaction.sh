#!/usr/bin/env bash
# Engram — Post-compaction hook for Claude Code
#
# When compaction happens, inject Memory Protocol + context and instruct
# the agent to persist the compacted summary via mem_session_summary.

# Load shared helpers
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
PROJECT=$(resolve_project "$CWD") || PROJECT=""

# Ensure session exists. Claude keeps the session ID across compaction as it
# does on --resume, so the root may already be ended; resume=true lets the
# server register a live continuation instead of refusing with
# session_already_ended, which would leave every later write denied by the
# PreToolUse gate. Registers exactly like session-start.sh.
if [ -n "$SESSION_ID" ] && [ -n "$PROJECT" ]; then
  # Roots created before Claude registered project_owned sessions are shared;
  # resuming one keeps that mode so its continuation stays resolvable.
  OWNERSHIP_MODE="project_owned"
  ENCODED_SESSION_ID=$(printf '%s' "$SESSION_ID" | jq -sRr @uri 2>/dev/null)
  SESSION=""
  if [ -n "$ENCODED_SESSION_ID" ]; then
    SESSION=$(engram_curl -sf "${ENGRAM_URL}/sessions/${ENCODED_SESSION_ID}" --max-time 1 2>/dev/null)
  fi
  if [ -n "$SESSION" ] && printf '%s' "$SESSION" \
    | jq -e --arg id "$SESSION_ID" '.id == $id and .ended_at != null and .ownership_mode == "shared"' >/dev/null 2>&1; then
    OWNERSHIP_MODE="shared"
  fi
  engram_curl -sf "${ENGRAM_URL}/sessions" \
    -X POST \
    -H "Content-Type: application/json" \
    -d "$(jq -n --arg id "$SESSION_ID" --arg project "$PROJECT" --arg dir "$CWD" --arg mode "$OWNERSHIP_MODE" \
      '{id: $id, project: $project, directory: $dir, ownership_mode: $mode, resume: true}')" \
    > /dev/null
fi

# Fetch context from previous sessions
CONTEXT=""
if [ -n "$PROJECT" ]; then
  ENCODED_PROJECT=$(printf '%s' "$PROJECT" | jq -sRr @uri)
  # Same bounded request as session-start.sh: compact bullets (titles kept,
  # 300-char body previews dropped), a pinned-section ceiling, and a 16 KiB total cap.
  CONTEXT=$(engram_curl -sf "${ENGRAM_URL}/context?project=${ENCODED_PROJECT}&compact=1&pinned=20&max_bytes=16384" --max-time 3 | jq -r '.context // empty' 2>/dev/null)
fi

# Resolve protocol verbosity mode for this slug. All slim/full branching
# (including the engram-version floor check) lives in Go — see `engram
# protocol-mode`. A missing/old engram binary or an unrecognized subcommand
# never yields "slim" here, so this always defaults safely to full. $mode is
# NEVER echoed/logged to this hook's own stdout.
mode=$(engram protocol-mode claude-code 2>/dev/null)
if [ "$mode" != "slim" ]; then
  mode="full"
fi

# Inject Memory Protocol + compaction instruction + context. Only the static
# protocol prose is gated on $mode — the "CRITICAL INSTRUCTION" header below
# and the numbered recovery steps that follow it stay unconditional (they are
# the compaction-recovery contract itself, not the duplicated protocol text).
if [ "$mode" != "slim" ]; then
cat <<'PROTOCOL'
## Engram Persistent Memory — ACTIVE PROTOCOL

You have engram memory tools. The agent MUST follow this protocol without waiting for activation.

### CORE TOOLS — always available, no ToolSearch needed
mem_save, mem_search, mem_context, mem_session_summary, mem_get_observation, mem_save_prompt, mem_current_project, mem_judge, mem_compare

Use ToolSearch for other tools: mem_update, mem_review, mem_pin, mem_unpin, mem_suggest_topic_key, mem_session_start, mem_session_end, mem_doctor, mem_capture_passive

### SELECTIVE MEMORY
Persist verified knowledge that is durable and costly to reconstruct: decisions with rationale,
non-obvious root causes, real constraints, and resumption-relevant pending work.
Group related conclusions deliberately, not every task, response, or event.
Exclude secrets, private data, speculation, raw dumps, and routine progress.

For selected writes, use mem_search on the bounded topic in the intended project and scope before writing.
Read candidates in full with mem_get_observation; snippets are not identity evidence.
For the same bounded topic use mem_update by ID: keep topic_key, preserve every still-valid fact,
and record superseded facts as history. For a genuinely new topic use mem_save with a deliberate,
distinct topic_key. If already covered, write nothing.
On ambiguous identity or failed lookup, do not overwrite or fall back to a duplicate save.
A failed read is not evidence of absence. A suggested topic_key is a hint, not proof of identity.
Never merge across projects or scopes. Structure content as What, Why, Where, Learned.

Honestly report unavailable memory evidence; never claim unconfirmed persistence.
Do not blindly repeat writes. Complete unrelated work even if bookkeeping fails.
Do not add per-event learning sections or ceremonial delivery blocks.

### DELIVERY GUARANTEE
Memory operations are internal bookkeeping, never the user-facing answer. Complete required memory work before composing the completed-task reply; send the complete answer as the final message of the turn with no later tool calls. If memory work fails or needs follow-up, still send the answer.

### SEARCH AND SESSION CLOSE
Search when materially useful for recall or related work; mere project mentions do not require a search.
For recall: mem_context, then mem_search with distinctive keywords, then mem_get_observation for full content.
Narrow by project/type after broad recall as needed.
At significant explicit closure or handoff, call mem_session_summary with Goal, Instructions,
Discoveries, Accomplished, Next Steps, Relevant Files. Ordinary task completion does not require a summary.
Summary persistence does not end the session; Claude's SessionEnd hook owns lifecycle closure.

### PASSIVE CAPTURE — adapter-specific safety net
Claude's SubagentStop hook forwards eligible delegated/subagent output for server-side extraction.
Ordinary main-agent responses are not automatically captured; a numbered "## Key Learnings:" section alone does not guarantee storage.
Use `mem_save` as the primary path for significant, verified knowledge. Explicit
`mem_capture_passive(content)` extraction is a safety net, not a replacement for `mem_save`.

---

PROTOCOL
fi

# Unconditional lead-in for the numbered recovery steps below — this is the
# compaction-recovery contract itself, not duplicated protocol prose, so it
# stays outside the $mode gate even when the static protocol text is slim.
echo "CRITICAL INSTRUCTION POST-COMPACTION — follow these steps IN ORDER:"

printf "\n1. FIRST: Call mem_session_summary with the content of the compacted summary above. Use project: '%s'.\n" "$PROJECT"
printf "   This preserves what was accomplished before compaction.\n\n"
printf "2. THEN: Call mem_context with project: '%s' to recover recent session history and observations.\n" "$PROJECT"
printf "   Read the returned context carefully — it tells you what was being worked on.\n\n"
cat <<'PROTOCOL'
3. If you need more detail on a specific topic, call mem_search with relevant keywords.

4. Only THEN continue working on what the user asked.

All 4 steps are MANDATORY. Without them, you lose context and start blind.
PROTOCOL

# Inject memory context if available
if [ -n "$CONTEXT" ]; then
  printf "\n%s\n" "$CONTEXT"
fi

exit 0

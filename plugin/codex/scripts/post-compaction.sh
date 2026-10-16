#!/usr/bin/env bash
# Engram — Post-compaction hook for Codex
#
# When compaction happens, inject Memory Protocol + context and instruct
# the agent to persist the compacted summary via mem_session_summary.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_EXTERNAL_URL="${ENGRAM_URL:-}"
ENGRAM_EXTERNAL_URL="${ENGRAM_EXTERNAL_URL#"${ENGRAM_EXTERNAL_URL%%[![:space:]]*}"}"
ENGRAM_EXTERNAL_URL="${ENGRAM_EXTERNAL_URL%"${ENGRAM_EXTERNAL_URL##*[![:space:]]}"}"
ENGRAM_URL="${ENGRAM_EXTERNAL_URL:-http://127.0.0.1:${ENGRAM_PORT}}"

# Load shared helpers
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
PROJECT=$(resolve_project "$CWD") || PROJECT=""

# Register and retain only the server-confirmed runtime identity.
SESSION_HANDOFF=$(engram_session_handoff "$INPUT" "$PROJECT" "$CWD" codex-resolve) && REGISTERED=1 || REGISTERED=0

# Fetch context from previous sessions
CONTEXT=""
if [ -n "$PROJECT" ]; then
  ENCODED_PROJECT=$(printf '%s' "$PROJECT" | jq -sRr @uri)
  CONTEXT=$(curl -sf "${ENGRAM_URL}/context?project=${ENCODED_PROJECT}" --max-time 3 2>/dev/null | jq -r '.context // empty')
fi

# Inject Memory Protocol + compaction instruction + context
printf '%s\n' "$SESSION_HANDOFF"
if [ "$REGISTERED" -ne 1 ]; then
  printf '%s\n' 'Registration was not confirmed after compaction. Keep the compacted summary and any retained context for the current task; do not invoke agent-attributed memory tools until the host hook re-registers the runtime session.'
  if [ -n "$CONTEXT" ]; then
    printf '\n%s\n' "$CONTEXT"
  fi
  exit 0
fi
cat <<'PROTOCOL'
## Engram Persistent Memory — ACTIVE PROTOCOL

You have Engram memory tools. The agent MUST follow this protocol without waiting for activation.

### AVAILABLE TOOLS
Use the Engram tools actually exposed by Codex, with their registered MCP names
(`mcp__engram__mem_*` or `mcp__plugin_engram_engram__mem_*`). The names below are
logical names; availability and discovery follow Codex's host-specific interface.

mem_save, mem_search, mem_context, mem_session_summary, mem_get_observation, mem_save_prompt, mem_current_project, mem_judge, mem_compare
mem_update, mem_review, mem_pin, mem_unpin, mem_suggest_topic_key, mem_session_start, mem_session_end, mem_doctor, mem_capture_passive

### PROACTIVE SAVE — do NOT wait for user to ask
Call `mem_save` IMMEDIATELY after ANY of these:
- Decision made (architecture, convention, workflow, tool choice)
- Bug fixed (include root cause)
- Convention or workflow documented/updated
- Notion/Jira/GitHub artifact created or updated with significant content
- Non-obvious discovery, gotcha, or edge case found
- Pattern established (naming, structure, approach)
- User preference or constraint learned
- Feature implemented with non-obvious approach
- Configuration change or environment setup done
- User confirms your recommendation
- User rejects an approach or expresses a preference
- Discussion concludes with a clear direction chosen

**Self-check after EVERY task**: "Did I or the user just make a decision, confirm a recommendation, express a preference, fix a bug, learn something, or establish a convention? If yes → mem_save NOW."

### DELIVERY GUARANTEE
Memory operations are internal bookkeeping, never the user-facing answer. Complete required memory work before composing the completed-task reply; send the complete answer as the final message of the turn with no later tool calls. If memory work fails or needs follow-up, still send the answer.

### SEARCH MEMORY when:
- User asks to recall anything ("remember", "what did we do", or the equivalent in the user's language)
- Starting work on something that might have been done before
- User mentions a topic you have no context on

### SESSION CLOSE — before saying "done":
Call `mem_session_summary` with: Goal, Instructions, Discoveries, Accomplished, Next Steps, Relevant Files.
Summary persistence does not end the session; Codex's SessionEnd hook owns lifecycle closure.

### PASSIVE CAPTURE — adapter-specific safety net
Codex's SubagentStop hook forwards eligible delegated/subagent output for server-side extraction.
Ordinary main-agent responses are not automatically captured; a numbered "## Key Learnings:" section alone does not guarantee storage.
Use `mem_save` as the primary path for significant, verified knowledge. Explicit
`mem_capture_passive(content)` extraction is a safety net, not a replacement for `mem_save`.

### SESSION BINDING
Codex's PreToolUse command `engram hook codex-pre-tool-use` matches
`mcp__engram__mem_*` and `mcp__plugin_engram_engram__mem_*`. Supported mutating
Engram calls resolve the authoritative host session before binding `session_id`
for writes and `id` for session start/end. Never invent or override this identity.
Read-only tools are unchanged; malformed input or unconfirmed host resolution denies mutating calls.
For direct/nonintercepted clients, reuse the confirmed lifecycle identity supplied above.

---

CRITICAL INSTRUCTION POST-COMPACTION — follow these steps IN ORDER:
PROTOCOL

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

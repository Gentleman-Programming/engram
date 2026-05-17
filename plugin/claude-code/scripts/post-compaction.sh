#!/bin/bash
# Engram — Post-compaction hook for Claude Code
#
# When compaction happens, inject Memory Protocol + context and instruct
# the agent to persist the compacted summary via mem_session_summary.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

# Load shared helpers
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
PROJECT=$(detect_project "$CWD")

# Ensure session exists
if [ -n "$SESSION_ID" ] && [ -n "$PROJECT" ]; then
  curl -sf "${ENGRAM_URL}/sessions" \
    -X POST \
    -H "Content-Type: application/json" \
    -d "$(jq -n --arg id "$SESSION_ID" --arg project "$PROJECT" --arg dir "$CWD" \
      '{id: $id, project: $project, directory: $dir}')" \
    > /dev/null 2>&1
fi

# Fetch context from previous sessions
ENCODED_PROJECT=$(printf '%s' "$PROJECT" | jq -sRr @uri)
CONTEXT=$(curl -sf "${ENGRAM_URL}/context?project=${ENCODED_PROJECT}" --max-time 3 2>/dev/null | jq -r '.context // empty')

# Inject Memory Protocol + compaction instruction + context
cat <<'PROTOCOL'
## Engram Persistent Memory — ACTIVE PROTOCOL

You have engram memory tools. This protocol is MANDATORY and ALWAYS ACTIVE.

### CORE TOOLS — always available, no ToolSearch needed
mem_save, mem_search, mem_context, mem_session_summary, mem_get_observation, mem_save_prompt

Use ToolSearch for other tools: mem_update, mem_suggest_topic_key, mem_session_start, mem_session_end, mem_stats, mem_delete, mem_timeline, mem_capture_passive

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

**Self-check after EVERY task**: "Did I just make a decision, fix a bug, learn something, or establish a convention? If yes → mem_save NOW."

### SEARCH MEMORY when:
- User asks to recall anything ("remember", "what did we do", or the equivalent in the user's language)
- Starting work on something that might have been done before
- User mentions a topic you have no context on

### SESSION CLOSE — before saying "done":
Call `mem_session_summary` with: Goal, Discoveries, Accomplished, Next Steps, Relevant Files.

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

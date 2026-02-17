#!/bin/bash
# Engram — Post-compaction hook for Claude Code
#
# When compaction happens, inject previous session context and instruct
# the agent to persist the compacted summary via mem_session_summary.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

# Read hook input from stdin
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
PROJECT=$(basename "$CWD")

# Ensure session exists
if [ -n "$SESSION_ID" ] && [ -n "$PROJECT" ]; then
  curl -sf "${ENGRAM_URL}/sessions" \
    -X POST \
    -H "Content-Type: application/json" \
    -d "{\"id\":\"${SESSION_ID}\",\"project\":\"${PROJECT}\",\"directory\":\"${CWD}\"}" \
    > /dev/null 2>&1
fi

# Inject context from previous sessions
CONTEXT=$(curl -sf "${ENGRAM_URL}/context?project=${PROJECT}" --max-time 3 2>/dev/null | jq -r '.context // empty')

# Build output for Claude
cat <<EOF
${CONTEXT}

CRITICAL INSTRUCTION POST-COMPACTION:
You have access to Engram persistent memory via MCP tools (mem_save, mem_search, mem_session_summary, etc.).
FIRST ACTION REQUIRED: Call mem_session_summary with the content of the compacted summary above. Use project: '${PROJECT}'.
This preserves what was accomplished before compaction. Do this BEFORE any other work.
This is NOT optional. Without this, everything done before compaction is lost from memory.
EOF

exit 0

#!/bin/bash
# Engram — SessionStart hook for Claude Code
#
# 1. Ensures the engram server is running
# 2. Creates a session in engram
# 3. Auto-imports git-synced chunks if .engram/manifest.json exists
# 4. Injects memory context into the session

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

# Read hook input from stdin
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
PROJECT=$(basename "$CWD")

# Ensure engram server is running
if ! curl -sf "${ENGRAM_URL}/health" --max-time 1 > /dev/null 2>&1; then
  engram serve &>/dev/null &
  sleep 0.5
fi

# Create session
if [ -n "$SESSION_ID" ] && [ -n "$PROJECT" ]; then
  curl -sf "${ENGRAM_URL}/sessions" \
    -X POST \
    -H "Content-Type: application/json" \
    -d "{\"id\":\"${SESSION_ID}\",\"project\":\"${PROJECT}\",\"directory\":\"${CWD}\"}" \
    > /dev/null 2>&1
fi

# Auto-import git-synced chunks
if [ -f "${CWD}/.engram/manifest.json" ]; then
  engram sync --import 2>/dev/null
fi

# Inject memory context — stdout goes to Claude as additionalContext
CONTEXT=$(curl -sf "${ENGRAM_URL}/context?project=${PROJECT}" --max-time 3 2>/dev/null | jq -r '.context // empty')

if [ -n "$CONTEXT" ]; then
  cat <<EOF
${CONTEXT}
EOF
fi

exit 0

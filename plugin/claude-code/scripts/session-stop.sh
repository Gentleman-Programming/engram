#!/usr/bin/env bash
# Engram — Stop hook for Claude Code (async)
#
# Marks the session as ended via the HTTP API.
# Runs async so it doesn't block Claude's response.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')

if [ -z "$SESSION_ID" ]; then
  exit 0
fi

engram_curl -sf "${ENGRAM_URL}/sessions/${SESSION_ID}/end" \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{}' \
  > /dev/null

exit 0

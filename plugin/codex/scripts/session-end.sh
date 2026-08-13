#!/bin/bash
# Engram — SessionEnd hook for Codex (synchronous)
#
# Marks the session as ended via the HTTP API.
# Runs synchronously (Codex does not support async: true).
# The HTTP call is bounded to leave process overhead within Codex's 3s timeout.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty' 2>/dev/null)

if [ -z "$SESSION_ID" ]; then
  exit 0
fi

curl -sf "${ENGRAM_URL}/sessions/${SESSION_ID}/end" \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{}' \
  --max-time 2 \
  > /dev/null 2>&1

exit 0

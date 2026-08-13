#!/bin/bash
# Engram — Stop hook for Codex (synchronous)
#
# Marks the session as ended via the HTTP API.
# Runs synchronously (Codex does not support async: true).
# The HTTP call is fast enough to complete well within the 5s timeout.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty' 2>/dev/null)

if [ -n "$SESSION_ID" ]; then
  curl -sf "${ENGRAM_URL}/sessions/${SESSION_ID}/end" \
    -X POST \
    -H "Content-Type: application/json" \
    -d '{}' \
    --max-time 4 \
    > /dev/null 2>&1
fi

# Codex validates Stop-hook stdout as JSON on exit 0; empty output is rejected.
printf '{"continue":true,"suppressOutput":false}'
exit 0

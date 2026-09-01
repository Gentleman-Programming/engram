#!/usr/bin/env bash
# Engram — SessionEnd hook for Codex (synchronous)
#
# Marks the session as ended via the HTTP API.
# Runs synchronously (Codex does not support async: true).
# The HTTP call is bounded to leave process overhead within Codex's 3s timeout.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"

case "$ENGRAM_PORT" in
  *[!0-9]*|'') exit 0 ;;
esac

while [ "${ENGRAM_PORT#0}" != "$ENGRAM_PORT" ]; do
  ENGRAM_PORT="${ENGRAM_PORT#0}"
done

case "$ENGRAM_PORT" in
  ''|??????*) exit 0 ;;
esac

if [ "$ENGRAM_PORT" -gt 65535 ]; then
  exit 0
fi

INPUT=$(cat)
SESSION_ID=$(printf '%s' "$INPUT" | jq -r 'if (.session_id | type) == "string" and .session_id != "" then .session_id | @uri else empty end' 2>/dev/null)

if [ -z "$SESSION_ID" ]; then
  exit 0
fi

curl -sf "http://127.0.0.1:${ENGRAM_PORT}/sessions/${SESSION_ID}/end" \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{}' \
  --max-time 2 \
  > /dev/null 2>&1

exit 0

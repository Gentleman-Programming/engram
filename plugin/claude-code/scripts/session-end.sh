#!/usr/bin/env bash
# Engram — SessionEnd hook for Claude Code
#
# Marks the session as ended via the HTTP API. This adapter is intentionally
# synchronous, silent, and fail-open so SessionEnd waits only for its bounded
# local transport.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=_helpers.sh
source "${SCRIPT_DIR}/_helpers.sh" "__engram_hook_default_max_time=2"

INPUT=$(cat)
SESSION_ID=$(printf '%s' "$INPUT" | jq -er '
  if (.session_id | type) == "string" and (.session_id | length) > 0
  then .session_id
  else empty
  end
' 2>/dev/null) || exit 0
ENCODED_SESSION_ID=$(printf '%s' "$SESSION_ID" | jq -sRr @uri 2>/dev/null) || exit 0

[ -n "$ENCODED_SESSION_ID" ] || exit 0

engram_curl -sf "${ENGRAM_URL}/sessions/${ENCODED_SESSION_ID}/end" \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{}' \
  > /dev/null 2>&1

exit 0

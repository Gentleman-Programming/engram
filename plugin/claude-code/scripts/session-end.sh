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

# A resumed Claude session keeps its ID while the original session is already
# ended; close its live continuation through the runtime scope instead.
SESSION=$(engram_curl -sf "${ENGRAM_URL}/sessions/${ENCODED_SESSION_ID}" --max-time 1 2>/dev/null)
if [ -n "$SESSION" ] && RUNTIME_SCOPE=$(printf '%s' "$SESSION" | jq -ce --arg id "$SESSION_ID" '
  select(.id == $id and .ended_at != null and (.project // "") != "" and (.directory // "") != "")
  | {id, project, directory, ownership_mode: (.ownership_mode // "shared")}
' 2>/dev/null); then
  HEALTH=$(engram_curl -s "${ENGRAM_URL}/health" --no-location --retry 0 --max-time 0.5 -w '\n%{http_code}' 2>/dev/null) || exit 0
  [ "${HEALTH##*$'\n'}" = 200 ] || exit 0
  HEALTH=${HEALTH%$'\n'*}
  END_MODE=$(printf '%s' "$HEALTH" | jq -ers '
    select(length == 1) | .[0] |
    select(type == "object" and .status == "ok") | (if has("capabilities") then .capabilities else {} end) |
    if type != "object" then empty
    elif .runtime_session_conditional_end == true then "conditional"
    elif (has("runtime_session_conditional_end") | not) or .runtime_session_conditional_end == false then "legacy"
    else empty end
  ' 2>/dev/null) || exit 0
  case "$END_MODE" in
    conditional)
      RESOLVED=$(engram_curl -s "${ENGRAM_URL}/runtime-sessions/resolve" --no-location --retry 0 --max-time 1 -w '\n%{http_code}' \
        -H "Content-Type: application/json" -d "$RUNTIME_SCOPE" 2>/dev/null) || exit 0
      [ "${RESOLVED##*$'\n'}" = 200 ] || exit 0
      # A later resume may replace the continuation while this callback is in flight.
      RUNTIME_SCOPE=$(printf '%s' "${RESOLVED%$'\n'*}" | jq -ces --arg id "$SESSION_ID" --argjson scope "$RUNTIME_SCOPE" '
        select(length == 1) | .[0] | select(.status == "resolved" and .resumed_from == $id) | .id |
        select(type == "string" and startswith($id + ":resume:") and
          (ltrimstr($id + ":resume:") | test("\\A[0-9]+\\z"))) |
        $scope + {expected_effective_id: .}
      ' 2>/dev/null) || exit 0
      ;;
    legacy)
      # Object parsing keeps only the last value for a duplicate JSON key.
      # A discarded advertisement must never authorize an unfenced end.
      printf '%s' "$HEALTH" | jq -en --stream '
        ["capabilities", "runtime_session_conditional_end"] as $capability |
        [limit(2; inputs | select(length == 2 and .[0][:2] == $capability))] |
        . == [] or . == [[$capability, false]]
      ' >/dev/null 2>&1 || exit 0
      ;;
    *) exit 0 ;;
  esac
  engram_curl -sf "${ENGRAM_URL}/runtime-sessions/end" --no-location --retry 0 \
    -X POST \
    -H "Content-Type: application/json" \
    -d "$RUNTIME_SCOPE" \
    > /dev/null 2>&1
  exit 0
fi

engram_curl -sf "${ENGRAM_URL}/sessions/${ENCODED_SESSION_ID}/end" \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{}' \
  > /dev/null 2>&1

exit 0

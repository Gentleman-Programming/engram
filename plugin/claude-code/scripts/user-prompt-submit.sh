#!/bin/bash
# Engram — UserPromptSubmit hook (save-nudge only)
#
# Checks when the last mem_save was for the current project.
# If > 15 minutes AND session > 5 minutes, injects a reminder.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

INPUT=$(cat)
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
PROJECT=$(detect_project "$CWD")

OUTPUT="{}"

if [ -z "$PROJECT" ]; then
  echo "$OUTPUT"
  exit 0
fi

# Check session age — skip nudge if session < 5 minutes
# Note: there is no GET /sessions/:id endpoint. Use GET /sessions/recent and filter.
SESSION_START=""
if [ -n "$SESSION_ID" ]; then
  SESSION_START=$(curl -sf "${ENGRAM_URL}/sessions/recent?limit=10" --max-time 0.2 2>/dev/null \
    | jq -r --arg sid "$SESSION_ID" '[.[] | select(.id == $sid)][0].started_at // empty' 2>/dev/null)
fi

if [ -n "$SESSION_START" ]; then
  SESSION_START_EPOCH=$(date -d "${SESSION_START%%.*}" "+%s" 2>/dev/null \
    || date -j -f "%Y-%m-%dT%H:%M:%S" "${SESSION_START%%.*}" "+%s" 2>/dev/null \
    || echo "0")
  NOW_EPOCH=$(date "+%s")
  SESSION_AGE_SECS=$(( NOW_EPOCH - SESSION_START_EPOCH ))

  if [ "$SESSION_AGE_SECS" -lt 300 ]; then
    echo "$OUTPUT"
    exit 0
  fi
fi

# Check last save time (use /observations/recent which sorts by created_at DESC)
ENCODED_PROJECT=$(printf '%s' "$PROJECT" | jq -sRr @uri)
LAST_SAVE_JSON=$(curl -sf \
  "${ENGRAM_URL}/observations/recent?project=${ENCODED_PROJECT}&limit=1" \
  --max-time 0.2 2>/dev/null)

if [ -z "$LAST_SAVE_JSON" ]; then
  echo "$OUTPUT"
  exit 0
fi

LAST_SAVE_AT=$(echo "$LAST_SAVE_JSON" | jq -r '.[0].created_at // empty' 2>/dev/null)

if [ -z "$LAST_SAVE_AT" ]; then
  echo "$OUTPUT"
  exit 0
fi

LAST_EPOCH=$(date -d "${LAST_SAVE_AT%%.*}" "+%s" 2>/dev/null \
  || date -j -f "%Y-%m-%dT%H:%M:%S" "${LAST_SAVE_AT%%.*}" "+%s" 2>/dev/null \
  || echo "0")
NOW_EPOCH=$(date "+%s")
ELAPSED=$(( NOW_EPOCH - LAST_EPOCH ))

if [ "$ELAPSED" -gt 900 ]; then
  OUTPUT=$(jq -n \
    '{"systemMessage": "MEMORY REMINDER: It'\''s been over 15 minutes since your last save. If you'\''ve made decisions, discoveries, or completed significant work, call mem_save now."}')
fi

echo "$OUTPUT"
exit 0

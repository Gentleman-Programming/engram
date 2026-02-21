#!/bin/bash
# Engram — Stop hook for Claude Code (async)
#
# Runs when the Claude Code session ends. Checks whether mem_session_summary
# was called during this session. If not, logs a warning to help diagnose
# missed session summaries.
#
# Runs async — does NOT block Claude's response.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"
LOG_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/engram/logs"
LOG_FILE="${LOG_DIR}/session-stop.log"

mkdir -p "$LOG_DIR"
log() { echo "[$(date -Iseconds)] [session-stop] $*" >> "$LOG_FILE"; }

# Read hook input from stdin
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
PROJECT=$(basename "$CWD")

log "Session stopping. session=$SESSION_ID project=$PROJECT"

# Verify Engram is running
if ! curl -sf "${ENGRAM_URL}/health" --max-time 2 > /dev/null 2>&1; then
  log "Engram server not running. Cannot check session summary."
  exit 0
fi

# Check if mem_session_summary was called for this session
SESSION_DATA=$(curl -sf "${ENGRAM_URL}/sessions/${SESSION_ID}" --max-time 3 2>/dev/null || true)

if [ -z "$SESSION_DATA" ]; then
  log "Could not fetch session data for session=$SESSION_ID"
  exit 0
fi

# Check if summary exists for this session
HAS_SUMMARY=$(echo "$SESSION_DATA" | jq -r '.has_summary // .summary_count // 0' 2>/dev/null || echo "0")

if [ "$HAS_SUMMARY" = "0" ] || [ "$HAS_SUMMARY" = "false" ] || [ "$HAS_SUMMARY" = "null" ]; then
  log "WARNING: Session $SESSION_ID ended without mem_session_summary. Project: $PROJECT"
  log "This session's context may not be recoverable in future sessions."
else
  log "Session $SESSION_ID closed cleanly with summary. Project: $PROJECT"
fi

# Log session observation count for metrics
OBS_COUNT=$(curl -sf "${ENGRAM_URL}/observations?session_id=${SESSION_ID}&count=true" --max-time 3 2>/dev/null | jq -r '.count // 0' 2>/dev/null || echo "0")
log "Session $SESSION_ID metrics: observations=$OBS_COUNT"

exit 0

#!/bin/bash
# Engram — Stop hook for Claude Code (async)
#
# Logs session end. The Memory Protocol instructs the agent
# to call mem_session_summary before ending.
# Runs async — does NOT block Claude's response.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"
LOG_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/engram/logs"
LOG_FILE="${LOG_DIR}/session-stop.log"

mkdir -p "$LOG_DIR"

# Read hook input from stdin
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
PROJECT=$(basename "$CWD")

echo "[$(date -Iseconds)] [session-stop] Session ended. session=$SESSION_ID project=$PROJECT" >> "$LOG_FILE"

exit 0

#!/bin/bash
# Engram — UserPromptSubmit hook for Claude Code
#
# On the FIRST message of a session: injects a ToolSearch instruction to force
# Claude Code to load all engram memory tools (which are deferred by default).
#
# On subsequent messages: checks when the last mem_save was for the current
# project. If it's been > 15 minutes AND the session has been active > 5
# minutes, injects a nudge reminding the agent to save.
#
# MUST exit 0 always and output valid JSON — otherwise Claude Code blocks the message.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

# Windows Git Bash/MSYS2 can fail while forking helper processes under
# enterprise Defender/EDR, which makes Claude Code wait on prompt submission.
# Keep the Windows path bash-builtin-only: no jq, git, curl, date, dirname, cat,
# touch, or command substitutions. It preserves first-message tool loading and
# degrades subsequent save nudges to a fast no-op instead of risking a hang.
is_windows_bash() {
  case "${OSTYPE:-}" in
    msys*|cygwin*|win32*) return 0 ;;
  esac
  [ -n "${MSYSTEM:-}" ] || [ -n "${MINGW_PREFIX:-}" ]
}

set_json_string_value() {
  local key="$1"
  local json="$2"
  local pattern='"'"$key"'"[[:space:]]*:[[:space:]]*"([^"]*)"'
  JSON_VALUE=""
  if [[ "$json" =~ $pattern ]]; then
    JSON_VALUE="${BASH_REMATCH[1]}"
  fi
}

sanitize_session_key_part() {
  local raw="$1"
  local safe=""
  local i char
  for (( i=0; i<${#raw}; i++ )); do
    char="${raw:i:1}"
    if [[ "$char" =~ [a-zA-Z0-9_-] ]]; then
      safe+="$char"
    else
      safe+="_"
    fi
  done
  JSON_VALUE="$safe"
}

print_toolsearch_message() {
  printf '%s\n' '{"systemMessage":"CRITICAL FIRST ACTION — Execute this ToolSearch NOW before responding to the user:\nselect:mcp__engram__mem_save,mcp__engram__mem_search,mcp__engram__mem_context,mcp__engram__mem_session_summary,mcp__engram__mem_session_start,mcp__engram__mem_session_end,mcp__engram__mem_get_observation,mcp__engram__mem_suggest_topic_key,mcp__engram__mem_capture_passive,mcp__engram__mem_save_prompt,mcp__engram__mem_update,mcp__engram__mem_current_project,mcp__engram__mem_judge\n\nAfter loading tools, call mem_context to check for prior session history before responding."}'
}

if is_windows_bash && [ "${ENGRAM_CLAUDE_WINDOWS_BASH_SAFE_MODE:-auto}" != "0" ]; then
  INPUT=""
  while IFS= read -r LINE || [ -n "$LINE" ]; do
    INPUT+="${LINE}"$'\n'
  done

  set_json_string_value "session_id" "$INPUT"
  SESSION_ID="$JSON_VALUE"
  if [ -n "$SESSION_ID" ]; then
    sanitize_session_key_part "$SESSION_ID"
    SESSION_KEY="engram-claude-${JSON_VALUE}-tools-loaded"
  else
    SESSION_KEY="engram-claude-windows-$$-tools-loaded"
  fi
  STATE_DIR="${TMPDIR:-/tmp}"
  STATE_FILE="${STATE_DIR}/${SESSION_KEY}"

  if [ ! -f "$STATE_FILE" ]; then
    : > "$STATE_FILE" 2>/dev/null || true
    print_toolsearch_message
    exit 0
  fi

  printf '%s\n' '{}'
  exit 0
fi

# Load shared helpers after the Windows-safe fast path so Git Bash does not fork
# for dirname/pwd before deciding whether the safe path applies.
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

# Read hook input from stdin
INPUT=$(cat)
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')

parse_epoch() {
  TS="$1"
  if [ -z "$TS" ]; then
    return 1
  fi

  # Drop fractional seconds without dropping timezone information.
  if [[ "$TS" == *.* ]]; then
    TS_PREFIX="${TS%%.*}"
    TS_SUFFIX="${TS#*.}"
    case "$TS_SUFFIX" in
      *Z) TS="${TS_PREFIX}Z" ;;
      *+*) TS="${TS_PREFIX}+${TS_SUFFIX#*+}" ;;
      *-*) TS="${TS_PREFIX}-${TS_SUFFIX#*-}" ;;
      *) TS="$TS_PREFIX" ;;
    esac
  fi

  # BSD date accepts numeric RFC3339 offsets with %z, but requires +HHMM.
  if [[ "$TS" =~ ^([0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})([+-][0-9]{2}):([0-9]{2})$ ]]; then
    TZ_TS="${BASH_REMATCH[1]}${BASH_REMATCH[2]}${BASH_REMATCH[3]}"
    date -j -f "%Y-%m-%dT%H:%M:%S%z" "$TZ_TS" "+%s" 2>/dev/null && return 0
  fi
  if [[ "$TS" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}[+-][0-9]{4}$ ]]; then
    date -j -f "%Y-%m-%dT%H:%M:%S%z" "$TS" "+%s" 2>/dev/null && return 0
  fi

  if [[ "$TS" == *Z ]]; then
    Z_TS="${TS%Z}"
    date -j -u -f "%Y-%m-%dT%H:%M:%S" "$Z_TS" "+%s" 2>/dev/null && return 0
  fi

  date -j -f "%Y-%m-%dT%H:%M:%S" "$TS" "+%s" 2>/dev/null \
    || date -j -f "%Y-%m-%d %H:%M:%S" "$TS" "+%s" 2>/dev/null \
    || date -d "$TS" "+%s" 2>/dev/null
}

# Default: no injection
OUTPUT="{}"

# ──────────────────────────────────────────────────────────────────────────────
# FIRST-MESSAGE DETECTION
#
# Use a state file per session to determine if this is the first user message.
# State file lives in /tmp and is keyed by session_id (falls back to project+pid).
# ──────────────────────────────────────────────────────────────────────────────

# Build a stable session key — prefer SESSION_ID, fall back to project name
if [ -n "$SESSION_ID" ]; then
  SESSION_KEY="engram-claude-${SESSION_ID}-tools-loaded"
else
  # No session ID available — only then detect project for the fallback state key.
  PROJECT=$(detect_project "$CWD")
  SAFE_PROJECT=$(printf '%s' "${PROJECT:-unknown}" | tr -cs 'a-zA-Z0-9_-' '_')
  SESSION_KEY="engram-claude-${SAFE_PROJECT}-$$-tools-loaded"
fi

STATE_FILE="/tmp/${SESSION_KEY}"

if [ ! -f "$STATE_FILE" ]; then
  # ── FIRST MESSAGE ────────────────────────────────────────────────────────────
  # Create the state file immediately to prevent repeat injections
  touch "$STATE_FILE" 2>/dev/null || true

  # Inject ToolSearch + mem_context instruction.
  print_toolsearch_message
  exit 0
fi

# ──────────────────────────────────────────────────────────────────────────────
# SUBSEQUENT MESSAGES — existing save-nudge logic
# ──────────────────────────────────────────────────────────────────────────────

# Detect project only after the first-message path has had a chance to return.
if [ -z "${PROJECT:-}" ]; then
  PROJECT=$(detect_project "$CWD")
fi

# Bail early if we can't determine the project
if [ -z "$PROJECT" ]; then
  echo "$OUTPUT"
  exit 0
fi

# Get session start time to check if session is > 5 minutes old
SESSION_START=""
if [ -n "$SESSION_ID" ]; then
  SESSION_START=$(curl -sf "${ENGRAM_URL}/sessions/${SESSION_ID}" --max-time 0.2 2>/dev/null \
    | jq -r '.started_at // empty' 2>/dev/null)
fi

# Check session age — skip nudge if session is new (< 5 minutes)
if [ -n "$SESSION_START" ]; then
  SESSION_START_EPOCH=$(parse_epoch "$SESSION_START")
  if [ -z "$SESSION_START_EPOCH" ]; then
    echo "$OUTPUT"
    exit 0
  fi
  NOW_EPOCH=$(date "+%s")
  SESSION_AGE_SECS=$(( NOW_EPOCH - SESSION_START_EPOCH ))

  if [ "$SESSION_AGE_SECS" -lt 300 ]; then
    # Session < 5 minutes old — no nudge yet
    echo "$OUTPUT"
    exit 0
  fi
fi

# Fetch the most recent observation for this project (any type)
ENCODED_PROJECT=$(printf '%s' "$PROJECT" | jq -sRr @uri)
LAST_SAVE_JSON=$(curl -sf \
  "${ENGRAM_URL}/observations?project=${ENCODED_PROJECT}&limit=1&sort=created_at:desc" \
  --max-time 0.2 2>/dev/null)

if [ -z "$LAST_SAVE_JSON" ]; then
  # Server not responding or slow — fail silently, no nudge
  echo "$OUTPUT"
  exit 0
fi

LAST_SAVE_AT=$(echo "$LAST_SAVE_JSON" | jq -r '.[0].created_at // empty' 2>/dev/null)

if [ -z "$LAST_SAVE_AT" ]; then
  # No observations yet — no nudge (session might just be starting)
  echo "$OUTPUT"
  exit 0
fi

# Parse last save timestamp and compare to now
LAST_EPOCH=$(parse_epoch "$LAST_SAVE_AT")
if [ -z "$LAST_EPOCH" ]; then
  echo "$OUTPUT"
  exit 0
fi
NOW_EPOCH=$(date "+%s")
ELAPSED=$(( NOW_EPOCH - LAST_EPOCH ))

# Nudge if last save was > 15 minutes ago (900 seconds)
if [ "$ELAPSED" -gt 900 ]; then
  OUTPUT=$(jq -n \
    '{"systemMessage": "MEMORY REMINDER: It'\''s been over 15 minutes since your last save. If you'\''ve made decisions, discoveries, or completed significant work, call mem_save now."}')
fi

echo "$OUTPUT"
exit 0

#!/bin/bash
# Engram — UserPromptSubmit hook
#
# Injects up to 3 components into systemMessage on every turn:
# 1. Memory save nudge (conditional: >15min since last save, session >5min)
# 2. File manifest (parsed from session transcript)
# 3. RTK redirect instruction (always)

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

INPUT=$(cat)
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
TRANSCRIPT_PATH=$(echo "$INPUT" | jq -r '.transcript_path // empty')
PROJECT=$(detect_project "$CWD")

# --- Component 1: Memory nudge (conditional) ---
NUDGE=""

if [ -n "$PROJECT" ]; then
  SESSION_START=""
  if [ -n "$SESSION_ID" ]; then
    SESSION_START=$(curl -sf "${ENGRAM_URL}/sessions/recent?limit=50" --max-time 0.2 2>/dev/null \
      | jq -r --arg sid "$SESSION_ID" '[.[] | select(.id == $sid)][0].started_at // empty' 2>/dev/null)
  fi

  SESSION_OLD_ENOUGH=false
  if [ -n "$SESSION_START" ]; then
    SESSION_START_EPOCH=$(date -d "${SESSION_START%%.*}" "+%s" 2>/dev/null \
      || date -j -f "%Y-%m-%dT%H:%M:%S" "${SESSION_START%%.*}" "+%s" 2>/dev/null \
      || echo "0")
    NOW_EPOCH=$(date "+%s")
    SESSION_AGE_SECS=$(( NOW_EPOCH - SESSION_START_EPOCH ))
    [ "$SESSION_AGE_SECS" -ge 300 ] && SESSION_OLD_ENOUGH=true
  fi

  if [ "$SESSION_OLD_ENOUGH" = "true" ]; then
    ENCODED_PROJECT=$(printf '%s' "$PROJECT" | jq -sRr @uri)
    LAST_SAVE_JSON=$(curl -sf \
      "${ENGRAM_URL}/observations/recent?project=${ENCODED_PROJECT}&limit=1" \
      --max-time 0.2 2>/dev/null)

    if [ -n "$LAST_SAVE_JSON" ]; then
      LAST_SAVE_AT=$(echo "$LAST_SAVE_JSON" | jq -r '.[0].created_at // empty' 2>/dev/null)
      if [ -n "$LAST_SAVE_AT" ]; then
        LAST_EPOCH=$(date -d "${LAST_SAVE_AT%%.*}" "+%s" 2>/dev/null \
          || date -j -f "%Y-%m-%dT%H:%M:%S" "${LAST_SAVE_AT%%.*}" "+%s" 2>/dev/null \
          || echo "0")
        NOW_EPOCH=$(date "+%s")
        ELAPSED=$(( NOW_EPOCH - LAST_EPOCH ))
        if [ "$ELAPSED" -gt 900 ]; then
          NUDGE="MEMORY REMINDER: It's been over 15 minutes since your last save. If you've made decisions, discoveries, or completed significant work, call mem_save now."
        fi
      fi
    fi
  fi
fi

# --- Component 2: File manifest (from session transcript) ---
MANIFEST=""
if [ -n "$TRANSCRIPT_PATH" ] && [ -f "$TRANSCRIPT_PATH" ]; then
  # Extract unique file paths from Read tool calls and rtk read Bash calls
  # Only look at assistant messages with tool_use
  MANIFEST=$(grep '"tool_use"' "$TRANSCRIPT_PATH" 2>/dev/null | jq -r '
    .message.content[]? |
    select(.type == "tool_use") |
    if .name == "Read" then .input.file_path // empty
    elif .name == "Bash" then
      (.input.command // "") |
      capture("^rtk read\\s+(?:-[a-zA-Z]\\s+)*(?<path>\\S+)") |
      .path // empty
    else empty
    end
  ' 2>/dev/null | sort -u | tail -15 | while read -r fp; do
    [ -z "$fp" ] && continue
    # Expand ~
    fp="${fp/#\~/$HOME}"
    if [ -f "$fp" ]; then
      KB=$(( $(stat -c '%s' "$fp" 2>/dev/null || echo 0) / 1024 ))
      SHORT=$(echo "$fp" | rev | cut -d/ -f1-2 | rev)
      echo "- ${SHORT} (${KB} KB)"
    fi
  done)

  if [ -n "$MANIFEST" ]; then
    MANIFEST="## Files in Context\nThese files were recently read. Do not re-read unless editing:\n${MANIFEST}"
  fi
fi

# --- Component 3: RTK redirect (always) ---
RTK_REDIRECT="Use rtk read/grep via Bash instead of Read/Grep tools (60-90% token savings)."

# --- Combine and output ---
SYSTEM_MSG=""
if [ -n "$NUDGE" ]; then
  SYSTEM_MSG="${NUDGE}"
fi
if [ -n "$MANIFEST" ]; then
  if [ -n "$SYSTEM_MSG" ]; then
    SYSTEM_MSG="${SYSTEM_MSG}\n\n${MANIFEST}"
  else
    SYSTEM_MSG="${MANIFEST}"
  fi
fi
if [ -n "$SYSTEM_MSG" ]; then
  SYSTEM_MSG="${SYSTEM_MSG}\n\n${RTK_REDIRECT}"
else
  SYSTEM_MSG="${RTK_REDIRECT}"
fi

jq -n --arg msg "$SYSTEM_MSG" '{"systemMessage": $msg}'
exit 0

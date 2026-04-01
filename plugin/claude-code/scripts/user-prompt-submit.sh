#!/bin/bash
# Engram — UserPromptSubmit hook
#
# Injects up to 3 components into systemMessage on every turn:
# 1. Memory save nudge (conditional: >15min since last save, session >5min)
# 2. File manifest (from PostToolUse tracking, if available)
# 3. RTK redirect instruction (always)

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "${SCRIPT_DIR}/_helpers.sh"

INPUT=$(cat)
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
PROJECT=$(detect_project "$CWD")

# --- Component 1: Memory nudge (conditional) ---
NUDGE=""

if [ -n "$PROJECT" ]; then
  # Check session age — skip nudge if session < 5 minutes
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
    # Check last save time
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

# --- Component 2: File manifest (from PostToolUse tracking) ---
MANIFEST=""
if [ -n "$SESSION_ID" ]; then
  TRACK_FILE="/tmp/engram-claude-${SESSION_ID}-filereads.json"
  if [ -f "$TRACK_FILE" ]; then
    NOW_EPOCH=$(date "+%s")
    MANIFEST=$(cat "$TRACK_FILE" | jq -r --argjson now "$NOW_EPOCH" '
      [.[] | {
        file_path: .file_path,
        bytes: .bytes,
        ago: (($now - (.timestamp | sub("\\.[0-9]+Z$"; "Z") | fromdate)) |
          if . < 60 then "\(.)s ago"
          elif . < 3600 then "\(. / 60 | floor)m ago"
          else "\(. / 3600 | floor)h ago"
          end),
        kb: ((.bytes / 1024 * 10 | floor) / 10)
      }] |
      if length == 0 then ""
      else "## Files in Context\nThese files were recently read. Do not re-read unless editing:\n" +
        (map("- \(.file_path | split("/") | .[-2:] | join("/")) (\(.ago), \(.kb) KB)") | join("\n"))
      end
    ' 2>/dev/null || true)
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

#!/usr/bin/env bash
# Engram — UserPromptSubmit hook for Claude Code
#
# On the FIRST message of a session: injects a ToolSearch instruction to force
# Claude Code to load all engram memory tools (which are deferred by default).
#
# On subsequent messages: checks when the last mem_save was for the current
# project. If it's been at least 15 minutes AND the session has been active > 5
# minutes, injects a nudge reminding the agent to save.
#
# The nudge is debounced per session: once shown, it stays quiet for
# ENGRAM_NUDGE_COOLDOWN_SECS (default 900s) before it can fire again. Without
# this, an agent that genuinely has nothing to save never resets the
# last-save clock, so the reminder would fire on every single message forever.
#
# MUST exit 0 always and output valid JSON — otherwise Claude Code blocks the message.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"
ENGRAM_HOOK_MAX_TIME="${ENGRAM_HOOK_MAX_TIME:-0.2}"

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

session_state_key_part() {
  local raw="$1"
  local encoded="sid-"
  local i char byte
  local LC_ALL=C
  if [[ "$raw" =~ ^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$ ]]; then
    JSON_VALUE="$raw"
    return 0
  fi
  for (( i=0; i<${#raw}; i++ )); do
    char="${raw:i:1}"
    printf -v byte '%02X' "'$char"
    encoded+="$byte"
  done
  JSON_VALUE="$encoded"
}

# Emit the first-message ToolSearch bootstrap.
#
# Uses hookSpecificOutput.additionalContext (NOT systemMessage): on a
# UserPromptSubmit hook, only stdout/additionalContext enters the model's
# context. A systemMessage payload is rendered to the terminal as
# "UserPromptSubmit says: ..." (issue #145) and never reaches the model, so the
# bootstrap silently no-ops. session-start.sh already uses additionalContext.
#
# The select list carries BOTH tool-name prefixes: mcp__plugin_engram_engram__*
# for plugin/marketplace installs and mcp__engram__* for direct MCP-server
# installs. ToolSearch select: returns whichever names exist and silently
# ignores the rest, so one list covers both install modes (issue #534).
print_toolsearch_message() {
  printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"CRITICAL FIRST ACTION - Execute this ToolSearch NOW before responding to the user:\nselect:mcp__plugin_engram_engram__mem_save,mcp__plugin_engram_engram__mem_search,mcp__plugin_engram_engram__mem_context,mcp__plugin_engram_engram__mem_session_summary,mcp__plugin_engram_engram__mem_session_start,mcp__plugin_engram_engram__mem_session_end,mcp__plugin_engram_engram__mem_get_observation,mcp__plugin_engram_engram__mem_suggest_topic_key,mcp__plugin_engram_engram__mem_capture_passive,mcp__plugin_engram_engram__mem_save_prompt,mcp__plugin_engram_engram__mem_update,mcp__plugin_engram_engram__mem_current_project,mcp__plugin_engram_engram__mem_judge,mcp__engram__mem_save,mcp__engram__mem_search,mcp__engram__mem_context,mcp__engram__mem_session_summary,mcp__engram__mem_session_start,mcp__engram__mem_session_end,mcp__engram__mem_get_observation,mcp__engram__mem_suggest_topic_key,mcp__engram__mem_capture_passive,mcp__engram__mem_save_prompt,mcp__engram__mem_update,mcp__engram__mem_current_project,mcp__engram__mem_judge\n\nAfter loading tools, call mem_context to check for prior session history before responding."}}'
}

json_string_value_without_jq() {
  local key="$1"
  local json="$2"
  local pattern='"'"$key"'"[[:space:]]*:[[:space:]]*"(([^"\\]|\\.)*)"'
  local raw i char escaped hex codepoint low_hex low_surrogate value=""
  local LC_ALL=C
  JSON_VALUE=""
  [[ "$json" =~ $pattern ]] || return 1
  raw="${BASH_REMATCH[1]}"
  for (( i=0; i<${#raw}; i++ )); do
    char="${raw:i:1}"
    if [ "$char" != $'\\' ]; then
      value+="$char"
      continue
    fi
    (( i++ ))
    [ "$i" -lt "${#raw}" ] || return 1
    escaped="${raw:i:1}"
    case "$escaped" in
      '"'|$'\\'|/) value+="$escaped" ;;
      b) value+=$'\b' ;;
      f) value+=$'\f' ;;
      n) value+=$'\n' ;;
      r) value+=$'\r' ;;
      t) value+=$'\t' ;;
      u)
        hex="${raw:i+1:4}"
        [[ "$hex" =~ ^[0-9A-Fa-f]{4}$ ]] || return 1
        codepoint=$(( 16#$hex ))
        i=$(( i + 4 ))
        if [ "$codepoint" -ge 55296 ] && [ "$codepoint" -le 56319 ]; then
          [ "${raw:i+1:2}" = $'\\u' ] || return 1
          low_hex="${raw:i+3:4}"
          [[ "$low_hex" =~ ^[0-9A-Fa-f]{4}$ ]] || return 1
          low_surrogate=$(( 16#$low_hex ))
          [ "$low_surrogate" -ge 56320 ] && [ "$low_surrogate" -le 57343 ] || return 1
          codepoint=$(( 65536 + (codepoint - 55296) * 1024 + low_surrogate - 56320 ))
          i=$(( i + 6 ))
        elif [ "$codepoint" -ge 56320 ] && [ "$codepoint" -le 57343 ]; then
          return 1
        fi
        utf8_from_codepoint_without_jq "$codepoint" || return 1
        value+="$JSON_VALUE"
        ;;
      *) return 1 ;;
    esac
  done
  JSON_VALUE="$value"
}

# Validate a complete JSON value using bash builtins. The no-jq hook cannot
# depend on another runtime, but a 200 response is useful only when it is a
# valid observations array.
json_skip_whitespace_without_jq() {
  while [ "$JSON_PARSE_INDEX" -lt "$JSON_PARSE_LENGTH" ]; do
    case "${JSON_PARSE_INPUT:JSON_PARSE_INDEX:1}" in
      ' '|$'\t'|$'\r'|$'\n') JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 )) ;;
      *) return 0 ;;
    esac
  done
}

json_parse_string_without_jq() {
  [ "${JSON_PARSE_INPUT:JSON_PARSE_INDEX:1}" = '"' ] || return 1
  JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 ))
  while [ "$JSON_PARSE_INDEX" -lt "$JSON_PARSE_LENGTH" ]; do
    local char="${JSON_PARSE_INPUT:JSON_PARSE_INDEX:1}"
    case "$char" in
      '"') JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 )); return 0 ;;
      $'\\')
        JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 ))
        [ "$JSON_PARSE_INDEX" -lt "$JSON_PARSE_LENGTH" ] || return 1
        char="${JSON_PARSE_INPUT:JSON_PARSE_INDEX:1}"
        case "$char" in
          '"'|$'\\'|/|b|f|n|r|t) JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 )) ;;
          u)
            [[ "${JSON_PARSE_INPUT:JSON_PARSE_INDEX+1:4}" =~ ^[0-9A-Fa-f]{4}$ ]] || return 1
            JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 5 ))
            ;;
          *) return 1 ;;
        esac
        ;;
      *)
        [ "$(printf '%d' "'$char")" -ge 32 ] || return 1
        JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 ))
        ;;
    esac
  done
  return 1
}

json_parse_value_without_jq() {
  local char tail
  json_skip_whitespace_without_jq
  [ "$JSON_PARSE_INDEX" -lt "$JSON_PARSE_LENGTH" ] || return 1
  char="${JSON_PARSE_INPUT:JSON_PARSE_INDEX:1}"
  case "$char" in
    '"') json_parse_string_without_jq ;;
    t) [ "${JSON_PARSE_INPUT:JSON_PARSE_INDEX:4}" = "true" ] && JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 4 )) ;;
    f) [ "${JSON_PARSE_INPUT:JSON_PARSE_INDEX:5}" = "false" ] && JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 5 )) ;;
    n) [ "${JSON_PARSE_INPUT:JSON_PARSE_INDEX:4}" = "null" ] && JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 4 )) ;;
    [0-9-])
      tail="${JSON_PARSE_INPUT:JSON_PARSE_INDEX}"
      [[ "$tail" =~ ^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)? ]] || return 1
      JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + ${#BASH_REMATCH[0]} ))
      ;;
    '[')
      JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 ))
      json_skip_whitespace_without_jq
      if [ "${JSON_PARSE_INPUT:JSON_PARSE_INDEX:1}" = ']' ]; then
        JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 ))
        return 0
      fi
      while true; do
        json_parse_value_without_jq || return 1
        json_skip_whitespace_without_jq
        case "${JSON_PARSE_INPUT:JSON_PARSE_INDEX:1}" in
          ',') JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 )) ;;
          ']') JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 )); return 0 ;;
          *) return 1 ;;
        esac
      done
      ;;
    '{')
      JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 ))
      json_skip_whitespace_without_jq
      if [ "${JSON_PARSE_INPUT:JSON_PARSE_INDEX:1}" = '}' ]; then
        JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 ))
        return 0
      fi
      while true; do
        json_skip_whitespace_without_jq
        json_parse_string_without_jq || return 1
        json_skip_whitespace_without_jq
        [ "${JSON_PARSE_INPUT:JSON_PARSE_INDEX:1}" = ':' ] || return 1
        JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 ))
        json_parse_value_without_jq || return 1
        json_skip_whitespace_without_jq
        case "${JSON_PARSE_INPUT:JSON_PARSE_INDEX:1}" in
          ',') JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 )) ;;
          '}') JSON_PARSE_INDEX=$(( JSON_PARSE_INDEX + 1 )); return 0 ;;
          *) return 1 ;;
        esac
      done
      ;;
    *) return 1 ;;
  esac
}

json_is_valid_array_without_jq() {
  JSON_PARSE_INPUT="$1"
  JSON_PARSE_INDEX=0
  JSON_PARSE_LENGTH=${#JSON_PARSE_INPUT}
  json_skip_whitespace_without_jq
  [ "${JSON_PARSE_INPUT:JSON_PARSE_INDEX:1}" = '[' ] || return 1
  json_parse_value_without_jq || return 1
  json_skip_whitespace_without_jq
  [ "$JSON_PARSE_INDEX" -eq "$JSON_PARSE_LENGTH" ]
}

utf8_from_codepoint_without_jq() {
  local codepoint="$1"
  local byte1 byte2 byte3 byte4
  [ "$codepoint" -gt 0 ] && [ "$codepoint" -le 1114111 ] || return 1
  if [ "$codepoint" -le 127 ]; then
    printf -v byte1 '%03o' "$codepoint"
    printf -v JSON_VALUE '%b' "\\0${byte1}"
  elif [ "$codepoint" -le 2047 ]; then
    printf -v byte1 '%03o' "$(( 192 | (codepoint >> 6) ))"
    printf -v byte2 '%03o' "$(( 128 | (codepoint & 63) ))"
    printf -v JSON_VALUE '%b' "\\0${byte1}\\0${byte2}"
  elif [ "$codepoint" -le 65535 ]; then
    printf -v byte1 '%03o' "$(( 224 | (codepoint >> 12) ))"
    printf -v byte2 '%03o' "$(( 128 | ((codepoint >> 6) & 63) ))"
    printf -v byte3 '%03o' "$(( 128 | (codepoint & 63) ))"
    printf -v JSON_VALUE '%b' "\\0${byte1}\\0${byte2}\\0${byte3}"
  else
    printf -v byte1 '%03o' "$(( 240 | (codepoint >> 18) ))"
    printf -v byte2 '%03o' "$(( 128 | ((codepoint >> 12) & 63) ))"
    printf -v byte3 '%03o' "$(( 128 | ((codepoint >> 6) & 63) ))"
    printf -v byte4 '%03o' "$(( 128 | (codepoint & 63) ))"
    printf -v JSON_VALUE '%b' "\\0${byte1}\\0${byte2}\\0${byte3}\\0${byte4}"
  fi
}

json_escape_without_jq() {
  local raw="$1"
  local i char byte byte2 byte3 byte4 codepoint escaped value=""
  local LC_ALL=C
  for (( i=0; i<${#raw}; i++ )); do
    char="${raw:i:1}"
    case "$char" in
      '"') value+='\"' ;;
      $'\\') value+=$'\\\\' ;;
      $'\b') value+='\b' ;;
      $'\f') value+='\f' ;;
      $'\n') value+='\n' ;;
      $'\r') value+='\r' ;;
      $'\t') value+='\t' ;;
      *)
        printf -v byte '%d' "'$char"
        if [ "$byte" -ge 1 ] && [ "$byte" -le 31 ]; then
          printf -v escaped '\\u%04x' "$byte"
          value+="$escaped"
        elif [ "$byte" -ge 128 ]; then
          codepoint=""
          if [ "$byte" -ge 194 ] && [ "$byte" -le 223 ] && [ "$(( i + 1 ))" -lt "${#raw}" ]; then
            printf -v byte2 '%d' "'${raw:i+1:1}"
            if [ "$byte2" -ge 128 ] && [ "$byte2" -le 191 ]; then
              codepoint=$(( ((byte & 31) << 6) | (byte2 & 63) ))
              i=$(( i + 1 ))
            fi
          elif [ "$byte" -ge 224 ] && [ "$byte" -le 239 ] && [ "$(( i + 2 ))" -lt "${#raw}" ]; then
            printf -v byte2 '%d' "'${raw:i+1:1}"
            printf -v byte3 '%d' "'${raw:i+2:1}"
            if [ "$byte2" -ge 128 ] && [ "$byte2" -le 191 ] && [ "$byte3" -ge 128 ] && [ "$byte3" -le 191 ]; then
              codepoint=$(( ((byte & 15) << 12) | ((byte2 & 63) << 6) | (byte3 & 63) ))
              if [ "$codepoint" -ge 2048 ] && { [ "$codepoint" -lt 55296 ] || [ "$codepoint" -gt 57343 ]; }; then
                i=$(( i + 2 ))
              else
                codepoint=""
              fi
            fi
          elif [ "$byte" -ge 240 ] && [ "$byte" -le 244 ] && [ "$(( i + 3 ))" -lt "${#raw}" ]; then
            printf -v byte2 '%d' "'${raw:i+1:1}"
            printf -v byte3 '%d' "'${raw:i+2:1}"
            printf -v byte4 '%d' "'${raw:i+3:1}"
            if [ "$byte2" -ge 128 ] && [ "$byte2" -le 191 ] && [ "$byte3" -ge 128 ] && [ "$byte3" -le 191 ] && [ "$byte4" -ge 128 ] && [ "$byte4" -le 191 ]; then
              codepoint=$(( ((byte & 7) << 18) | ((byte2 & 63) << 12) | ((byte3 & 63) << 6) | (byte4 & 63) ))
              if [ "$codepoint" -ge 65536 ] && [ "$codepoint" -le 1114111 ]; then
                i=$(( i + 3 ))
              else
                codepoint=""
              fi
            fi
          fi
          if [ -n "$codepoint" ]; then
            if [ "$codepoint" -le 65535 ]; then
              printf -v escaped '\\u%04x' "$codepoint"
            else
              printf -v escaped '\\u%04x\\u%04x' "$(( 55296 + ((codepoint - 65536) >> 10) ))" "$(( 56320 + ((codepoint - 65536) & 1023) ))"
            fi
            value+="$escaped"
          else
            printf -v escaped '\\u00%02x' "$byte"
            value+="$escaped"
          fi
        else
          value+="$char"
        fi
        ;;
    esac
  done
  JSON_VALUE="$value"
}

url_encode_without_jq() {
  local raw="$1"
  local i char byte value=""
  local LC_ALL=C
  for (( i=0; i<${#raw}; i++ )); do
    char="${raw:i:1}"
    case "$char" in
      [a-zA-Z0-9.~_-]) value+="$char" ;;
      *)
        printf -v byte '%02X' "'$char"
        value+="%${byte}"
        ;;
    esac
  done
  JSON_VALUE="$value"
}

resolve_project_without_jq() {
  local dir="$1"
  local encoded response project source
  [ -n "$dir" ] || return 1
  url_encode_without_jq "$dir"
  encoded="$JSON_VALUE"
  response=$(curl -sf "${ENGRAM_URL}/project/current?cwd=${encoded}" --max-time 2 2>/dev/null) || return 1
  json_string_value_without_jq "project" "$response" || return 1
  project="$JSON_VALUE"
  json_string_value_without_jq "project_source" "$response" || return 1
  source="$JSON_VALUE"
  json_string_value_without_jq "error_hint" "$response" && return 1
  case "$source" in
    config|git_remote|git_root|git_child|dir_basename|process_override) ;;
    *) return 1 ;;
  esac
  [ -n "$project" ] || return 1
  printf '%s\n' "$project"
}

user_prompt_submit_without_jq() {
  local cwd="" session_id="" prompt="" project="" session_key state_dir state_file
  local session_start="" session_start_epoch now_epoch session_age_secs encoded_project
  local last_save_json="" last_save_at="" last_epoch elapsed nudge_cooldown nudge_state_file last_nudge_epoch=""

  json_string_value_without_jq "cwd" "$INPUT" && cwd="$JSON_VALUE"
  json_string_value_without_jq "session_id" "$INPUT" && session_id="$JSON_VALUE"
  json_string_value_without_jq "prompt" "$INPUT" && prompt="$JSON_VALUE"

  if [ -n "$prompt" ] && [ -n "$session_id" ]; then
    (
      project=$(resolve_project_without_jq "$cwd") || exit 0
      json_escape_without_jq "$session_id"
      local escaped_session="$JSON_VALUE"
      json_escape_without_jq "$project"
      local escaped_project="$JSON_VALUE"
      json_escape_without_jq "$prompt"
      curl -sf -X POST "${ENGRAM_URL}/prompts" --max-time 2 \
        -H 'Content-Type: application/json' \
        -d "{\"session_id\":\"${escaped_session}\",\"project\":\"${escaped_project}\",\"content\":\"${JSON_VALUE}\"}" >/dev/null 2>&1 || true
    ) &
  fi

  if [ -n "$session_id" ]; then
    session_state_key_part "$session_id"
    session_key="engram-claude-${JSON_VALUE}-tools-loaded"
  else
    session_key="engram-claude-unknown-$$-tools-loaded"
  fi
  state_dir="${TMPDIR:-/tmp}"
  state_file="${state_dir}/${session_key}"

  if [ ! -f "$state_file" ]; then
    : > "$state_file" 2>/dev/null || true
    print_toolsearch_message
    return 0
  fi

  project=$(resolve_project_without_jq "$cwd") || {
    printf '%s\n' '{}'
    return 0
  }

  if [ -n "$session_id" ]; then
    session_start=$(curl -sf "${ENGRAM_URL}/sessions/${session_id}" --max-time "$ENGRAM_HOOK_MAX_TIME" 2>/dev/null)
    json_string_value_without_jq "started_at" "$session_start" && session_start="$JSON_VALUE" || session_start=""
  fi
  if [ -n "$session_start" ]; then
    session_start_epoch=$(parse_epoch "$session_start")
    [ -n "$session_start_epoch" ] || { printf '%s\n' '{}'; return 0; }
    now_epoch=$(date "+%s")
    session_age_secs=$(( now_epoch - session_start_epoch ))
    [ "$session_age_secs" -ge 300 ] || { printf '%s\n' '{}'; return 0; }
  fi

  url_encode_without_jq "$project"
  encoded_project="$JSON_VALUE"
  last_save_json=$(curl -sf "${ENGRAM_URL}/observations?project=${encoded_project}&limit=1&sort=created_at:desc" --max-time "$ENGRAM_HOOK_MAX_TIME" 2>/dev/null) || {
    printf '%s\n' '{}'
    return 0
  }
  json_is_valid_array_without_jq "$last_save_json" || {
    printf '%s\n' '{}'
    return 0
  }
  if [[ "$last_save_json" =~ ^[[:space:]]*\[[[:space:]]*\][[:space:]]*$ ]]; then
    [ -n "$session_age_secs" ] || { printf '%s\n' '{}'; return 0; }
    now_epoch=$(date "+%s")
    elapsed="$session_age_secs"
  else
    json_string_value_without_jq "created_at" "$last_save_json" && last_save_at="$JSON_VALUE" || last_save_at=""
    [ -n "$last_save_at" ] || { printf '%s\n' '{}'; return 0; }
    last_epoch=$(parse_epoch "$last_save_at")
    [ -n "$last_epoch" ] || { printf '%s\n' '{}'; return 0; }
    now_epoch=$(date "+%s")
    elapsed=$(( now_epoch - last_epoch ))
  fi

  if [ "$elapsed" -ge 900 ]; then
    nudge_cooldown="${ENGRAM_NUDGE_COOLDOWN_SECS:-900}"
    nudge_state_file="${state_file%-tools-loaded}-last-nudge"
    if [ -f "$nudge_state_file" ]; then
      read -r last_nudge_epoch < "$nudge_state_file" 2>/dev/null || last_nudge_epoch=""
    fi
    case "$last_nudge_epoch" in
      ''|*[!0-9]*) last_nudge_epoch="" ;;
    esac
    if [ -z "$last_nudge_epoch" ] || [ "$(( now_epoch - last_nudge_epoch ))" -ge "$nudge_cooldown" ]; then
      printf '%s\n' "$now_epoch" > "$nudge_state_file" 2>/dev/null || true
      printf '%s\n' '{"systemMessage":"MEMORY REMINDER: It'\''s been at least 15 minutes since your last save. If you'\''ve made decisions, discoveries, or completed significant work, call mem_save now."}'
      return 0
    fi
  fi
  printf '%s\n' '{}'
}

if is_windows_bash && [ "${ENGRAM_CLAUDE_WINDOWS_BASH_SAFE_MODE:-auto}" != "0" ]; then
  INPUT=""
  while IFS= read -r LINE || [ -n "$LINE" ]; do
    INPUT+="${LINE}"$'\n'
  done

  set_json_string_value "session_id" "$INPUT"
  SESSION_ID="$JSON_VALUE"
  if [ -n "$SESSION_ID" ]; then
    session_state_key_part "$SESSION_ID"
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

  # No timezone marker at all — these are naive timestamps from sqlite's
  # datetime('now'), which is always UTC. Must parse with -u; parsing as
  # local time skews the result by the host's UTC offset (e.g. on a UTC-6
  # host, session age comes out ~6h in the future, so it's always seen as
  # "just started" and the save-nudge can never fire).
  date -j -u -f "%Y-%m-%dT%H:%M:%S" "$TS" "+%s" 2>/dev/null \
    || date -j -u -f "%Y-%m-%d %H:%M:%S" "$TS" "+%s" 2>/dev/null \
    || date -u -d "$TS" "+%s" 2>/dev/null
}

# Read hook input from stdin
INPUT=$(cat)
if ! command -v jq >/dev/null 2>&1; then
  user_prompt_submit_without_jq
  exit 0
fi
CWD=$(echo "$INPUT" | jq -r '.cwd // empty')
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // empty')
PROJECT=""

# ──────────────────────────────────────────────────────────────────────────────
# PROMPT PERSIST
#
# Every user message is captured to POST /prompts so mem_save can attach the
# originating prompt via SessionActivity. The canonical project is resolved by
# the server before this script writes. Fire-and-forget: never blocks and never
# fails the hook.
# ──────────────────────────────────────────────────────────────────────────────
PROMPT=$(echo "$INPUT" | jq -r '.prompt // empty')
if [ -n "$PROMPT" ] && [ -n "$SESSION_ID" ]; then
  # Detached subshell so the POST never stalls the hook. The server derives the
  # prompt's project from the session and rejects any mismatch.
  (
    PROJECT=$(resolve_project "$CWD") || exit 0
    curl -sf -X POST "${ENGRAM_URL}/prompts" --max-time 2 \
      -H 'Content-Type: application/json' \
      -d "$(jq -n --arg s "$SESSION_ID" --arg p "$PROJECT" --arg c "$PROMPT" \
            '{session_id:$s, project:$p, content:$c}')" >/dev/null 2>&1 || true
  ) &
fi

# Default: no injection
OUTPUT="{}"

# ──────────────────────────────────────────────────────────────────────────────
# FIRST-MESSAGE DETECTION
#
# Use a state file per session to determine if this is the first user message.
# State file lives in TMPDIR (or /tmp) and is keyed by session_id (falls back to project+pid).
# ──────────────────────────────────────────────────────────────────────────────

# Build a stable session key — prefer SESSION_ID, then a process-local fallback.
if [ -n "$SESSION_ID" ]; then
  session_state_key_part "$SESSION_ID"
  SESSION_KEY="engram-claude-${JSON_VALUE}-tools-loaded"
else
  SESSION_KEY="engram-claude-unknown-$$-tools-loaded"
fi

STATE_FILE="${TMPDIR:-/tmp}/${SESSION_KEY}"

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

# Resolve the project only after the first-message path has had a chance to return.
if [ -z "${PROJECT:-}" ]; then
  PROJECT=$(resolve_project "$CWD") || PROJECT=""
fi

# Bail early if we can't determine the project
if [ -z "$PROJECT" ]; then
  echo "$OUTPUT"
  exit 0
fi

# Get session start time to check if session is > 5 minutes old
SESSION_START=""
if [ -n "$SESSION_ID" ]; then
  SESSION_START=$(curl -sf "${ENGRAM_URL}/sessions/${SESSION_ID}" --max-time "$ENGRAM_HOOK_MAX_TIME" 2>/dev/null \
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
  --max-time "$ENGRAM_HOOK_MAX_TIME" 2>/dev/null)

if [ -z "$LAST_SAVE_JSON" ]; then
  # Server not responding or slow — fail silently, no nudge
  echo "$OUTPUT"
  exit 0
fi

if ! echo "$LAST_SAVE_JSON" | jq -e 'type == "array"' >/dev/null 2>&1; then
  # A successful HTTP response still must be a valid observations array.
  echo "$OUTPUT"
  exit 0
fi

NOW_EPOCH=$(date "+%s")

if [ "$(echo "$LAST_SAVE_JSON" | jq 'length')" -eq 0 ]; then
  # No observations exist yet. Use the real session age so the first-save
  # reminder observes the same 15-minute threshold as existing saves.
  if [ -z "${SESSION_AGE_SECS:-}" ]; then
    echo "$OUTPUT"
    exit 0
  fi
  ELAPSED="$SESSION_AGE_SECS"
else
  LAST_SAVE_AT=$(echo "$LAST_SAVE_JSON" | jq -r '.[0].created_at | if type == "string" then . else empty end')
  if [ -z "$LAST_SAVE_AT" ]; then
    # A non-empty response without a timestamp is incomplete, not a first save.
    echo "$OUTPUT"
    exit 0
  fi
  # Parse last save timestamp and compare to now
  LAST_EPOCH=$(parse_epoch "$LAST_SAVE_AT")
  if [ -z "$LAST_EPOCH" ]; then
    echo "$OUTPUT"
    exit 0
  fi
  ELAPSED=$(( NOW_EPOCH - LAST_EPOCH ))
fi

# Nudge if the last save was at least 15 minutes ago (900 seconds), but debounce so we do
# not repeat the reminder on every message while the agent has nothing to save.
if [ "$ELAPSED" -ge 900 ]; then
  NUDGE_COOLDOWN="${ENGRAM_NUDGE_COOLDOWN_SECS:-900}"
  NUDGE_STATE_FILE="${STATE_FILE%-tools-loaded}-last-nudge"

  LAST_NUDGE_EPOCH=""
  if [ -f "$NUDGE_STATE_FILE" ]; then
    read -r LAST_NUDGE_EPOCH < "$NUDGE_STATE_FILE" 2>/dev/null || LAST_NUDGE_EPOCH=""
  fi
  # Ignore a corrupt/non-numeric state file — treat as "never nudged".
  case "$LAST_NUDGE_EPOCH" in
    ''|*[!0-9]*) LAST_NUDGE_EPOCH="" ;;
  esac

  if [ -z "$LAST_NUDGE_EPOCH" ] || [ "$(( NOW_EPOCH - LAST_NUDGE_EPOCH ))" -ge "$NUDGE_COOLDOWN" ]; then
    printf '%s\n' "$NOW_EPOCH" > "$NUDGE_STATE_FILE" 2>/dev/null || true
    # additionalContext (not systemMessage) so the nudge reaches the model — see
    # print_toolsearch_message above.
    OUTPUT=$(jq -n \
      '{hookSpecificOutput: {hookEventName: "UserPromptSubmit", additionalContext: "MEMORY REMINDER: It'\''s been over 15 minutes since your last save. If you'\''ve made decisions, discoveries, or completed significant work, call mem_save now."}}')
  fi
fi

echo "$OUTPUT"
exit 0

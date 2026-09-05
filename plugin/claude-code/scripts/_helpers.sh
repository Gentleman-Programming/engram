#!/usr/bin/env bash
# Engram — Shared helpers for Claude Code hooks
# WARNING: Do not read from stdin here — scripts source this before reading their hook input.

trim_whitespace() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

is_valid_port() {
  local port="$1"
  [[ "$port" =~ ^[0-9]+$ ]] || return 1
  port="${port#"${port%%[!0]*}"}"
  [[ "$port" =~ ^([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])$ ]]
}

is_valid_hook_max_time() {
  local value="$1" integer fraction prefix
  local LC_ALL=C
  [[ "$value" =~ ^([0-9]+(\.[0-9]*)?|\.[0-9]+)$ ]] || return 1
  [[ "$value" == *[1-9]* ]] || return 1

  if [[ "$value" == .* ]]; then
    integer=0
    fraction="${value#.}"
  elif [[ "$value" == *.* ]]; then
    integer="${value%%.*}"
    fraction="${value#*.}"
  else
    integer="$value"
    fraction=""
  fi
  integer="${integer#"${integer%%[!0]*}"}"
  [ -n "$integer" ] || integer=0

  if [ "${#integer}" -gt 7 ] || { [ "${#integer}" -eq 7 ] && [ "$integer" -gt 2147483 ]; }; then
    return 1
  fi
  [ "$integer" = 2147483 ] || return 0

  fraction="${fraction%"${fraction##*[!0]}"}"
  while [ "${#fraction}" -lt 3 ]; do
    fraction+="0"
  done
  prefix="${fraction:0:3}"
  [ "$prefix" -gt 647 ] && return 1
  [[ "$prefix" = 647 && "${fraction:3}" == *[1-9]* ]] && return 1
  return 0
}

case "${1:-}" in
  __engram_hook_default_max_time=*) ENGRAM_HOOK_CALLER_DEFAULT_MAX_TIME="${1#*=}" ;;
  *) ENGRAM_HOOK_CALLER_DEFAULT_MAX_TIME=3 ;;
esac
if ! is_valid_hook_max_time "$ENGRAM_HOOK_CALLER_DEFAULT_MAX_TIME"; then
  ENGRAM_HOOK_CALLER_DEFAULT_MAX_TIME=3
fi
if ! is_valid_hook_max_time "${ENGRAM_HOOK_MAX_TIME:-}"; then
  ENGRAM_HOOK_MAX_TIME="$ENGRAM_HOOK_CALLER_DEFAULT_MAX_TIME"
fi
unset ENGRAM_HOOK_CALLER_DEFAULT_MAX_TIME

ENGRAM_PORT="$(trim_whitespace "${ENGRAM_PORT:-}")"
if is_valid_port "$ENGRAM_PORT"; then
  ENGRAM_PORT="${ENGRAM_PORT#"${ENGRAM_PORT%%[!0]*}"}"
else
  ENGRAM_PORT=7437
fi
ENGRAM_SOCKET="$(trim_whitespace "${ENGRAM_SOCKET:-}")"
if [ -n "$ENGRAM_SOCKET" ]; then
  ENGRAM_URL="http://localhost"
  ENGRAM_CURL_TRANSPORT=(--unix-socket "$ENGRAM_SOCKET")
else
  ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"
  ENGRAM_CURL_TRANSPORT=()
fi

engram_curl() {
  curl --max-time "${ENGRAM_HOOK_MAX_TIME:-3}" "${ENGRAM_CURL_TRANSPORT[@]}" "$@"
  local status=$?
  if [ "$status" -ne 0 ] && [ -n "$ENGRAM_SOCKET" ]; then
    printf '%s\n' "warning: Engram Unix-socket request failed; check that the server is running and ENGRAM_SOCKET is configured." >&2
  fi
  return "$status"
}

# Resolve the project through the server, which owns project policy.
# An unavailable, malformed, empty, or ambiguous response is not a project.
resolve_project() {
  local dir="$1"
  [ -n "$dir" ] || return 1

  local encoded_cwd response
  encoded_cwd=$(printf '%s' "$dir" | jq -sRr @uri) || return 1
  response=$(engram_curl -sf "${ENGRAM_URL}/project/current?cwd=${encoded_cwd}" --max-time 2 2>/dev/null) || {
    [ -n "$ENGRAM_SOCKET" ] && printf '%s\n' "warning: Engram could not resolve the project over the Unix socket; check that the server is running and ENGRAM_SOCKET is configured." >&2
    return 1
  }
  printf '%s' "$response" | jq -er '
    if (.project | type) == "string"
      and (.project | gsub("^[[:space:]]+|[[:space:]]+$"; "") | length) > 0
      and (.project_source | type) == "string"
      and (.project_source as $source | ["config", "git_remote", "git_root", "git_child", "dir_basename", "process_override"] | index($source) != null)
      and (has("error_hint") | not)
    then .project
    else error("canonical project resolution failed")
    end
  ' 2>/dev/null || {
    [ -n "$ENGRAM_SOCKET" ] && printf '%s\n' "warning: Engram could not resolve the project over the Unix socket; check that the server is running and ENGRAM_SOCKET is configured." >&2
    return 1
  }
}

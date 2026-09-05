#!/usr/bin/env bash
# Engram — Shared helpers for Claude Code hooks
# WARNING: Do not read from stdin here — scripts source this before reading their hook input.

trim_whitespace() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

ENGRAM_PORT="$(trim_whitespace "${ENGRAM_PORT:-7437}")"
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

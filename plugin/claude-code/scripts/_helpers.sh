#!/usr/bin/env bash
# Engram — Shared helpers for Claude Code hooks
# WARNING: Do not read from stdin here — scripts source this before reading their hook input.

ENGRAM_PORT="${ENGRAM_PORT:-7437}"
ENGRAM_SOCKET="${ENGRAM_SOCKET:-}"
if [ -n "$ENGRAM_SOCKET" ]; then
  ENGRAM_URL="http://localhost"
  ENGRAM_CURL_TRANSPORT=(--unix-socket "$ENGRAM_SOCKET")
else
  ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"
  ENGRAM_CURL_TRANSPORT=()
fi

engram_curl() {
  curl "${ENGRAM_CURL_TRANSPORT[@]}" "$@"
}

# Resolve the project through the server, which owns project policy.
# An unavailable, malformed, empty, or ambiguous response is not a project.
resolve_project() {
  local dir="$1"
  [ -n "$dir" ] || return 1

  local encoded_cwd response
  encoded_cwd=$(printf '%s' "$dir" | jq -sRr @uri) || return 1
  response=$(engram_curl -sf "${ENGRAM_URL}/project/current?cwd=${encoded_cwd}" --max-time 2 2>/dev/null) || return 1
  printf '%s' "$response" | jq -er '
    if (.project | type) == "string"
      and (.project | gsub("^[[:space:]]+|[[:space:]]+$"; "") | length) > 0
      and (.project_source | type) == "string"
      and (.project_source as $source | ["config", "git_remote", "git_root", "git_child", "dir_basename", "process_override"] | index($source) != null)
      and (has("error_hint") | not)
    then .project
    else error("canonical project resolution failed")
    end
  ' 2>/dev/null
}

#!/usr/bin/env bash
# Engram — Shared helpers for Claude Code hooks
# WARNING: Do not read from stdin here — scripts source this before reading their hook input.
# The only exception is the Pi guard below, which drains stdin because the hook exits right after.

# Pi owns its Engram session natively (gentle-engram). When pi-claude-bridge runs
# Claude Code inside a Pi turn (PI_CODING_AGENT is non-empty), every hook is a silent no-op.
# The hook input is drained (bash builtins only) because the hook exits right here.
if [ -n "${PI_CODING_AGENT:-}" ]; then
  [ -t 0 ] || while IFS= read -r _engram_pi_line; do :; done 2>/dev/null
  exit 0
fi

trim_whitespace() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

# Resolves Claude Code's config dir: honors CLAUDE_CONFIG_DIR (relative paths resolved against $PWD), otherwise ~/.claude.
claude_config_root() {
  local dir
  dir="$(trim_whitespace "${CLAUDE_CONFIG_DIR:-}")"
  if [ -z "$dir" ]; then
    printf '%s' "$HOME/.claude"
    return
  fi
  case "$dir" in
    /*|[A-Za-z]:/*|[A-Za-z]:\\*|\\\\*) printf '%s' "$dir" ;;
    *) printf '%s' "$PWD/$dir" ;;
  esac
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
ENGRAM_EXTERNAL_URL="$(trim_whitespace "${ENGRAM_URL:-}")"
if [ -n "$ENGRAM_EXTERNAL_URL" ]; then
  ENGRAM_URL="$ENGRAM_EXTERNAL_URL"
  ENGRAM_CURL_TRANSPORT=()
  ENGRAM_MANAGED_LOCAL=0
elif [ -n "$ENGRAM_SOCKET" ]; then
	ENGRAM_URL="http://localhost"
	ENGRAM_CURL_TRANSPORT=(--unix-socket "$ENGRAM_SOCKET")
	ENGRAM_MANAGED_LOCAL=1
else
	ENGRAM_URL="http://127.0.0.1:${ENGRAM_PORT}"
	ENGRAM_CURL_TRANSPORT=()
	ENGRAM_MANAGED_LOCAL=1
fi

engram_curl() {
  curl --max-time "${ENGRAM_HOOK_MAX_TIME:-3}" "${ENGRAM_CURL_TRANSPORT[@]}" "$@"
  local status=$?
  if [ "$status" -ne 0 ] && [ -n "$ENGRAM_SOCKET" ]; then
    printf '%s\n' "warning: Engram Unix-socket request failed; check that the server is running and ENGRAM_SOCKET is configured." >&2
  fi
  return "$status"
}

# Lifecycle hooks register through the server; it owns continuation allocation.
# Return the authoritative project for the hook's subsequent context request.
engram_register_claude_session() {
  local id="$1" project="$2" dir="$3"
  local mode="project_owned" encoded_id session="" persisted_project=""
  local response code lookup_status attempt
  if [ -z "$id" ]; then
    printf '%s\n' "$project"
    return 0
  fi
  if [ -z "$project" ]; then
    printf '%s\n' "warning: Engram session registration failed: project could not be resolved; memory writes may be denied." >&2
    printf '\n'
    return 0
  fi
  encoded_id=$(printf '%s' "$id" | jq -sRr @uri 2>/dev/null)
  if [ -n "$encoded_id" ]; then
    # Two read-only attempts share the previous one-second lookup allowance.
    # Only a received 404 establishes absence; failures never authorize a POST.
    for attempt in 1 2; do
      response=$(engram_curl -s "${ENGRAM_URL}/sessions/${encoded_id}" --max-time 0.5 -w '\n%{http_code}' 2>/dev/null)
      lookup_status=$?
      code=${response##*$'\n'}
      if [ "$lookup_status" -eq 0 ] && [ "$code" = 200 ]; then
        session=${response%$'\n'*}
        break
      fi
      if [ "$lookup_status" -eq 0 ] && [ "$code" = 404 ]; then
        break
      fi
      if [ "$attempt" = 1 ]; then
        case "$code" in
          408|429|5??) continue ;;
        esac
        [ "$lookup_status" -eq 0 ] || continue
      fi
      printf '%s\n' "warning: Engram session registration failed: session lookup failed; no registration was attempted. Check the Engram server and retry SessionStart." >&2
      printf '%s\n' "$project"
      return 0
    done
  fi
  # Project resolution may change while Claude keeps the same host ID. Use
  # only an exact root response; never move its owner to match today's cwd.
  if [ -n "$session" ]; then
    persisted_project=$(printf '%s' "$session" | jq -ej --arg id "$id" '
      select(.id == $id) | .project |
      select(type == "string" and (gsub("^[[:space:]]+|[[:space:]]+$"; "") | length) > 0)
    ' 2>/dev/null) || persisted_project=""
    [ -z "$persisted_project" ] || project="$persisted_project"
    if printf '%s' "$session" | jq -e --arg id "$id" '.id == $id and .ended_at != null and .ownership_mode == "shared"' >/dev/null 2>&1; then
      mode="shared"
    fi
  fi
  if ! engram_curl -sf "${ENGRAM_URL}/sessions" \
    -X POST -H "Content-Type: application/json" \
    -d "$(jq -n --arg id "$id" --arg project "$project" --arg dir "$dir" --arg mode "$mode" \
      '{id: $id, project: $project, directory: $dir, ownership_mode: $mode, resume: true}')" >/dev/null 2>/dev/null; then
    # Never log the response body, session ID, project, cwd or credentials.
    printf '%s\n' "warning: Engram session registration failed: server rejected the request or could not be reached; memory writes may be denied. Check the Engram server and retry SessionStart." >&2
  fi
  printf '%s\n' "$project"
}

engram_health_matches_instance() {
  local expected="$1" response
  response=$(engram_curl -sf "${ENGRAM_URL}/health" --max-time 1 2>/dev/null) || return 1
  [ -n "$response" ] || return 1
  printf '%s' "$response" | jq -e --arg expected "$expected" '.instance_id? == $expected' >/dev/null 2>&1
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
  [ -n "$response" ] || return 1
  # Let Bash emit the LF: native Windows jq uses CRLF, and jq 1.6 has no -b.
  printf '%s' "$response" | jq -sej '
    if length == 1 then .[0] else error("expected one project response") end |
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
  printf '\n'
}

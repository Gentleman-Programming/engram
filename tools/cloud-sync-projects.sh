#!/usr/bin/env bash
# Scheduled explicit cloud sync wrapper — ALTERNATIVE to native autosync.
# Runs `engram sync --cloud --project <project>` once per explicitly named
# project, continuing through all; nonzero if any project or logging op fails.
# Choose ONE mode: native autosync (ENGRAM_CLOUD_AUTOSYNC=1, recommended) OR
# this wrapper — running both creates redundant overlapping sync attempts.
# Projects are never inferred from cwd or an env var. Exit: 0 ok, 1 fail, 2 usage.

PROG_NAME="cloud-sync-projects.sh"
DEFAULT_LOG_NAME="cloud-sync-projects.log"

usage() {
  cat <<'USAGE'
Usage: cloud-sync-projects.sh [--log <path>] <project> [<project> ...]

Run `engram sync --cloud --project <project>` once per explicitly named project,
in order, continuing through all. Exit 0 if all succeed, 1 if any project sync
or logging op fails, 2 on usage error.

  --log <path>  Append-only log. Overrides default ($ENGRAM_DATA_DIR/
               cloud-sync-projects.log) and ENGRAM_CLOUD_SYNC_LOG.
  -h, --help    Show this help.

Env: ENGRAM_DATA_DIR (defaults to ~/.engram); ENGRAM_CLOUD_SYNC_LOG (log override).
Projects are never inferred from cwd or an env var.
USAGE
}

die_usage() { printf '%s: error: %s\n' "$PROG_NAME" "$*" >&2; printf 'Run with --help for usage.\n' >&2; exit 2; }

log_path=""
projects=()
while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 2 ;;
    --log) [ $# -ge 2 ] || die_usage "--log requires a path argument"; log_path="$2"; shift 2 ;;
    --log=*) log_path="${1#--log=}"; [ -n "$log_path" ] || die_usage "--log requires a non-empty path"; shift ;;
    --) shift; while [ $# -gt 0 ]; do projects+=("$1"); shift; done ;;
    -*) die_usage "unknown option: $1" ;;
    *) projects+=("$1"); shift ;;
  esac
done

[ "${#projects[@]}" -gt 0 ] || die_usage "at least one project is required"

# Log path precedence: --log > ENGRAM_CLOUD_SYNC_LOG > ENGRAM_DATA_DIR default.
[ -z "$log_path" ] && log_path="${ENGRAM_CLOUD_SYNC_LOG:-}"
if [ -z "$log_path" ]; then
  log_path="${ENGRAM_DATA_DIR:-$HOME/.engram}/$DEFAULT_LOG_NAME"
fi
case "$log_path" in /*) ;; *) log_path="$PWD/$log_path" ;; esac  # absolute

log_dir="$(dirname "$log_path")"
[ -d "$log_dir" ] || { printf '%s: error: log directory does not exist: %s\n' "$PROG_NAME" "$log_dir" >&2; exit 2; }

# Timestamped [ts] message to BOTH console and the append-only log; returns
# nonzero on log write failure.
logline() {
  local ts; ts="$(date '+%Y-%m-%dT%H:%M:%S%z')" || return 1
  printf '[%s] %s\n' "$ts" "$*" >>"$log_path" || return 1
  printf '[%s] %s\n' "$ts" "$*"
}

# Run the verified command for one project, tee output live to log and console.
# Returns the engram exit status, or 1 if tee/logging failed. Never hides failures.
run_project() {
  local proj="$1" rc tee_rc
  local -a statuses
  logline "project START project=$proj" || return 1
  engram sync --cloud --project "$proj" 2>&1 | tee -a "$log_path"
  statuses=("${PIPESTATUS[@]}")  # snapshot before any other command mutates it
  rc=${statuses[0]:-1}; tee_rc=${statuses[1]:-1}
  if [ "$rc" -eq 0 ]; then
    logline "project SUCCESS project=$proj exit=0" || return 1
  else
    logline "project FAILURE project=$proj exit=$rc" || return 1
  fi
  [ "$tee_rc" -ne 0 ] && [ "$rc" -eq 0 ] && return 1  # tee/log failed
  return "$rc"
}

overall=0
logline "wrapper START projects=${#projects[@]} log=$log_path" || overall=1
for proj in "${projects[@]}"; do
  run_project "$proj" || overall=1
done
if [ "$overall" -eq 0 ]; then
  logline "wrapper END result=success" || overall=1
else
  logline "wrapper END result=failure overall=$overall" || overall=1
fi
exit "$overall"

#!/usr/bin/env bash
set -uo pipefail

PROG_NAME="cloud-sync-projects.sh"
DEFAULT_LOG_NAME="cloud-sync-projects.log"

usage() {
  cat <<'USAGE'
Usage: cloud-sync-projects.sh [--log <path>] <project> [<project> ...]
Run export then import for each explicitly named project.
An export failure skips that project's import, matching native autosync.
Exit 0 if all succeed, 1 if any project/log op fails, 2 on usage error.
  --log <path>  Overrides default and ENGRAM_CLOUD_SYNC_LOG.
  -h, --help    Show this help.
Env: ENGRAM_DATA_DIR (defaults to ~/.engram); ENGRAM_CLOUD_SYNC_LOG (log override).
USAGE
}

die_usage() { printf '%s: error: %s\n' "$PROG_NAME" "$*" >&2; exit 2; }
log_path=""
projects=()
while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --log) [ $# -ge 2 ] || die_usage "--log requires a path argument"; log_path="$2"; shift 2 ;;
    --log=*) log_path="${1#--log=}"; [ -n "$log_path" ] || die_usage "--log requires a non-empty path"; shift ;;
    --) shift; while [ $# -gt 0 ]; do projects+=("$1"); shift; done ;;
    -*) die_usage "unknown option: $1" ;;
    *) projects+=("$1"); shift ;;
  esac
done

[ "${#projects[@]}" -gt 0 ] || die_usage "at least one project is required"

[ -z "$log_path" ] && log_path="${ENGRAM_CLOUD_SYNC_LOG:-}"
if [ -z "$log_path" ]; then
  log_path="${ENGRAM_DATA_DIR:-$HOME/.engram}/$DEFAULT_LOG_NAME"
fi
case "$log_path" in /*) ;; *) log_path="$PWD/$log_path" ;; esac  # absolute

log_dir="$(dirname "$log_path")"
[ -d "$log_dir" ] || { printf '%s: error: log directory does not exist: %s\n' "$PROG_NAME" "$log_dir" >&2; exit 2; }

logline() {
  local ts; ts="$(date '+%Y-%m-%dT%H:%M:%S%z')" || return 1
  printf '[%s] %s\n' "$ts" "$*" >>"$log_path" || return 1
  printf '[%s] %s\n' "$ts" "$*"
}

run_phase() {
  local proj="$1" phase="$2" rc tee_rc
  local -a statuses
  logline "phase=$phase START project=$proj" || return 1
  if [ "$phase" = "export" ]; then
    engram sync --cloud --project "$proj" 2>&1 | tee -a "$log_path"
  else
    engram sync --cloud --import --project "$proj" 2>&1 | tee -a "$log_path"
  fi
  statuses=("${PIPESTATUS[@]}")  # snapshot before any other command mutates it
  rc=${statuses[0]:-1}; tee_rc=${statuses[1]:-1}
  if [ "$rc" -eq 0 ]; then
    logline "phase=$phase SUCCESS project=$proj exit=0" || return 1
  else
    logline "phase=$phase FAILURE project=$proj exit=$rc" || return 1
  fi
  [ "$tee_rc" -ne 0 ] && [ "$rc" -eq 0 ] && return 1  # tee/log failed
  return "$rc"
}

run_project() {
  local proj="$1" rc
  logline "project START project=$proj" || return 1
  run_phase "$proj" export
  rc=$?
  if [ "$rc" -ne 0 ]; then
    logline "project FAILURE project=$proj phase=export exit=$rc" || return 1
    return "$rc"  # Native autosync does not pull after a failed push.
  fi
  run_phase "$proj" import
  rc=$?
  if [ "$rc" -ne 0 ]; then
    logline "project FAILURE project=$proj phase=import exit=$rc" || return 1
    return "$rc"
  fi
  logline "project SUCCESS project=$proj" || return 1
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

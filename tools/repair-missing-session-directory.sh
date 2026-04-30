#!/usr/bin/env bash
# Temporary/manual rescue helper for legacy Engram Cloud upgrade data.
#
# This script patches legacy `sync_mutations` rows whose upsert payloads are
# missing required fields, producing doctor errors such as:
#   session payload directory is required and cannot be inferred from local state (seq=N entity=session op=upsert)
#   observation payload missing required upsert fields: title (seq=N entity=observation op=upsert)
#
# It is intentionally conservative: dry-run is the default, `--apply` creates a
# timestamped SQLite backup per applied blocker, loop mode requires `--apply`,
# and it never touches `last_acked_seq` or deletes sync mutations. It can also
# repair local `sessions.directory` and `observations` rows left incomplete by
# legacy data after doctor reports ready, because those rows can still be
# included in a final push chunk.
# Prefer the
# built-in `engram cloud upgrade repair` flow when it can infer the directory on
# its own; use this only for the manual rescue case.

set -eu

usage() {
  cat <<'USAGE'
Usage: repair-missing-session-directory.sh [--apply] [--interactive] [--all] [--fix-empty-sessions] [--fix-empty-observations] [--fix-exported] [--max N] [--seq N] PROJECT [DIRECTORY]

Safely patch one legacy sync mutation payload:
  - session upsert missing `directory`
  - observation upsert missing required payload fields from local `observations`

Arguments:
  PROJECT     Required Engram project name passed to cloud upgrade doctor.
  DIRECTORY   Optional session directory for session repairs. Defaults to git
              root, then pwd. Accepted but not required for observation repairs.

Flags:
  --apply     Write changes. Default is dry-run and does not modify the DB.
  --interactive
              Prompt for missing observation fields after local inference fails.
              Without this flag, incomplete observation repairs stay blocked.
  --all       Apply supported blockers one at a time, rerunning doctor after each
               repair until no supported session/observation upsert blocker remains.
               Requires --apply; when doctor reports ready, also repairs local
               sessions for PROJECT whose directory is empty/null, then repairs
               exported observations for PROJECT with missing required fields.
               Use a one-shot dry-run first to preview changes.
  --fix-empty-sessions
               When doctor has no supported mutation blocker, repair local
               sessions for PROJECT whose directory is empty/null. Does not
               modify sync_mutations or last_acked_seq.
  --fix-empty-observations
               Bypass doctor parsing and scan/repair local observations in the
               PROJECT export scope whose cloud-push required fields are empty.
               Does not modify sync_mutations or last_acked_seq.
  --fix-exported
               Shortcut for --fix-empty-sessions --fix-empty-observations.
  --max N     Maximum --all iterations before stopping. Defaults to 20.
  --seq N     Use a known sync_mutations.seq instead of parsing doctor output.
  -h, --help  Show this help.

Environment:
  ENGRAM_DB   SQLite DB path. Defaults to ~/.engram/engram.db.

Next flow after a successful apply:
  engram cloud upgrade doctor --project PROJECT
  engram cloud upgrade repair --project PROJECT --dry-run
  engram cloud upgrade repair --project PROJECT --apply
  engram sync --cloud --project PROJECT
USAGE
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

info() {
  printf '%s\n' "$*"
}

sql_escape() {
  # SQLite string literal escaping: single quote becomes two single quotes.
  # Usage: printf "'%s'" "$(sql_escape "$value")"
  printf "%s" "$1" | sed "s/'/''/g"
}

single_line_excerpt() {
  # Keep prompts compact and avoid multiline terminal surprises.
  printf '%s' "$1" | tr '\r\n\t' '   ' | cut -c 1-300
}

prompt_value() {
  field=$1
  label=$2
  default_value=$3
  required=$4

  while :; do
    if [ "$default_value" ]; then
      printf '%s [%s]: ' "$label" "$default_value" >&2
    else
      printf '%s: ' "$label" >&2
    fi

    if ! IFS= read -r answer; then
      die "interactive input ended before $field was provided"
    fi

    if [ ! "$answer" ] && [ "$default_value" ]; then
      answer=$default_value
    fi

    if [ "$answer" ] || [ "$required" != "1" ]; then
      printf '%s\n' "$answer"
      return 0
    fi

    printf 'error: %s is required and must not be empty.\n' "$field" >&2
  done
}

abs_path_if_possible() {
  case "$1" in
    /* | [A-Za-z]:/*) printf '%s\n' "$1" ;;
    *)
      if command -v realpath >/dev/null 2>&1; then
        realpath "$1" 2>/dev/null || printf '%s/%s\n' "$(pwd)" "$1"
      else
        printf '%s/%s\n' "$(pwd)" "$1"
      fi
      ;;
  esac
}

resolve_directory() {
  if [ "$DIRECTORY_ARG" ]; then
    abs_path_if_possible "$DIRECTORY_ARG"
  elif command -v git >/dev/null 2>&1 && git_root=$(git rev-parse --show-toplevel 2>/dev/null); then
    printf '%s\n' "$git_root"
  else
    pwd
  fi
}

is_absoluteish_directory() {
  case "$1" in
    /* | [A-Za-z]:/*) return 0 ;;
    *) return 1 ;;
  esac
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

db_path() {
  if [ "${ENGRAM_DB:-}" ]; then
    printf '%s\n' "$ENGRAM_DB"
  else
    printf '%s\n' "$HOME/.engram/engram.db"
  fi
}

sqlite_scalar() {
  sqlite3 -batch -noheader "$DB" "$1"
}

sqlite_box() {
  sqlite3 -batch -box "$DB" "$1"
}

have_table() {
  [ "$(sqlite_scalar "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='$(sql_escape "$1")';")" = "1" ]
}

have_column() {
  [ "$(sqlite_scalar "SELECT COUNT(*) FROM pragma_table_info('$(sql_escape "$1")') WHERE name = '$(sql_escape "$2")';")" = "1" ]
}

parse_target_from_doctor() {
  output_file=$1
  if ! engram cloud upgrade doctor --project "$PROJECT" >"$output_file" 2>&1; then
    : # doctor is expected to fail for the legacy mutation this helper repairs.
  fi
  parsed=$(sed -n \
    -e 's/.*seq=\([0-9][0-9]*\) entity=session op=upsert.*/\1 session/p' \
    -e 's/.*seq=\([0-9][0-9]*\) entity=observation op=upsert.*/\1 observation/p' \
    "$output_file" | sed -n '1p')
  [ "$parsed" ] || return 1
  printf '%s\n' "$parsed"
}

json_required_count_sql() {
  printf "%s" "(
    CASE WHEN ifnull(json_extract(payload, '$.sync_id'), '') = '' THEN 1 ELSE 0 END +
    CASE WHEN ifnull(json_extract(payload, '$.session_id'), '') = '' THEN 1 ELSE 0 END +
    CASE WHEN ifnull(json_extract(payload, '$.type'), '') = '' THEN 1 ELSE 0 END +
    CASE WHEN ifnull(json_extract(payload, '$.title'), '') = '' THEN 1 ELSE 0 END +
    CASE WHEN ifnull(json_extract(payload, '$.content'), '') = '' THEN 1 ELSE 0 END +
    CASE WHEN ifnull(json_extract(payload, '$.scope'), '') = '' THEN 1 ELSE 0 END
  )"
}

APPLY=0
INTERACTIVE=0
ALL=0
FIX_EMPTY_SESSIONS=0
FIX_EMPTY_OBSERVATIONS=0
MAX_ITERATIONS=20
SEQ=""
ENTITY=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --apply)
      APPLY=1
      shift
      ;;
    --interactive)
      INTERACTIVE=1
      shift
      ;;
    --all)
      ALL=1
      shift
      ;;
    --fix-empty-sessions)
      FIX_EMPTY_SESSIONS=1
      shift
      ;;
    --fix-empty-observations)
      FIX_EMPTY_OBSERVATIONS=1
      shift
      ;;
    --fix-exported)
      FIX_EMPTY_SESSIONS=1
      FIX_EMPTY_OBSERVATIONS=1
      shift
      ;;
    --max)
      [ "$#" -ge 2 ] || die "--max requires a positive integer"
      MAX_ITERATIONS=$2
      shift 2
      ;;
    --max=*)
      MAX_ITERATIONS=${1#--max=}
      shift
      ;;
    --seq)
      [ "$#" -ge 2 ] || die "--seq requires a numeric value"
      SEQ=$2
      shift 2
      ;;
    --seq=*)
      SEQ=${1#--seq=}
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    --*)
      die "unknown flag: $1"
      ;;
    *)
      break
      ;;
  esac
done

[ "$#" -ge 1 ] || { usage >&2; die "PROJECT is required"; }
[ "$#" -le 2 ] || die "too many arguments"

PROJECT=$1
DIRECTORY_ARG=${2:-}
[ "$PROJECT" ] || die "PROJECT must not be empty"

case "$SEQ" in
  "" | *[!0-9]*) [ "$SEQ" = "" ] || die "--seq must be a positive integer" ;;
esac

case "$MAX_ITERATIONS" in
  "" | *[!0-9]*) die "--max must be a positive integer" ;;
esac
[ "$MAX_ITERATIONS" -gt 0 ] || die "--max must be greater than zero"

if [ "$ALL" -eq 1 ] && [ "$APPLY" -ne 1 ]; then
  die "--all requires --apply because loop mode writes repeatedly; run a one-shot dry-run first: $0 <project>"
fi

if [ "$ALL" -eq 1 ] && [ "$SEQ" ]; then
  die "--all cannot be combined with --seq; loop mode reads the next blocker from doctor each iteration"
fi

require_cmd sqlite3

DB=$(db_path)
[ -f "$DB" ] || die "SQLite DB not found: $DB (override with ENGRAM_DB=/path/to/engram.db)"

have_table sync_mutations || die "sync_mutations table not found in DB: $DB"

empty_session_count() {
  if ! have_table sessions; then
    printf '0\n'
    return 0
  fi
  scope_predicate=$(empty_session_scope_predicate)
  sqlite_scalar "SELECT COUNT(*) FROM sessions WHERE ifnull(directory, '') = '' AND $scope_predicate;"
}

empty_session_scope_predicate() {
  PROJECT_SQL=$(sql_escape "$PROJECT")
  predicate="(project = '$PROJECT_SQL'"

  if have_table observations; then
    predicate="$predicate OR id IN (
      SELECT session_id FROM observations
       WHERE ifnull(project, '') = '$PROJECT_SQL'
          OR (ifnull(project, '') = '' AND session_id IN (SELECT id FROM sessions WHERE project = '$PROJECT_SQL'))
    )"
  fi

  if have_table user_prompts; then
    predicate="$predicate OR id IN (
      SELECT session_id FROM user_prompts
       WHERE ifnull(project, '') = '$PROJECT_SQL'
          OR (ifnull(project, '') = '' AND session_id IN (SELECT id FROM sessions WHERE project = '$PROJECT_SQL'))
    )"
  fi

  printf '%s\n' "$predicate)"
}

preview_empty_sessions() {
  scope_predicate=$(empty_session_scope_predicate)
  info "Affected local sessions:"
  if have_column sessions started_at; then
    sqlite_box "SELECT id, project, started_at, ifnull(directory, '') AS directory FROM sessions WHERE ifnull(directory, '') = '' AND $scope_predicate ORDER BY ifnull(started_at, ''), id;" || true
  else
    sqlite_box "SELECT id, project, ifnull(directory, '') AS directory FROM sessions WHERE ifnull(directory, '') = '' AND $scope_predicate ORDER BY id;" || true
  fi
}

repair_empty_sessions() {
  have_table sessions || die "sessions table not found in DB: $DB"
  missing_count=$(empty_session_count)

  if [ "$missing_count" = "0" ]; then
    info "No local sessions in project export scope with empty/null directory found for project '$PROJECT'."
    return 0
  fi

  DIRECTORY=$(resolve_directory)
  is_absoluteish_directory "$DIRECTORY" || die "directory must be absolute-ish (/..., /c/..., or C:/...): $DIRECTORY"
  DIRECTORY_SQL=$(sql_escape "$DIRECTORY")

  info "Preview"
  info "-------"
  info "DB path: $DB"
  info "Project: $PROJECT"
  info "Repair: local sessions.directory fallback"
  info "Directory: $DIRECTORY"
  info "Rows missing directory: $missing_count"
  info ""
  preview_empty_sessions

  if [ "$APPLY" -ne 1 ]; then
    info ""
    info "Dry-run only: no database changes were made. Re-run with --apply --fix-empty-sessions or --apply --all to write."
    return 0
  fi

  backup="$DB.repair-empty-sessions-directory.$PROJECT.$(date +%Y%m%d%H%M%S).bak"
  cp "$DB" "$backup"
  info ""
  info "Backup created: $backup"
  scope_predicate=$(empty_session_scope_predicate)

  sqlite3 -batch "$DB" "BEGIN;
UPDATE sessions
SET directory = '$DIRECTORY_SQL'
WHERE ifnull(directory, '') = ''
  AND $scope_predicate;
COMMIT;"

  remaining_count=$(empty_session_count)
  [ "$remaining_count" = "0" ] || die "verification failed: $remaining_count sessions still have empty/null directory for project '$PROJECT'"
  info "Apply complete: local sessions.directory verified for $missing_count row(s)."
  return 0
}

observation_export_scope_predicate() {
  PROJECT_SQL=$(sql_escape "$PROJECT")
  predicate="(ifnull(observations.project, '') = '$PROJECT_SQL'"

  if have_table sessions; then
    predicate="$predicate OR (ifnull(observations.project, '') = '' AND observations.session_id IN (SELECT id FROM sessions WHERE ifnull(project, '') = '$PROJECT_SQL'))"
  fi

  printf '%s\n' "$predicate)"
}

observation_missing_predicate() {
  printf '%s\n' "(
    ifnull(sync_id, '') = '' OR
    ifnull(session_id, '') = '' OR
    ifnull(type, '') = '' OR
    ifnull(title, '') = '' OR
    ifnull(content, '') = '' OR
    ifnull(scope, '') = ''
  )"
}

observation_missing_fields_sql() {
  printf '%s' "rtrim(
    CASE WHEN ifnull(sync_id, '') = '' THEN 'sync_id,' ELSE '' END ||
    CASE WHEN ifnull(session_id, '') = '' THEN 'session_id,' ELSE '' END ||
    CASE WHEN ifnull(type, '') = '' THEN 'type,' ELSE '' END ||
    CASE WHEN ifnull(title, '') = '' THEN 'title,' ELSE '' END ||
    CASE WHEN ifnull(content, '') = '' THEN 'content,' ELSE '' END ||
    CASE WHEN ifnull(scope, '') = '' THEN 'scope,' ELSE '' END,
    ','
  )"
}

observation_inferred_title_sql() {
  printf '%s' "substr(trim(replace(replace(replace(content, char(13), ' '), char(10), ' '), char(9), ' ')), 1, 120)"
}

empty_observation_count() {
  if ! have_table observations; then
    printf '0\n'
    return 0
  fi
  scope_predicate=$(observation_export_scope_predicate)
  missing_predicate=$(observation_missing_predicate)
  sqlite_scalar "SELECT COUNT(*) FROM observations WHERE $scope_predicate AND $missing_predicate;"
}

preview_empty_observations() {
  scope_predicate=$(observation_export_scope_predicate)
  missing_predicate=$(observation_missing_predicate)
  missing_fields=$(observation_missing_fields_sql)

  info "Affected local observations:"
  sqlite_box "SELECT id, ifnull(sync_id, '') AS sync_id, ifnull(session_id, '') AS session_id, ifnull(project, '') AS project, ifnull(type, '') AS type, ifnull(title, '') AS title, ifnull(scope, '') AS scope, $missing_fields AS missing_fields FROM observations WHERE $scope_predicate AND $missing_predicate ORDER BY id;" || true
}

blocking_observation_count() {
  scope_predicate=$(observation_export_scope_predicate)
  missing_predicate=$(observation_missing_predicate)
  sqlite_scalar "SELECT COUNT(*) FROM observations WHERE $scope_predicate AND $missing_predicate AND (
    ifnull(session_id, '') = '' OR
    ifnull(content, '') = '' OR
    (ifnull(title, '') = '' AND ifnull(content, '') = '') OR
    (ifnull(sync_id, '') = '' AND ifnull(id, '') = '')
  );"
}

preview_blocking_observations() {
  scope_predicate=$(observation_export_scope_predicate)
  missing_predicate=$(observation_missing_predicate)
  missing_fields=$(observation_missing_fields_sql)

  info "Blocked local observations needing human input:"
  sqlite_box "SELECT id, ifnull(sync_id, '') AS sync_id, ifnull(session_id, '') AS session_id, ifnull(project, '') AS project, ifnull(type, '') AS type, ifnull(title, '') AS title, ifnull(scope, '') AS scope, $missing_fields AS missing_fields FROM observations WHERE $scope_predicate AND $missing_predicate AND (
    ifnull(session_id, '') = '' OR
    ifnull(content, '') = '' OR
    (ifnull(title, '') = '' AND ifnull(content, '') = '') OR
    (ifnull(sync_id, '') = '' AND ifnull(id, '') = '')
  ) ORDER BY id;" || true
}

prompt_empty_observations() {
  scope_predicate=$(observation_export_scope_predicate)
  missing_predicate=$(observation_missing_predicate)

  sqlite3 -batch -noheader -separator '|' "$DB" "SELECT id FROM observations WHERE $scope_predicate AND $missing_predicate AND (ifnull(content, '') = '' OR (ifnull(title, '') = '' AND ifnull(content, '') = '')) ORDER BY id;" |
  while IFS='|' read -r observation_id; do
    [ "$observation_id" ] || continue
    OBS_ID_SQL=$(sql_escape "$observation_id")
    current_content=$(sqlite_scalar "SELECT ifnull(content, '') FROM observations WHERE id = CAST('$OBS_ID_SQL' AS INTEGER);")
    current_title=$(sqlite_scalar "SELECT ifnull(title, '') FROM observations WHERE id = CAST('$OBS_ID_SQL' AS INTEGER);")
    current_sync_id=$(sqlite_scalar "SELECT ifnull(sync_id, '') FROM observations WHERE id = CAST('$OBS_ID_SQL' AS INTEGER);")
    current_session_id=$(sqlite_scalar "SELECT ifnull(session_id, '') FROM observations WHERE id = CAST('$OBS_ID_SQL' AS INTEGER);")

    info "Manual local observation completion needed"
    info "-----------------------------------------"
    info "Observation id: $observation_id"
    info "sync_id: ${current_sync_id:-<missing>}"
    info "session_id: ${current_session_id:-<missing>}"

    if [ ! "$current_content" ]; then
      current_content=$(prompt_value "content" "content" "" 1)
      CURRENT_CONTENT_SQL=$(sql_escape "$current_content")
      sqlite3 -batch "$DB" "UPDATE observations SET content = '$CURRENT_CONTENT_SQL' WHERE id = CAST('$OBS_ID_SQL' AS INTEGER) AND ifnull(content, '') = '';"
    fi

    if [ ! "$current_title" ]; then
      title_default=$(single_line_excerpt "$current_content" | cut -c 1-120)
      current_title=$(prompt_value "title" "title" "$title_default" 1)
      CURRENT_TITLE_SQL=$(sql_escape "$current_title")
      sqlite3 -batch "$DB" "UPDATE observations SET title = '$CURRENT_TITLE_SQL' WHERE id = CAST('$OBS_ID_SQL' AS INTEGER) AND ifnull(title, '') = '';"
    fi
  done
}

repair_empty_observations() {
  have_table observations || die "observations table not found in DB: $DB"
  missing_count=$(empty_observation_count)

  if [ "$missing_count" = "0" ]; then
    info "No local observations in project export scope with missing required fields found for project '$PROJECT'."
    return 0
  fi

  info "Preview"
  info "-------"
  info "DB path: $DB"
  info "Project: $PROJECT"
  info "Repair: local observations required-field fallback"
  info "Rows missing required fields: $missing_count"
  info ""
  preview_empty_observations

  if [ "$APPLY" -ne 1 ]; then
    info ""
    info "Dry-run only: no database changes were made. Re-run with --apply --fix-empty-observations, --apply --fix-exported, or --apply --all to write."
    return 0
  fi

  backup="$DB.repair-empty-observations.$PROJECT.$(date +%Y%m%d%H%M%S).bak"
  cp "$DB" "$backup"
  info ""
  info "Backup created: $backup"

  if [ "$INTERACTIVE" -eq 1 ]; then
    prompt_empty_observations
  fi

  blocker_count=$(blocking_observation_count)
  if [ "$blocker_count" != "0" ]; then
    preview_blocking_observations
    die "blocked: $blocker_count exported observation row(s) still need non-inferable required fields; session_id is never invented, and empty content/title require --interactive input"
  fi

  scope_predicate=$(observation_export_scope_predicate)
  missing_predicate=$(observation_missing_predicate)
  inferred_title=$(observation_inferred_title_sql)

  sqlite3 -batch "$DB" "BEGIN;
UPDATE observations
SET sync_id = CASE WHEN ifnull(sync_id, '') = '' AND ifnull(id, '') != '' THEN CAST(id AS TEXT) ELSE sync_id END,
    type = CASE WHEN ifnull(type, '') = '' THEN 'manual' ELSE type END,
    title = CASE WHEN ifnull(title, '') = '' AND ifnull(content, '') != '' THEN $inferred_title ELSE title END,
    scope = CASE WHEN ifnull(scope, '') = '' THEN 'project' ELSE scope END
WHERE $scope_predicate
  AND $missing_predicate;
COMMIT;"

  remaining_count=$(empty_observation_count)
  if [ "$remaining_count" != "0" ]; then
    preview_empty_observations
    die "verification failed: $remaining_count exported observation row(s) still have missing required fields for project '$PROJECT'"
  fi

  info "Apply complete: local observation required fields verified for $missing_count row(s)."
  return 0
}

handle_no_supported_blocker() {
  context=$1
  missing_count=$(empty_session_count)
  missing_observation_count=$(empty_observation_count)

  if [ "$missing_count" != "0" ]; then
    info "No supported sync_mutations blocker found, but $missing_count local session row(s) in project export scope for '$PROJECT' have empty/null directory."
    if [ "$FIX_EMPTY_SESSIONS" -eq 1 ] || [ "$ALL" -eq 1 ]; then
      repair_empty_sessions
      return 0
    fi
    info "Rerun with --apply --fix-empty-sessions or --apply --all after reviewing the dry-run preview."
    return 2
  fi

  if [ "$missing_observation_count" != "0" ]; then
    info "No supported sync_mutations blocker found, but $missing_observation_count local observation row(s) in project export scope for '$PROJECT' have missing required fields."
    if [ "$FIX_EMPTY_OBSERVATIONS" -eq 1 ] || [ "$ALL" -eq 1 ]; then
      repair_empty_observations
      return 0
    fi
    info "Rerun with --fix-empty-observations for a dry-run preview, then --apply --fix-empty-observations or --apply --all to write."
    return 2
  fi

  info "No supported session/observation upsert blocker found."
  if [ "$FIX_EMPTY_SESSIONS" -eq 1 ]; then
    info "No local sessions in project export scope with empty/null directory found for project '$PROJECT'."
  fi
  if [ "$FIX_EMPTY_OBSERVATIONS" -eq 1 ]; then
    info "No local observations in project export scope with missing required fields found for project '$PROJECT'."
  fi
  [ "$context" = "loop" ] && info "Loop mode complete."
  return 1
}

if [ "$ALL" -ne 1 ] && [ ! "$SEQ" ] && { [ "$FIX_EMPTY_SESSIONS" -eq 1 ] || [ "$FIX_EMPTY_OBSERVATIONS" -eq 1 ]; }; then
  [ "$FIX_EMPTY_SESSIONS" -eq 1 ] && repair_empty_sessions
  [ "$FIX_EMPTY_OBSERVATIONS" -eq 1 ] && repair_empty_observations
  exit 0
fi

if [ ! "$SEQ" ] || [ "$ALL" -eq 1 ]; then
  require_cmd engram
fi

if [ ! "$SEQ" ] && [ "$ALL" -ne 1 ]; then
  tmp=${TMPDIR:-/tmp}/engram-doctor-output.$$.txt
  trap 'rm -f "$tmp"' EXIT HUP INT TERM
  if ! target=$(parse_target_from_doctor "$tmp"); then
    cat "$tmp" >&2 || true
    if handle_no_supported_blocker "one-shot"; then
      exit 0
    else
      no_blocker_rc=$?
      [ "$no_blocker_rc" = "2" ] && exit 1
      [ "$no_blocker_rc" = "1" ] && exit 0
    fi
    die "could not parse 'seq=N entity=session op=upsert' or 'seq=N entity=observation op=upsert' from doctor output; rerun with --seq N"
  fi
  SEQ=${target%% *}
  ENTITY=${target#* }
fi

repair_current_target() {
PROJECT_SQL=$(sql_escape "$PROJECT")
SEQ_SQL=$(sql_escape "$SEQ")

if [ ! "$ENTITY" ]; then
  row_count=$(sqlite_scalar "SELECT COUNT(*) FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity IN ('session', 'observation') AND op = 'upsert';")
  [ "$row_count" = "1" ] || die "expected exactly one sync_mutations row for seq=$SEQ entity=session|observation op=upsert, found $row_count"
  ENTITY=$(sqlite_scalar "SELECT entity FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity IN ('session', 'observation') AND op = 'upsert';")
fi

case "$ENTITY" in
  session | observation) ;;
  *) die "unsupported entity for seq=$SEQ: $ENTITY" ;;
esac

row_count=$(sqlite_scalar "SELECT COUNT(*) FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = '$ENTITY' AND op = 'upsert';")
[ "$row_count" = "1" ] || die "expected exactly one sync_mutations row for seq=$SEQ entity=$ENTITY op=upsert, found $row_count"

payload_project=$(sqlite_scalar "SELECT ifnull(json_extract(payload, '$.project'), '') FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = '$ENTITY' AND op = 'upsert';")
[ "$payload_project" = "$PROJECT" ] || die "payload project mismatch for seq=$SEQ: got '$payload_project', want '$PROJECT'"

if [ "$ENTITY" = "session" ]; then
  DIRECTORY=$(resolve_directory)

  is_absoluteish_directory "$DIRECTORY" || die "directory must be absolute-ish (/..., /c/..., or C:/...): $DIRECTORY"
  DIRECTORY_SQL=$(sql_escape "$DIRECTORY")

  existing_directory=$(sqlite_scalar "SELECT ifnull(json_extract(payload, '$.directory'), '') FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'session' AND op = 'upsert';")
  [ ! "$existing_directory" ] || die "payload already has directory for seq=$SEQ: $existing_directory"

  session_id=$(sqlite_scalar "SELECT coalesce(nullif(json_extract(payload, '$.id'), ''), nullif(entity_key, '')) FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'session' AND op = 'upsert';")
  [ "$session_id" ] || die "payload id/entity_key is required for seq=$SEQ"
  SESSION_ID_SQL=$(sql_escape "$session_id")

  current_payload=$(sqlite_scalar "SELECT payload FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'session' AND op = 'upsert';")
  patched_payload=$(sqlite_scalar "SELECT json_set(payload, '$.directory', '$DIRECTORY_SQL') FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'session' AND op = 'upsert';")

  info "Preview"
  info "-------"
  info "DB path: $DB"
  info "Project: $PROJECT"
  info "Seq: $SEQ"
  info "Entity: $ENTITY"
  info "Session id: $session_id"
  info "Directory: $DIRECTORY"
  info ""
  info "Current payload:"
  printf '%s\n' "$current_payload"
  info ""
  info "Patched payload:"
  printf '%s\n' "$patched_payload"
  info ""
  info "Matching mutation row:"
  if have_column sync_mutations project; then
    sqlite_box "SELECT seq, entity, entity_key, op, ifnull(project, '') AS row_project FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER);" || true
  else
    sqlite_box "SELECT seq, entity, entity_key, op FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER);" || true
  fi

  if [ "$APPLY" -ne 1 ]; then
    info ""
    info "Dry-run only: no database changes were made. Re-run with --apply to write."
  else
    backup="$DB.repair-missing-session-directory.seq-$SEQ.$(date +%Y%m%d%H%M%S).bak"
    cp "$DB" "$backup"
    info ""
    info "Backup created: $backup"

    sqlite3 -batch "$DB" "BEGIN;
UPDATE sync_mutations
SET payload = json_set(payload, '$.directory', '$DIRECTORY_SQL')
WHERE seq = CAST('$SEQ_SQL' AS INTEGER)
  AND entity = 'session'
  AND op = 'upsert'
  AND ifnull(json_extract(payload, '$.project'), '') = '$PROJECT_SQL'
  AND ifnull(json_extract(payload, '$.directory'), '') = '';
COMMIT;"

    if have_table sessions; then
      sqlite3 -batch "$DB" "UPDATE sessions
SET directory = '$DIRECTORY_SQL'
WHERE id = '$SESSION_ID_SQL'
  AND ifnull(directory, '') = '';"
    fi

    verified_directory=$(sqlite_scalar "SELECT ifnull(json_extract(payload, '$.directory'), '') FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'session' AND op = 'upsert';")
    [ "$verified_directory" = "$DIRECTORY" ] || die "verification failed: payload directory is '$verified_directory', want '$DIRECTORY'"
    info "Apply complete: payload directory verified."
  fi
else
  [ ! "$DIRECTORY_ARG" ] || info "Note: DIRECTORY argument is ignored for observation repairs."
  have_table observations || die "observations table not found in DB: $DB"

  payload_sync_id=$(sqlite_scalar "SELECT ifnull(json_extract(payload, '$.sync_id'), '') FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'observation' AND op = 'upsert';")
  entity_key=$(sqlite_scalar "SELECT ifnull(entity_key, '') FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'observation' AND op = 'upsert';")
  payload_session_id=$(sqlite_scalar "SELECT ifnull(json_extract(payload, '$.session_id'), '') FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'observation' AND op = 'upsert';")
  payload_type=$(sqlite_scalar "SELECT ifnull(json_extract(payload, '$.type'), '') FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'observation' AND op = 'upsert';")
  payload_title=$(sqlite_scalar "SELECT ifnull(json_extract(payload, '$.title'), '') FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'observation' AND op = 'upsert';")
  payload_content=$(sqlite_scalar "SELECT ifnull(json_extract(payload, '$.content'), '') FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'observation' AND op = 'upsert';")
  payload_scope=$(sqlite_scalar "SELECT ifnull(json_extract(payload, '$.scope'), '') FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'observation' AND op = 'upsert';")

  observation_sync_id=${payload_sync_id:-$entity_key}
  local_rowid=""
  local_sync_id=""
  local_session_id=""
  local_type=""
  local_title=""
  local_content=""
  local_scope=""

  if [ "$observation_sync_id" ]; then
    OBS_SYNC_ID_SQL=$(sql_escape "$observation_sync_id")
    local_count=$(sqlite_scalar "SELECT COUNT(*) FROM observations WHERE sync_id = '$OBS_SYNC_ID_SQL';")
    if [ "$local_count" != "0" ]; then
      local_rowid=$(sqlite_scalar "SELECT id FROM observations WHERE sync_id = '$OBS_SYNC_ID_SQL' ORDER BY ifnull(updated_at, '') DESC, ifnull(created_at, '') DESC, id DESC LIMIT 1;")
    fi
  fi

  if [ "$local_rowid" ]; then
    LOCAL_ROWID_SQL=$(sql_escape "$local_rowid")
    local_sync_id=$(sqlite_scalar "SELECT ifnull(sync_id, '') FROM observations WHERE id = CAST('$LOCAL_ROWID_SQL' AS INTEGER);")
    local_session_id=$(sqlite_scalar "SELECT ifnull(session_id, '') FROM observations WHERE id = CAST('$LOCAL_ROWID_SQL' AS INTEGER);")
    local_type=$(sqlite_scalar "SELECT ifnull(type, '') FROM observations WHERE id = CAST('$LOCAL_ROWID_SQL' AS INTEGER);")
    local_title=$(sqlite_scalar "SELECT ifnull(title, '') FROM observations WHERE id = CAST('$LOCAL_ROWID_SQL' AS INTEGER);")
    local_content=$(sqlite_scalar "SELECT ifnull(content, '') FROM observations WHERE id = CAST('$LOCAL_ROWID_SQL' AS INTEGER);")
    local_scope=$(sqlite_scalar "SELECT ifnull(scope, '') FROM observations WHERE id = CAST('$LOCAL_ROWID_SQL' AS INTEGER);")
  fi

  final_sync_id=${payload_sync_id:-${entity_key:-$local_sync_id}}
  final_session_id=${payload_session_id:-$local_session_id}
  final_type=${payload_type:-$local_type}
  final_title=${payload_title:-$local_title}
  final_content=${payload_content:-$local_content}
  final_scope=${payload_scope:-$local_scope}

  excerpt_source=${payload_content:-$local_content}
  content_excerpt=$(single_line_excerpt "$excerpt_source")

  missing_after_inference=0
  [ "$final_sync_id" ] || missing_after_inference=$((missing_after_inference + 1))
  [ "$final_session_id" ] || missing_after_inference=$((missing_after_inference + 1))
  [ "$final_type" ] || missing_after_inference=$((missing_after_inference + 1))
  [ "$final_title" ] || missing_after_inference=$((missing_after_inference + 1))
  [ "$final_content" ] || missing_after_inference=$((missing_after_inference + 1))
  [ "$final_scope" ] || missing_after_inference=$((missing_after_inference + 1))

  if [ "$missing_after_inference" -ne 0 ] && [ "$INTERACTIVE" -eq 1 ]; then
    info "Manual observation completion needed"
    info "------------------------------------"
    info "Seq: $SEQ"
    info "sync_id/entity_key: ${observation_sync_id:-<missing>}"
    info "Session id: ${final_session_id:-<missing>}"
    info "Content excerpt: ${content_excerpt:-<none>}"
    info ""
    info "Current payload:"
    sqlite_scalar "SELECT payload FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'observation' AND op = 'upsert';"
    info ""
    info "Local observation row:"
    if [ "$local_rowid" ]; then
      sqlite_box "SELECT id, sync_id, session_id, type, title, content, project, scope FROM observations WHERE id = CAST('$LOCAL_ROWID_SQL' AS INTEGER);" || true
    else
      info "<not found>"
    fi
    info ""

    [ "$final_sync_id" ] || final_sync_id=$(prompt_value "sync_id" "sync_id" "${payload_sync_id:-${entity_key:-$local_sync_id}}" 1)
    [ "$final_session_id" ] || final_session_id=$(prompt_value "session_id" "session_id" "${payload_session_id:-$local_session_id}" 1)
    [ "$final_type" ] || final_type=$(prompt_value "type" "type (press Enter for default)" "${local_type:-manual}" 1)
    if [ ! "$final_title" ]; then
      final_title=$(prompt_value "title" "title (content excerpt: ${content_excerpt:-<none>})" "" 1)
    fi
    if [ ! "$final_content" ]; then
      [ "$content_excerpt" ] || info "Warning: no content excerpt is available; content must still be provided."
      final_content=$(prompt_value "content" "content" "" 1)
      content_excerpt=$(single_line_excerpt "$final_content")
    fi
    [ "$final_scope" ] || final_scope=$(prompt_value "scope" "scope (press Enter for default)" "${local_scope:-project}" 1)
  fi

  [ "$final_sync_id" ] && [ "$final_session_id" ] && [ "$final_type" ] && [ "$final_title" ] && [ "$final_content" ] && [ "$final_scope" ] || die "blocked: local observation cannot fill all required payload fields for seq=$SEQ; rerun with --interactive to provide sync_id, session_id, type, title, content, and scope"

  FINAL_SYNC_ID_SQL=$(sql_escape "$final_sync_id")
  FINAL_SESSION_ID_SQL=$(sql_escape "$final_session_id")
  FINAL_TYPE_SQL=$(sql_escape "$final_type")
  FINAL_TITLE_SQL=$(sql_escape "$final_title")
  FINAL_CONTENT_SQL=$(sql_escape "$final_content")
  FINAL_SCOPE_SQL=$(sql_escape "$final_scope")

  patched_payload_sql="json_set(
    payload,
    '$.sync_id', '$FINAL_SYNC_ID_SQL',
    '$.session_id', '$FINAL_SESSION_ID_SQL',
    '$.type', '$FINAL_TYPE_SQL',
    '$.title', '$FINAL_TITLE_SQL',
    '$.content', '$FINAL_CONTENT_SQL',
    '$.scope', '$FINAL_SCOPE_SQL'
  )"

  current_payload=$(sqlite_scalar "SELECT payload FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'observation' AND op = 'upsert';")
  patched_payload=$(sqlite_scalar "SELECT $patched_payload_sql FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'observation' AND op = 'upsert';")

  missing_after_patch=$(sqlite_scalar "WITH patched(payload) AS (SELECT $patched_payload_sql FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'observation' AND op = 'upsert') SELECT $(json_required_count_sql) FROM patched;")
  [ "$missing_after_patch" = "0" ] || die "blocked: local observation cannot fill all required payload fields for seq=$SEQ; required fields sync_id, session_id, type, title, content, scope must be non-empty"

  info "Preview"
  info "-------"
  info "DB path: $DB"
  info "Project: $PROJECT"
  info "Seq: $SEQ"
  info "Entity: $ENTITY"
  info "Observation sync_id: $observation_sync_id"
  info "Local observation row id: $local_rowid"
  info "Content excerpt: ${content_excerpt:-<none>}"
  info ""
  info "Current payload:"
  printf '%s\n' "$current_payload"
  info ""
  info "Patched payload:"
  printf '%s\n' "$patched_payload"
  info ""
  info "Local observation row:"
  if [ "$local_rowid" ]; then
    sqlite_box "SELECT id, sync_id, session_id, type, title, content, project, scope FROM observations WHERE id = CAST('$LOCAL_ROWID_SQL' AS INTEGER);" || true
  else
    info "<not found>"
  fi
  info ""
  info "Matching mutation row:"
  if have_column sync_mutations project; then
    sqlite_box "SELECT seq, entity, entity_key, op, ifnull(project, '') AS row_project FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER);" || true
  else
    sqlite_box "SELECT seq, entity, entity_key, op FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER);" || true
  fi

  if [ "$APPLY" -ne 1 ]; then
    info ""
    info "Dry-run only: no database changes were made. Re-run with --apply to write."
  else
    backup="$DB.repair-missing-session-directory.seq-$SEQ.$(date +%Y%m%d%H%M%S).bak"
    cp "$DB" "$backup"
    info ""
    info "Backup created: $backup"

    sqlite3 -batch "$DB" "BEGIN;
UPDATE sync_mutations
SET payload = $patched_payload_sql
WHERE seq = CAST('$SEQ_SQL' AS INTEGER)
  AND entity = 'observation'
  AND op = 'upsert'
  AND ifnull(json_extract(payload, '$.project'), '') = '$PROJECT_SQL';
COMMIT;"

    missing_after_apply=$(sqlite_scalar "SELECT $(json_required_count_sql) FROM sync_mutations WHERE seq = CAST('$SEQ_SQL' AS INTEGER) AND entity = 'observation' AND op = 'upsert';")
    [ "$missing_after_apply" = "0" ] || die "verification failed: observation payload still has empty required fields"
    info "Apply complete: observation payload required fields verified."
  fi
fi
}

if [ "$ALL" -eq 1 ]; then
  tmp=${TMPDIR:-/tmp}/engram-doctor-output.$$.txt
  trap 'rm -f "$tmp"' EXIT HUP INT TERM
  iteration=1
  while [ "$iteration" -le "$MAX_ITERATIONS" ]; do
    info ""
    info "Loop iteration $iteration/$MAX_ITERATIONS: running doctor..."
    if ! target=$(parse_target_from_doctor "$tmp"); then
      info ""
      info "Doctor output summary:"
      cat "$tmp" || true
      info ""
      if handle_no_supported_blocker "loop"; then
        continue
      fi
      exit 0
    fi

    SEQ=${target%% *}
    ENTITY=${target#* }
    info "Supported blocker found: seq=$SEQ entity=$ENTITY op=upsert"
    repair_current_target
    iteration=$((iteration + 1))
  done

  die "--all stopped after --max $MAX_ITERATIONS repairs; rerun doctor or increase --max if supported blockers remain"
else
  repair_current_target
fi

info ""
info "Next commands:"
info "  engram cloud upgrade doctor --project $PROJECT"
info "  engram cloud upgrade repair --project $PROJECT --dry-run"
info "  engram cloud upgrade repair --project $PROJECT --apply"
info "  engram sync --cloud --project $PROJECT"

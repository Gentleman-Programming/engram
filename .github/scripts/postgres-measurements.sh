#!/usr/bin/env bash
# CI-only, disposable measurement databases. Never connect this helper to user data.
set -euo pipefail
if [[ "${1:-}" == --help && $# == 1 ]]; then
  echo 'Usage: bash .github/scripts/postgres-measurements.sh (GitHub Actions only)'
  exit 0
fi
[[ $# == 0 && "${GITHUB_ACTIONS:-}" == true ]] || { echo 'Requires GitHub Actions and no arguments' >&2; exit 1; }
: "${RUNNER_TEMP:?job-owned scratch required}"
: "${GITHUB_RUN_ID:?run identity required}"
: "${GITHUB_RUN_ATTEMPT:?attempt identity required}"
[[ "$GITHUB_RUN_ID" =~ ^[0-9]+$ && "$GITHUB_RUN_ATTEMPT" =~ ^[0-9]+$ ]]
image='postgres:18.6-alpine@sha256:10a5c69a83035ab5239f5620d708520e8b28f5aa1f2b20143e6024532edadfb5'
ids=()
cleanup() {
  local status=$?
  trap - EXIT
  for id in "${ids[@]:-}"; do
    [[ -n "$id" ]] || continue
    if ! docker rm -fv "$id"; then
      if (( status == 0 )); then status=1; fi
    fi
  done
  exit "$status"
}
trap cleanup EXIT
start() {
  local role=$1 port=$2 id ready=false
  id=$(docker run --detach --name "engram-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}-$role" \
    --publish "127.0.0.1:$port:5432" --tmpfs /var/lib/postgresql \
    -e "POSTGRES_USER=$role" -e "POSTGRES_DB=${role}_measurement" \
    -e POSTGRES_HOST_AUTH_METHOD=trust "$image" \
    -c shared_preload_libraries=pg_stat_statements -c compute_query_id=on)
  [[ "$id" =~ ^[a-f0-9]{64}$ ]] || { echo 'Invalid owned container ID' >&2; return 1; }
  ids+=("$id")
  for ((attempt=0; attempt<60; attempt++)); do
    if docker exec "$id" pg_isready -h 127.0.0.1 -U "$role" -d "${role}_measurement" -t 1; then
      ready=true
      break
    fi
    sleep 1
  done
  [[ "$ready" == true ]] || { echo "Fixture readiness failed: $role" >&2; return 1; }
  docker exec "$id" psql -U "$role" -d "${role}_measurement" -v ON_ERROR_STOP=1 \
    -c 'CREATE EXTENSION pg_stat_statements' -c 'SHOW server_version'
}
# Separate containers prevent either package contaminating the other's statistics.
start engram1832 15432
start engram1831 15431
unset CLOUDSTORE_TEST_DSN
export ENGRAM_DASHBOARD_BENCH_DSN='postgres://engram1832@127.0.0.1:15432/engram1832_measurement?sslmode=disable'
export ENGRAM_PAUSE_BENCH_DSN='postgres://engram1831@127.0.0.1:15431/engram1831_measurement?sslmode=disable'
set +e
go test -json -count=1 -timeout=10m ./internal/cloud/cloudstore ./internal/cloud/cloudserver \
  -run '^(TestDashboardReadModelPostgresCounts|TestDashboardReadModelPostgresControlsAndRecreation|TestPausePGPullSnapshotPagination|TestMutationPausePostgresQueryCount)$' \
  | tee "$RUNNER_TEMP/postgres-measurements.json"
statuses=("${PIPESTATUS[@]}")
set -e
printf 'measurement_go_exit=%s tee_exit=%s\n' "${statuses[0]}" "${statuses[1]}"
if (( statuses[0] != 0 )); then exit "${statuses[0]}"; fi
if (( statuses[1] != 0 )); then exit "${statuses[1]}"; fi
node .github/scripts/prerequisite-coverage.mjs postgres-measurements "$RUNNER_TEMP/postgres-measurements.json" "${statuses[0]}"

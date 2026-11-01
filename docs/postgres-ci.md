# PostgreSQL CI integration scope

The candidate **PostgreSQL Integration Tests** job runs existing tests against a
fresh, disposable `postgres:16-alpine` service on pull requests, merge groups and
main pushes. It does not change required checks, production persistence, or the
existing unit/E2E/plugin jobs. Hosted execution remains unproven until this
candidate is published and its job runs successfully.

## Scope and evidence

The job runs these packages with Go 1.26.10, Node.js 24 and a URL-style
`CLOUDSTORE_TEST_DSN`, without `-short` or extra build tags:

- `internal/cloud/cloudstore`: migrations, identity/token/session persistence,
  project controls, audit, prompt claims and dashboard read models.
- `internal/cloud/cloudserver`: database-backed HTTP session and prompt claims.
- `cmd/engram`: runtime wiring and authenticated cloud integration.

Most helpers create and drop a per-test schema. `openTestCloudStore` instead uses
the default schema and only closes its connection. **Never use a production or
shared database.** The whole CI service is discarded after the job.

Service health checks establish readiness; `psql` verifies the configured
connection and prints the actual server version. Go initialization/migration/test
errors fail the job. `pipefail` preserves Go failures through `tee`. Logs contain
the exact checkout SHA, tool versions and JSON test events. The evidence checker
requires successful package completion and an existing database sentinel test
in each package, and rejects DSN-related skip output or failed tests.

Unrelated environment/platform skips remain allowed. After the ordinary PG16
suite, `bash .github/scripts/postgres-measurements.sh` provisions two separate,
disposable PostgreSQL 18.6 containers, pinned by image digest and published only
on loopback ports 15432/15431. The existing exact DSN/version/extension guards
remain unchanged. Database-wide statistics are exclusive to each fixture, and
cleanup removes only container IDs created by this invocation, including volumes.
Go failures and cleanup failures fail the job; the existing 20-minute job ceiling
and ten-minute package budgets are unchanged.

The eleven measurement leaf cases assert state, query/build counts, concurrency,
pagination and rejection snapshots—not wall-clock performance. The checker
`node .github/scripts/prerequisite-coverage.mjs postgres-measurements EVENTS.json GO_EXIT`
requires each named case to run and pass once, its package to finish, and Go exit
zero. Missing/skipped/duplicate/failed/unfinished/malformed evidence is rejected.
Use `--help` on either helper for usage. The bootstrap is GitHub-Actions-only;
local owned measurement reproduction remains in the two benchmark documents.
Hosted fixture execution is pending until the candidate's actual job passes.
This job is neither a benchmark baseline nor proof of a PostgreSQL support matrix. PostgreSQL 16 is
aligned with the repository's cloud Compose examples; its floating image tag
means exact server versions must be taken from each run's logs.

## Local reproduction (POSIX shell)

Requires Docker, `psql`, Go 1.26.10 and Node.js 24. Run from the repository root.
Use an unused local port and container name. All credentials below are disposable
local fixture values, not production secrets.

```sh
set -euo pipefail
container="engram-postgres-ci-$$"
evidence="$(mktemp -p . postgres-ci-XXXXXX.json)"
cleanup() {
  docker rm -fv "$container"
  rm -f "$evidence"
}
trap cleanup EXIT
docker run --detach --name "$container" \
  -e POSTGRES_USER=engram_test -e POSTGRES_PASSWORD=disposable_test_password \
  -e POSTGRES_DB=engram_test -p 127.0.0.1:55432:5432 \
  --health-cmd 'pg_isready -U engram_test -d engram_test' \
  --health-interval 5s --health-timeout 5s --health-retries 12 postgres:16-alpine
for attempt in $(seq 1 60); do
  state="$(docker inspect --format '{{.State.Health.Status}}' "$container")"
  case "$state" in
    healthy) break ;;
    unhealthy) echo 'PostgreSQL readiness failed' >&2; exit 1 ;;
  esac
  sleep 2
done
test "$state" = healthy
export CLOUDSTORE_TEST_DSN='postgres://engram_test:disposable_test_password@127.0.0.1:55432/engram_test?sslmode=disable'
export GOTOOLCHAIN=local
unset ENGRAM_DASHBOARD_BENCH_DSN ENGRAM_PAUSE_BENCH_DSN
git rev-parse HEAD
go version
node --version
psql "$CLOUDSTORE_TEST_DSN" -v ON_ERROR_STOP=1 -c 'SELECT version();'
go test -json -count=1 -timeout=10m ./internal/cloud/cloudstore ./internal/cloud/cloudserver ./cmd/engram | tee "$evidence"
node .github/scripts/postgres-ci-evidence.mjs "$evidence"
```

For the migration fixture cleanup regression alone, use the same disposable
service and `CLOUDSTORE_TEST_DSN`, then run:

```sh
go test ./internal/cloud/cloudstore -run '^(TestMigrateAcceptsModernCloudChunksWithoutLegacyColumns|TestMigrationFixtureCleanup)$' -count=1 -v -timeout=120s
```

The regression checks schema absence after both success and an intentional failure
after schema creation. It launches the test binary in a subprocess so the expected
failure does not fail the parent suite. `ENGRAM_TEST_MIGRATION_FIXTURE_FAILURE` is
an internal subprocess test control; do not set it for ordinary test runs. Cleanup
errors fail the test with bounded messages that omit driver connection details.

Save the console output before exiting if evidence is needed; the trap removes
the container, its anonymous data volume and the temporary JSON file. No named
volume or host data directory is mounted.
Run `node --test .github/scripts/postgres-ci.test.mjs` for deterministic checker
and workflow contract tests; those tests do not execute PostgreSQL or hosted CI.

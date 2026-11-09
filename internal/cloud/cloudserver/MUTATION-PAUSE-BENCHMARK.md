# Mutation pause measurement (#1831)

Baseline: `7404e0b7c27cb056c86c085ca5ea1b78af346f30`; production code is unchanged.
This characterizes repeated control reads, not an approved optimization or pause-policy change.

## Isolated runner

Use a **fresh, dedicated** project-local PostgreSQL cluster, never a user/production DSN.
The opt-in fixture accepts only PG 18.6 and the exact loopback DSN below.
From the repository root, with native PostgreSQL binaries on PATH:

```bash
mkdir .git/issue-1831-postgres # must not already exist
initdb -D .git/issue-1831-postgres/data -U engram1831 -A trust --no-locale -E UTF8
pg_ctl -D .git/issue-1831-postgres/data -l .git/issue-1831-postgres/server.log -o '-h 127.0.0.1 -p 15431 -c shared_preload_libraries=pg_stat_statements -c compute_query_id=on' -w start
psql 'postgres://engram1831@127.0.0.1:15431/postgres?sslmode=disable' -v ON_ERROR_STOP=1 -c 'CREATE DATABASE engram1831_measurement'
export ENGRAM_PAUSE_BENCH_DSN='postgres://engram1831@127.0.0.1:15431/engram1831_measurement?sslmode=disable'
psql "$ENGRAM_PAUSE_BENCH_DSN" -v ON_ERROR_STOP=1 -c 'CREATE EXTENSION pg_stat_statements'
CLOUDSTORE_TEST_DSN='' go test ./internal/cloud/cloudserver -run '^Test(PauseLookup|PausePGPullSnapshotPagination|MutationPausePostgresQueryCount)' -count=1 -v
CLOUDSTORE_TEST_DSN='' go test ./internal/cloud/cloudserver -run '^$' -bench '^BenchmarkMutationPausePostgres$' -benchtime=10x -count=3 -benchmem
pg_ctl -D .git/issue-1831-postgres/data -m fast -w stop
```

On later starts, repeat the same `-o` options: `pg_ctl start` alone defaults to port 5432 without the preload.
Do not run other pause readers concurrently: SQL statistics deltas are database-wide.
Each case creates its own random schema and checks close/drop cleanup; no global statistics reset.
Loopback trust authentication is only for this disposable synthetic cluster, not a service configuration.

## Observed baseline

Windows/amd64, Go 1.26.4, PostgreSQL 18.6, AMD Ryzen 5 5600, benchmark suffix `-12`.
`fsync`, `full_page_writes`, `synchronous_commit` on; shared buffers 128 MiB; statistics track `top`.
The focused tests passed: **100 control SELECTs/request** for B=100 and D=1, 10, 100.
Pausing the first project caused one SELECT and HTTP 409; a complete 25-row-paged public mutation stream remained identical.
Before/after comparisons include sequence metadata, all mutation identities and payloads; a 200-row accepted-append probe protects against first-page truncation.
All nine benchmark samples passed their **100 control SELECTs/op** assertions (3 samples x 10 timed iterations per D).
D=100 displayed samples: 72.613/56.279/53.755 ms/op, 3,438,704/3,437,083/3,436,507 B/op, 59,818/59,816/59,810 allocs/op.
These are preliminary samples, not statistical evidence of a speedup or production latency.

## Measurement limits

Timing includes in-process HTTP handling, synthetic bearer/project authorization, pgx loopback and real insertion.
It excludes schema/migrations, statistics reads and response assertions; it does not isolate pause-query latency.
Connections/store are warm; payloads repeat, entities/chunks replay, and the mutation journal grows 100 rows/request.
The fake scripted second-read pause test is deterministic handler characterization, not PostgreSQL concurrency proof.
A request-scoped cache could change later pause/error observation; no such change or consistency policy is selected.
The normal cloudserver suite passed with both DB opt-ins unset; these PostgreSQL tests deliberately skip without their dedicated DSN.

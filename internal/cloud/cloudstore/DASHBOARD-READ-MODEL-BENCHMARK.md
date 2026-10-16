# Dashboard read-model measurements (#1832)

Measurement-only baseline: `fc438498198c00a7b5402014ce7f9e58f11a33e1`. No production optimization, freshness fix, projection, index, migration or authorization change.

## Owned runner

Use only the new project-owned PostgreSQL18.6 cluster at `.git/issue-1832-postgres/data`, not an inherited `CLOUDSTORE_TEST_DSN`, user database or the #1831 fixture. The fixture rejects every URI except:

```sh
export ENGRAM_DASHBOARD_BENCH_DSN='postgres://engram1832@127.0.0.1:15432/engram1832_measurement?sslmode=disable'
export CLOUDSTORE_TEST_DSN='' ENGRAM_PAUSE_BENCH_DSN=''
# From the repository root; do not initialize over existing data.
pg_ctl -D "$PWD/.git/issue-1832-postgres/data" -l "$PWD/.git/issue-1832-postgres/server.log" -o '-h 127.0.0.1 -p 15432 -c shared_preload_libraries=pg_stat_statements -c compute_query_id=on' -w start
# From the isolated issue workspace:
go test ./internal/cloud/cloudstore -run '^Test(DashboardCacheCharacterization|DashboardReadModelPostgres)' -count=1 -timeout=120s -v
go test ./internal/cloud/cloudstore -run '^$' -bench '^BenchmarkDashboardReadModelPostgres$' -benchtime=3x -count=3 -benchmem -timeout=300s
```

The role/database are `engram1832`/`engram1832_measurement`; the extension was created only there. Each fixture uses a random schema, normal Store migrations and checked schema/connection cleanup. Query deltas require exclusive readers on this owned database; no global statistics reset is used. Repeat the exact startup flags on every restart. Stop only this owned cluster after verification.

## Workload and boundaries

- Two history axes: 10/100/1000 extra empty, API-valid chunks or repeated session upserts. The final live entities remain one session and one observation with fixed identity/content. Historical `Chunks` counters deliberately vary with chunk history; this is not an identical full dashboard response across configurations. Actual chunk/mutation row counts and UTF-8 JSON payload-text bytes are logged, not database/wire size.
- All seeds use `WriteChunk`/`InsertMutationBatch`. Observation recreation uses explicit `scope: project`; legacy chunk acceptance is not evidence that scope-less public mutation upserts are valid.
- The existing test-local loader wraps the real SQL/folding build. Modes: warm, cold, after replayed chunk write, after control upsert and four forced overlapping cold callers. One concurrent operation is a four-reader group, not one request. It does not estimate natural overlap frequency.
- Read time is a public `ListProjects` Store query, not HTTP/authentication/rendering/network latency. Fixture setup, ingestion, cache-reset/write/control preparation and statistics sampling are excluded from benchmark time/allocations. Thread/channel/barrier overhead is included in the concurrent mode. Build time is the sum of real loader durations, excluding the artificial barrier wait; overlapping build durations are additive, not wall-clock elapsed time.
- The sampler counts the three concrete read-model SELECT shapes through actual `pg_stat_statements` deltas. It is not total SQL during fixture setup, writes or control changes. Warm cache still performs credential filtering.
- `sample-p95-ns` is empirical nearest-rank over the logged `b.N` operation samples. At `3x`, p95 is the maximum of three observations, not a stable population p95. Three repetitions are not production evidence or a speedup comparison.
- Public detail equality checks pin before/after warm/write/control/concurrent reads. Empty registration and observation delete/recreation are exercised separately. This is not complete public mutation-stream persistence proof or real PostgreSQL invalidation-during-build reproduction.

## Evidence status

The five controlled-loader tests passed before this PG slice and reproduce two equivalent cold builds and old publication after invalidation. These are baseline characterizations, not desired new freshness rules. Their earlier `-race` invocation timed out at30s with no test outcome; it was not automatically retried or treated as a pass.

The first PG run (job57) passed six history/count configurations but failed the recreation fixture because an upsert omitted required scope. That is a fixture RED, not a production regression. Scope-only correction passed the non-skipped focused run58 (3.931s); default affected-suite60 passed1.963s with PG opt-ins blank. No meaningful production RED applies because production behavior is unchanged.

Run59 passed58.344s: 90 final benchmark reports (30 configurations ×3 repeats), each covering3 timed operations, for270 timed benchmark operations total. A concurrent operation is a four-reader group. Runner: Go1.26.4/windows/amd64, Ryzen5 5600, suffix12, PG18.6, fsync/full_page_writes/synchronous_commit on, shared_buffers128MiB. Calibration logs with1 operation are excluded from these totals.

| Public read mode | Observed SELECTs/op | Actual builds/op |
|---|---:|---:|
| Warm | 0 | 0 |
| Cold, after replayed write, after control upsert | 3 | 1 |
| Four forced cold readers (one group) | 12 | 4 |

| Cold history axis | H10 ns/op means, range (ms) | H1000 ns/op means, range (ms) |
|---|---:|---:|
| Extra empty chunks | 0.394–0.540 | 5.373–7.723 |
| Extra session mutations | 0.509–0.796 | 18.098–19.661 |

These are ranges of three measured per-operation means, not confidence intervals or production latency. H1000 mutation fixtures logged2 chunks/1002 journal rows/472712 JSON payload-text bytes; live session/observation counts remain1/1. Mutation/H1000 concurrent groups measured53.107–85.386ms and approximately19.5–19.9MB/op; do not compare a four-reader group directly to one request as a claimed speedup.

Warm means were0.967–2.067µs/op in this short loop, but `sample-p95-ns` was reported as0: do not interpret that as zero latency or resolved microsecond percentile precision. Build-duration and read timing are separate short-loop measurements; small discrepancies are not a decomposition of all request costs. Real-world miss rate, representative histories and steady-state p95 remain unmeasured.

The first independent reviewer mv6bu0le-f-molr failed without a verdict. After explicit user authorization, a fresh independent reviewer mv6tgdev-g-k658 verified the focused characterization and real-PG tests: exit0, PASS3.743s, no skips. The wrong-port15433 synthetic DSN guard returned expected exit1 with `measurement DSN is not the exact owned fixture` before connection/schema creation. Owned custom schemas were0 before and after both checks; user-table/sequence/materialized-view inventory remained empty. The reviewer identified the report/operation-count ambiguity corrected above. Benchmark timings remain producer evidence, not independently rerun measurements; the documentation correction was structurally checked by the parent, not re-reviewed. The earlier race timeout remains inconclusive. Before publication, native assessments were unassessable while the follow-up files were untracked; no unrelated staging workaround or native approval/receipt is claimed. Any subsequent slice assessment is recorded in its PR, not retroactively counted as measurement proof.

Final owned cleanup63 passed after this review:0 non-system synthetic schemas and the server stopped; earlier cleanup61 is retained as historical evidence. Parent dirty files and production source were untouched. No delivery was authorized during measurement execution. Subsequent authorization integrated characterization PR #1848 as `9f9fcb9176569d3461803e8b5e7774a3762e03d1`; this PostgreSQL/guide follow-up starts there, with unchanged production cloud/dashboard code. Publication does not authorize merging this follow-up or optimizing production.

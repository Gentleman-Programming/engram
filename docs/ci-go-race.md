# Go race CI (#2018)

## Candidate scope

The candidate **Go Race Tests** job runs
`go test -race -timeout=20m ./...` independently on `ubuntu-24.04`, with
Go 1.26.10, `CGO_ENABLED=1` and `CC=gcc`.
Pinned Node.js 24 is provisioned before the suite for the owned Core lifecycle tests;
local reproduction needs the same Node prerequisite, not the runner's ambient version.
The runner must provide GCC and `/usr/bin/time`; compiler preflight failures
fail the job. Existing unit, E2E and plugin suites remain unchanged.
The race command uses the default build tags: it does not run tests requiring
the `e2e` tag, Windows-only tests, or tests skipped by their existing prerequisites.
It does not filter or exclude packages or weaken assertions.

The job inherits CI's PR, merge-group and main-push triggers and concurrency.
Its name is initially **not a required context**; this change does not alter
repository rulesets. Failure still fails the job: there is no
`continue-on-error` or successful fallback.

## Race-only cache refresh (#2192)

Race disables setup-go's implicit cache and gives one explicit restore/save pair
ownership of `GOMODCACHE` and `GOCACHE`. Both actions are pinned to canonical
`actions/cache` **v6.1.0**, commit
`55cc8345863c7cc4c66a329aec7e433d2d1c52a9` (the workflow's `# v6` comment
retains the existing pin-check format). Their Node 24 restore-only/save-only
entrypoints have no automatic post-save hook. Node 24 requires a compatible
managed runner; upstream's runner >=2.327.1 guidance is worded for v5.
Pinned-source inspection is not fresh hosted runner compatibility proof.

Before restore, the job validates nonempty scalar inputs, absolute cache paths,
Go 1.26.10 linux/amd64 and CGO=1. Its compatibility descriptor binds runner
OS/architecture, `ImageOS` and `ImageVersion`, exact Go/target, both root
`go.mod` and `go.sum`, CGO, race mode and the actual `CC` driver: resolved
executable path, SHA-256, full version, target and full version-text hash.
The compiler identity is logged. A NUL-delimited descriptor is hashed into a
bounded compatibility prefix; the tested `github.sha` extends that prefix into
the primary key. Both actions consume the same validated, precomputed paths
and primary key. Restore uses only the complete compatible prefix, with no
legacy-cache migration, broad fallback or cross-OS archive reuse.

Only a successful push to `refs/heads/main` in
`Gentleman-Programming/engram` may execute save, and only without an exact hit.
PRs and merge groups never execute this save step. This relies on GitHub's
ref-scoped provider isolation and trusted main, not key secrecy or a claim
that a YAML condition authenticates every PR runtime cache capability.
No privileged trigger, write-token permission or cache-mode override is added.

Restore reports `cache-hit='true'` for an exact hit, `'false'` for a prefix
hit, and leaves it unset on an ordinary miss. Feature unavailability reports
`'false'` and may omit the primary-key output, which is why save uses the
precomputed key instead. Misses and ordinary unavailable-cache paths do not
condition or narrow the full suite; Go failures remain failures. Input validation
and some restore errors can fail preparation: not every cache error is nonfatal.
Save-only does not automatically suppress prior exact hits; the explicit guard
provides that rule. Reservation collisions warn and return no new cache
(upstream returns `-1`); immutable entries are not overwritten. There is no
workflow retry. A successful save step alone is not evidence that an archive
was stored; inspect the action diagnostics. Upstream transport retries are
separate from workflow retries.

Local isolated compile-only measurements showed warm-cache benefit (store race
compilation 92.521s to 7.287s); they did not execute this Actions cache service
or measure hosted full-suite savings. Storage, eviction and transfer costs may
outweigh saved compilation. Hosted net benefit remains **unproven**. After
separate delivery/experiment authorization, compare a cold trusted-main save,
a later compatible main refresh, and a PR restore with no save, including
archive transfer and job elapsed time. Revert only this plumbing if net negative;
do not trim tests, raise budgets or alter event cadence to manufacture savings.

Local contract checks inspect wiring and policy, not hosted integration:
`node --test .github/scripts/ci-technical-contracts.test.mjs .github/scripts/ci-unit-node.test.mjs`.
Existing suites, required contexts, concurrency, checkout provenance, permissions
and the 20-minute per-binary / 30-minute job budgets remain unchanged.

## Reproduce on Linux

Use Go 1.26.10, GCC, GNU time and the repository's existing test prerequisites
(see [contributor verification](../CONTRIBUTING.md#focused-verification-map)):

```sh
git rev-parse HEAD
go version
gcc --version
CGO_ENABLED=1 CC=gcc GOTOOLCHAIN=local /usr/bin/time -p go test -race -timeout=20m ./...
```

The printed revision identifies the tested checkout. On pull requests this is
normally GitHub's synthetic merge revision, not merely the fork head.
GNU time writes `real`, `user` and `sys` seconds to stderr even on test failure
and returns the command's failure status. The explicit 20-minute timeout applies
to each race-instrumented test binary; it does not change the uninstrumented
suites' defaults. The separate 30-minute job budget includes setup, downloads,
compilation and test execution.

## Measurements and budget rationale

The [first hosted run](https://github.com/Gentleman-Programming/engram/actions/runs/40142405437)
failed at head `4d53464159f3a6c59d0b064bf18a783f4de99180`, testing synthetic merge
`ef88b0cd76c31312d4b114e2d6ac545f2974d85a` on Ubuntu 24.04.5
(image `20261012.334.1`), Go 1.26.10 and GCC 13.3.0. Its race command took
778.29 seconds and the job took 13m14s. It exposed a test-only SQLite varargs
`checkptr` failure and exhausted Go's default 10-minute store-package timeout;
it did not exhaust the 30-minute job limit. This is failure evidence, not a
successful full-suite baseline.

After correcting the test helper to use libc-owned TLS storage and `libc.VaList`
without disabling checkptr, a local WSL2 Ubuntu 24.04.5 run with the same Go/GCC
versions executed:

```sh
CGO_ENABLED=1 CC=gcc GOTOOLCHAIN=local go test -race ./internal/store -count=1 -json -timeout=20m
```

Its source was the same base tree plus the two reviewed helper/startup test
changes included in this candidate. Store passed in **843.887 seconds (14m04s)**;
command wall time was 857.89 seconds. There were 961 passing top-level tests
(2,131 passing events including subtests), no failures or race warnings, and
empty command stderr. Three top-level skips were existing subprocess-only
helper entrypoints; their corresponding parent tests passed. Source manifests
were unchanged before/after execution. The slowest top-level test took 35.67s;
the prompts test active at the hosted timeout passed in 0.99s here.

The 20-minute race-only per-binary budget provides about 42% margin over this
local store measurement. This supports a candidate budget, not identical hosted
performance or proof that all order-dependent contention is absent. No packages
were omitted and no assertions or production synchronization were changed.

## Successful hosted full-suite baseline

[Hosted run 40147453688](https://github.com/Gentleman-Programming/engram/actions/runs/40147453688)
succeeded on 2026-10-25 at PR head
`b64205b3cfe9f4377726e097bc96384e57f46306`, testing synthetic checkout
`8f5373e2d027f521b3e40b330cd861cf3ce6082e`. The runner used Ubuntu 24.04
image `20261012.334.1`, Go 1.26.10 linux/amd64, GCC 13.3.0 and CGO enabled.

The **Go Race Tests job took 19m04s**. GNU time reported real 1128.50s,
user 2675.69s and sys 197.98s. The slowest package, `internal/store`, passed
in 915.533s (15m15.533s), within the explicit 20-minute per-binary timeout.
No packages failed and no `WARNING: DATA RACE` appeared in the retained log.

The **30-minute job timeout remains provisional**: this is one successful
representative run, not a stable runtime guarantee or a release verdict.
The non-verbose log reports package completions, not an individual-test skip
inventory; the default-tag, platform and prerequisite boundaries above remain.
Keep classifying future failures and reviewing observed margins before changing
budgets or execution scope.

Classify results separately:

- **Race reports:** `WARNING: DATA RACE`, conflicting stacks and package/test.
  Investigate production races separately; do not add synchronization here.
- **Fixture/assertion failures:** retain the original output and assertions;
  ordinary failures alone are not proof of a production race.
- **Resources/timeouts/tooling:** distinguish Go's per-binary timeout from
  the Actions job limit, out-of-memory termination and missing prerequisites.
  A cancelled or timed-out run is not a clean baseline; time output may be absent.

Do not silently narrow the suite or omit failing/expensive packages.
If the full run cannot be delivered within a realistic budget, retain the
evidence on #2018 and obtain maintainer agreement on staged scope first.
Required-check activation remains a separate maintainer decision.

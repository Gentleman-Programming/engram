# Go race CI (#2018)

## Candidate scope

The candidate **Go Race Tests** job runs `go test -race ./...` independently
on `ubuntu-24.04`, with Go 1.26.10, `CGO_ENABLED=1` and `CC=gcc`.
The runner must provide GCC and `/usr/bin/time`; compiler preflight failures
fail the job. Existing unit, E2E and plugin suites remain unchanged.
The race command uses the default build tags: it does not run tests requiring
the `e2e` tag, Windows-only tests, or tests skipped by their existing prerequisites.
It does not filter or exclude packages or weaken assertions.

The job inherits CI's PR, merge-group and main-push triggers and concurrency.
Its name is initially **not a required context**; this change does not alter
repository rulesets. Failure still fails the job: there is no
`continue-on-error` or successful fallback.

## Reproduce on Linux

Use Go 1.26.10, GCC, GNU time and the repository's existing test prerequisites
(see [contributor verification](../CONTRIBUTING.md#focused-verification-map)):

```sh
git rev-parse HEAD
go version
gcc --version
CGO_ENABLED=1 CC=gcc GOTOOLCHAIN=local /usr/bin/time -p go test -race ./...
```

The printed revision identifies the tested checkout. On pull requests this is
normally GitHub's synthetic merge revision, not merely the fork head.
GNU time writes `real`, `user` and `sys` seconds to stderr even on test failure
and returns the command's failure status. The existing Go default timeout is
10 minutes per test binary; the separate job budget includes setup, downloads,
compilation and test execution.

## Hosted baseline: pending

The initial **30-minute job timeout is provisional**, not a measured baseline.
No representative hosted execution or race-free result is claimed yet.
After an authorized publication, record the run URL, exact tested revision,
runner image, Go/compiler versions, job wall duration and GNU-time output.
Inspect failures and skips, then choose the final timeout with explicit margin.
A local structural test pass is not hosted or race execution evidence.

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

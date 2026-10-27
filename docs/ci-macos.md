# Native macOS baseline (#2121)

## Native adapter lane (#2150, macOS portion only)

**Native macOS ARM64 Adapter Tests** is a separate, initially non-required
`macos-adapter-tests` job on `macos-15`. It inherits ordinary PR, merge-group
and main events/concurrency without changing the Go foundation or six required
App15368 contexts, queue, security/version/integrity guards or Race cadence.
Non-required does not mean ignored failures.

The lane verifies actual Darwin ARM64 Go/Node execution, records SHA/image/tool
versions, and requires Go **1.26.10**, Node **24**, npm, the full source checkout,
contract JSON and locked local TypeBox **1.3.34**. It installs only existing Pi
test dependencies with `npm ci --ignore-scripts --legacy-peer-deps`, prepares
repository Go modules, and builds the existing Pi test server:

```bash
go mod download
go build -mod=readonly -o "$RUNNER_TEMP/pi-test-server" ./plugin/pi/test/support/real-server
```

From `plugin/pi`, run the existing `npm test` script with CLI-only TAP reporting:
`ENGRAM_PI_TEST_SERVER_BINARY="$RUNNER_TEMP/pi-test-server" node --test --test-reporter=tap --test-concurrency=1 test/*.test.mjs`.
The contract test pins equivalence to the package script. This also executes the packaged positive
workflow case. Other existing fixtures keep their own build/startup deadlines.
From the repository root, reuse the current OpenCode CI selection unchanged:

```bash
node --test --test-reporter=tap plugin/opencode/engram.test.mjs plugin/opencode/engram.v2.test.mjs plugin/opencode/engram.v2.real-server.test.mjs
```

The additional OpenCode `engram.test.mts` is not in that selected CI command.
Each test retains its own store/server/process cleanup; the lane uses a physical
private temporary HOME/temp/fallback store, preserved Go cache locations and
sanitized adapter settings. Shared prebuilt bytes never mean shared servers.
No #1903/PR #1906 fixture/PATH work is copied or integrated.

The **15-minute outer**, **2-minute Pi fixture build**, **5-minute Pi** and
**5-minute OpenCode** step ceilings are provisional containment, not measured
Darwin timing guarantees. Existing fixture deadlines stay unchanged. Timeout,
test/build/setup failure or cancellation is not successful coverage; no automatic
budget increase, blanket skip or continue-on-error is permitted.

TAP reporting is selected only through CLI flags, never inherited NODE_OPTIONS.
The inline reporter retains pass/fail/skip counts and exact top-level skip
identities/reasons, reconciles counters with all nested execution records,
checks completed plans and required fixture cases,
and also requires both actual test-step outcomes to be success. Missing,
malformed or truncated execution output and unexpected skips fail completeness.
The sole accepted outer-suite skip is the explicitly opt-in installed Pi host
journey. Mandatory Go/contract/TypeBox/source-server absence is incomplete.
Package-boundary tests deliberately assert child failures/skips; those are not
new native positive workflows. The reporter emits JSON to the log/job summary;
cancellation or outer timeout can prevent that report, never imply success.

First normal PR [run 40456433724](https://github.com/Gentleman-Programming/engram/actions/runs/40456433724), attempt 1,
head `493992f9`, checkout `9b59e22c`, executed Pi on Darwin ARM64: 615 pass /
23 fail / 1 skip in 106.57s; OpenCode was skipped. Inherited NODE_OPTIONS
reporter flags collided with child fixtures\u0027 explicit TAP flags. CLI-only selection
fixed that wiring. Follow-up run 40459952974 (head `6bf00647`) executed Pi
638 pass / 1 opt-in skip and OpenCode 273 pass / 0 skip; only the summary failed
by rejecting a legitimate repeated parameterized name. That guard is corrected;
new native job execution remains pending, not established by replaying its TAP.
No adapter execution on Darwin is established by local Node structural checks.
Foundation PR run 40443462722 (head d9f0ab3baab017d8245f46b4381c8deccd48267f),
merge-group 40445951871 and main 40446353609 (both at
51bcdba8ffc930bd7b65f4c99446e5cabcc115fe, attempt 1) had successful native
foundation jobs; they do not execute this adapter candidate. Observe one normal
PR, merge-group and main execution of this new lane after authorized delivery,
then explicitly choose promotion, focused fix or defer under #2020. Do not
dispatch/rerun to manufacture evidence or promote automatically.

Windows #2155/PR #2156/#2159, Darwin amd64, installed-host lifecycle, selected
Core/Pi artifacts #2055, distributed-byte smoke #2122 and release eligibility
remain separate. Mac-only completion cannot close the whole of #2150.

## Candidate, not hosted qualification

**Native macOS ARM64 Tests** adds one initially non-required job to CI on
`macos-15`. The [official runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners#standard-github-hosted-runners-for-public-repositories)
lists this standard label as ARM64 (3 M1 CPUs, 7 GB RAM); `macos-15-intel`
is distinct. The label pins the OS family, not an immutable image revision.
Record the actual image version, macOS version and architecture on every run.

The job inherits CI's pull-request, merge-group and main-push events and
concurrency. It has read-only contents permission, no persisted checkout
credentials, no proof wait and no dependency on other jobs. Advisory means
**non-required**, not ignored failures. Existing jobs, required contexts,
merge queue, Windows pause and Linux Race cadence/budgets are unchanged.

No hosted Darwin execution is established by this change's structural checks.
Cross-built binaries, Linux/Windows results and source fixtures are not native
execution, installed-host or distributed-artifact qualification.

## Toolchain, prerequisites and isolated state

Use repository Go **1.26.10**, `GOTOOLCHAIN=local`, Node **24**, the existing
SHA-pinned setup actions, Bash, Git, jq, curl and ordinary shell utilities.
The workflow records the checkout SHA, `uname -sm`, `sw_vers`, image identity,
Go host/target architecture, CGO setting and tool versions. It requires
Darwin/ARM64 for both Go and Node, not a cross-target build.

Both PATH jq and **`/usr/bin/jq`** must be executable. The existing
`plugin/claude_code_jq_compatibility_test.go` fixture defaults to that absolute
path; PATH discovery alone is insufficient. Its supported `ENGRAM_TEST_JQ16`
override requires actual jq 1.6, not an arbitrary installed jq. This job does
not install a delegate, fabricate a system path or claim genuine jq 1.6 coverage.
Absent `/usr/bin/jq` is an explicit preflight failure and incomplete coverage;
runner-label documentation does not prove that fixture prerequisite exists.

A private `/tmp/eg-mac.XXXXXX` root keeps Unix socket paths short. Resolve it
with `pwd -P` before deriving any fixture path, so HOME, TMPDIR/TMP/TEMP and
the fallback store share the physical `/private/tmp` spelling on macOS;
this uses the real filesystem, not path emulation. Record that physical root; symlink creation is checked.
Existing tests retain their own temporary homes, stores, listeners, processes
and cleanup. The disposable runner VM owns the outer root; no real user store
is opened and no unrelated process is killed. Go caches use their explicitly
recorded setup locations, not the isolated HOME. Before the suite, inherited
ENGRAM/GENTLE settings, PostgreSQL DSN, NODE_OPTIONS and GOFLAGS are removed;
only the private fallback data directory is reintroduced.

## Exact command and budgets

After setup, preflight and isolation, the workflow runs:

```bash
set -euo pipefail
/usr/bin/time -p go test -json -count=1 -timeout=10m ./... | tee "$RUNNER_TEMP/macos-native-tests.json"
```

There is no package filter, `-short`, blanket skip or native Race addition.
The **20-minute outer** ceiling includes checkout, cold setup/downloads,
compilation and reporting. The explicit **10-minute inner** timeout applies
to each test binary and matches Go's uninstrumented default, not the whole
suite. These are initial operational ceilings, **not native timing/SLO
guarantees**. Exhaustion remains failure; do not automatically increase budgets,
weaken assertions or drop expensive packages. Overlapping package times are
not additive wall time.

Pipefail preserves a failing test/tee exit. JSON is also retained in the run
log, with time's real/user/sys on stderr. A separate non-cancellation summary
step runs after ordinary failures without changing the test step's outcome.
It reports package and test/subtest event counts, exact skipped-test output
and classifications to the log and job summary. Missing, malformed or empty
execution evidence, test/package failures, mandatory prerequisite skips and
unclassified skips report **incomplete** and fail reporting too. Completeness also
requires the actual native-test step outcome to be `success`; terminal package
events alone cannot prove the suite finished. Every other outcome, including a
missing outcome, remains incomplete without changing counts or exclusions. Cancellation
or the outer job timeout may prevent reporting: absence is not success.

## Native scope and exclusions

| Existing owner | Native scope / honest omission |
| --- | --- |
| `internal/store/filesystem_policy_darwin_test.go` | Darwin adapter and filesystem-type classification; not real NFS/SMB mounts. |
| `internal/project/detect_test.go` | Canonical temporary paths, including /var versus /private/var comparisons. |
| `cmd/engram/migrate_copy_test.go`, store/MCP/Obsidian casing tests | Actual case-alias or case-distinct capability; list each unsupported-capability skip, never claim both executed. |
| CLI init / Obsidian symlink tests | Rejection/escape behavior; missing symlinks mean incomplete coverage. |
| `cmd/engram/serve_owned_test.go`, `serve_socket_test.go`, Unix server tests | Owned-process lifecycle and real socket cleanup; injected SIGTERM is not real OS signal delivery. |
| `internal/setup`, `internal/store/project_alias_test.go` | Config preservation/idempotence and temporary SQLite write/reopen/read persistence. |
| Windows-only files/runtime cases; Linux-only Claude socket subtest | Foreign-OS exclusions; build-tag-excluded files do not emit skip events. |
| DSN-gated cloud/CLI PostgreSQL integration | Explicitly excluded; includes owned dashboard/pause fixtures gated by ENGRAM_DASHBOARD_BENCH_DSN / ENGRAM_PAUSE_BENCH_DSN. No service-container PostgreSQL copy. |
| Pi real-schema capture needing locked TypeBox | Optional capture only; native Pi/OpenCode placement remains #2150. Missing Node for portable Core lifecycle is **not** optional. |
| Node <24 rejection case; child-only helper entrypoints; TUI entry without serialization | Explicit foreign-runtime/helper/not-applicable skips, not evidence of those alternate executions. |

Native tagged E2E, Race, PostgreSQL, standalone plugin suites, benchmarks and
Darwin amd64 execution are outside this foundation. Existing tests that run
subprocesses remain included. #1903/#2055 retain fixture/installed-host contracts;
#2122 retains distributed-byte smoke. No all-released-architectures guarantee.

## Verification and finite disposition

Local Node 24 structural/regression checks (not Darwin execution):

```bash
node --test .github/scripts/ci-technical-contracts.test.mjs .github/scripts/ci-unit-node.test.mjs
node --test .github/scripts/issue-*.test.mjs
```

The technical suite owns native job invariants and executes the workflow's JSON
summarizer against isolated fixtures. Keep the LF-normalized full CI hash in
`issue-refresh-router-workflow.test.mjs` aligned deliberately; never remove its
integrity assertion.

After separately authorized delivery, follow the
[accepted finite contract](https://github.com/Gentleman-Programming/engram/issues/2020#issuecomment-6423158281):
observe **one normally occurring PR, merge-group and main execution**. Do not
dispatch/rerun jobs to manufacture the sample. Record each run/attempt/event,
actual tested SHA (PR head and synthetic merge SHA are distinct), runner image,
tool versions, exit, wall time, executed/skipped scope and timeout/cleanup limits.

Classify original failures as product defect, fixture assumption, missing
prerequisite or resource limitation; establish comparable revision/environment
evidence before calling anything a regression. Then make an explicit
**promotion, focused-fix or defer disposition**. No indefinite informational
qualification loop, automatic required-context promotion or settings change.

## First normal PR observation: failed, not qualified

[Run 40391005166, attempt 1](https://github.com/Gentleman-Programming/engram/actions/runs/40391005166)
tested PR head `d7aa661d13e7137d302c86adf5f4fdccf2711bb9` through synthetic
checkout `4fb6c78f3654cd7334f0648f564f83b77b7d741a`. The actual runner was
macOS 15.7.9 ARM64, image `20260907.0337.1`, Go 1.26.10 and Node 24.20.0.
Preflight passed, including real `/usr/bin/jq` (`jq-1.7.1-apple`).
Darwin adapter tests executed and passed; this is native execution, not a
successful full baseline.

The native job failed in 4m30s; the suite took 235.92s, without exhausting either
ceiling. JSON recorded **8,401 pass / 33 fail / 127 skip test/subtest events**
and **27 pass / 7 fail / 4 skip package events**. These are event counts, not
unique top-level tests. Original test failures and incomplete summary status
remain authoritative.

Observed issues include logical/physical temporary-path comparisons, an
existing Darwin update-instruction test/implementation disagreement, a hook
POST timeout and Core fixture cleanup failures. Thirteen skips were also
unclassified: eleven owned PostgreSQL-fixture cases and two foreign Windows
cases. The reporting correction recognizes only those existing exclusion
messages; it does not hide failed tests or unavailable mandatory prerequisites.

Physical-root canonicalization and corrected reporting still need a normal
hosted execution on the new candidate. No rerun, assertion weakening, product
change or larger budget is implied. Hook/Core and version-owner fixes remain
separate scope decisions. Merge-group/main samples and a green, stable native
baseline are still missing; do not promote the job from this failed sample.

The normal follow-up [run 40396146998](https://github.com/Gentleman-Programming/engram/actions/runs/40396146998)
used head `29838bcbd35c2e340cc3973077f95408ff02f046`, synthetic checkout
`817bf6c95edb66c201b683a3dcba891a2b33bdf3` and the same reported runner image.
The physical root was `/private/tmp`; previous path/history/backup and Core
comparison cases passed. The native suite still failed: 8,432 pass / 2 fail /
127 skip test/subtest events, with no unclassified skips, in 221.84s.
Its remaining failures were the version-instruction test and the no-jq prompt hook.

The test-owner follow-up asserts the existing exact update instructions for
Darwin, Linux and other hosts instead of imposing the other-host Releases URL
on Darwin. The no-jq fixture keeps its Unicode payload, three request checks,
state/nudge assertions and original deadlines. Failure-only diagnostics record
observed request counts and bounded child output; they do not repair or bypass
a failing hook. A passing local Windows test is not Bash 3.2/macOS proof.
The no-jq runtime cause and a green native baseline remain unproven; product
hook changes require a separate scope decision.

The subsequently authorized product fix normalizes quoted character conversions
to unsigned bytes before the Bash hook classifies JSON bytes or encodes
JSON and URLs. The Windows-safe session-key helper stays unchanged. Apple Bash 3.2's
[`printf` source](https://github.com/apple-oss-distributions/bash/blob/main/bash-3.2/builtins/printf.def)
returns the quoted `char` without an unsigned cast; sign-extension can reject
non-ASCII decision metadata or corrupt byte encodings. A source-derived
signed-conversion fixture reproduced those failures through the public hook
before the fix. That compatibility fixture is **not native Bash 3.2 execution**.

The regression keeps exact Unicode prompt/project/cwd values, capture-decision
and prompt-write counts, bootstrap/reminder output, empty stderr and rejected
raw-control metadata with no prompt writes or local store creation. Existing
malformed/nested/wrong-type/unknown-source decision guards remain unchanged.
No interpreter upgrade, external JSON dependency or longer deadline is added.
The required matching Claude plugin patch versions advance to 0.1.19; this is
metadata consistency, not a release or publication.

Actual macOS execution of this product fix is still pending. Separately, the
completed run 40403756179 also failed Linux Race at
`TestServeOwnedStartupFailure` (expected startup failure); its inspected log
showed no data-race warning or timeout. That failure is not attributed to this
unpublished fix, and Race cadence/budgets remain untouched.

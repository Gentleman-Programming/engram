# Release candidate checker: rejection-only slice

This opt-in checker reports metadata errors and current Pi blockers. **It cannot
approve a release candidate.** Every candidate check exits nonzero, including
hypothetical matching tool inventories. Only help exits zero.

## Run the checker

From the repository, using a local Go toolchain and existing authorized caches:

```sh
node tools/release-candidate.mjs --help
node tools/release-candidate.mjs --core-ref refs/tags/v3.0.0 --pi-ref refs/tags/pi-v0.5.0
```

The second command is an illustrative independent pair, not a public release
selection or creation/verification of remote tags. Candidate metadata must match
the selected Pi version; this example does not assert current metadata matches.
It is expected to fail, with blockers on stderr and no success JSON on stdout.
Both full refs are mandatory and define an explicit ordered core/Pi pair. Each
version must independently satisfy strict SemVer; equality is not required.
Package name/version and the installer pin are checked against the Pi version
by the live Go validator/exporter. Pair identity is metadata only, not proof of
remote tag existence, source/digest identity or runtime compatibility.
There are no skip, bypass, root, proof, status override or publication options.

## What this slice checks

1. Runs `go run ./tools/release-candidate` with the supplied refs. Its JSON export
   contains the live Go-owned Agent contract; metadata/export success is **not**
   Pi parity, candidate approval or publication permission.
2. Runs the existing, unchanged Pi release-contract guard against the Pi ref.
3. Copies the Pi runtime sources into a disposable sandbox and captures actual
   `registerTool` calls. An array preserves duplicate registrations. It compares
   exact tool inventories and reports sorted missing, extra and duplicate names.
4. Rejects the candidate and explains remaining proof gaps. It never silently
   allowlists differences or emits positive readiness evidence.

The sandbox uses the existing test TypeBox stub. It proves registration only,
**not JSON-schema equivalence or genuine runtime semantic equivalence**. Even
identical inventories cannot pass. Unknown schema/semantic proof blocks;
`mcp-foundation` and declared Pi semantic differences are explicit blockers.

Current Pi default registrations align with Go's ProfileAgent, including
`mem_consolidate`, `mem_judge_many`, and `mem_session_finalize`; administrative
`mem_delete`, `mem_stats`, and `mem_timeline` are excluded. The checker compares
actual registrations with the live Go inventory and still reports any future
missing, extra, or duplicate names. Matching names do not establish schema or
semantic equivalence and do not change the rejection-only outcome.

The Go contract remains `mcp-foundation` with declared Pi differences. These are
honest source rejection reasons, not failures to be bypassed for a release.

## Opt-in Go assessment (non-approving)

The Go CLI separately supports `-assess`; the Node checker above does not accept
this option and retains its rejection-only behavior:

```sh
go run ./tools/release-candidate -assess -core-ref refs/tags/v3.0.0 -pi-ref refs/tags/pi-v0.5.0
```

For a valid independent metadata pair with a matching installer pin, stdout is:

```json
{"schemaVersion":1,"version":"3.0.0","piVersion":"0.5.0","outcome":"blocked","reason":{"code":"positive-proof-unavailable","message":"live Go contract does not establish Pi parity or release approval"}}
```

This is illustrative, not a claim that current metadata matches those refs.
Assessment emits one deterministic JSON object plus newline, without source
writes. Readable invalid metadata yields `rejected`, `invalid-metadata`, and
exit 1; unavailable/unreadable metadata yields `blocked`, `metadata-unavailable`,
and exit 2. A live contract export failure yields `blocked`,
`contract-unavailable`, and exit 2. Reason messages are fixed safe diagnostics.
Structured results have empty stderr. The compiled executable preserves these
exit codes; `go run` adds its own exit-status stderr and wrapper exit code.
There is no `eligible` outcome or assessment success/exit 0: `mcp-foundation`
is not Pi parity, and no trusted positive producer/binding is available.

Flag/ref invocation errors instead retain plain stderr, empty stdout and exit 1.
Help retains exit 0. Caller-authored evidence, passed JSON and status/contract
overrides are unsupported. `-assess` refuses any `-pi-tarball` presence, even an
empty argument, before reading metadata or validating archives. Omit `-assess`
or use `-assess=false` for unchanged legacy export/standalone tarball behavior.
Assessment does not run suites, schema diagnostics, npm or the Node guard; it
cannot authorize publication or promotion to `latest`.

## Trusted source observation (partial evidence only)

The Go CLI also supports `-observe-contract` with `-core-source` and
`-pi-source`: explicit full lowercase 40/64-hex commit IDs available in the
local Git repository selected by `-root`. Existing `-core-ref`/`-pi-ref`
remain independent SemVer intents, not verified remote tags. Missing prospective
tags are allowed; existing local tag bindings must agree with selected commits.
Sources must already be reviewed and trusted by the operator. This executes Go
from the selected core commit; a temporary copy is **not a security sandbox**,
and a malicious checker can forge success or perform arbitrary side effects.
Never use this mode for untrusted sources or with privileged secrets.

Git replacement objects are disabled for all source, tag and object reads.
The collector copies raw committed Git objects (no export filters, symlinks or
submodule materialization), reads the selected Pi manifest and core installer
pin, and runs the fixed `go run ./tools/agent-contract -check` in the core copy.
Go uses local toolchain/cached dependencies, readonly modules and no work file.
The report binds verified source IDs and the artifact's actual SHA-256 to the
observed process result. It does not import caller-authored proof or commands.
A nonzero checker result is `not-established`, not automatically a semantic
regression. Cancellation/deadline expiry is `unavailable`, without a checker
exit outcome. Null-device outputs prevent waiting for inherited output pipes;
this does not terminate or sandbox the entire child process tree.
Missing objects/tools, unsupported sources, persistent source
mutation or cleanup failure cannot produce a passing check.
Output is partial source-evidence JSON with `outcome: "blocked"`, exit 2 and
empty stderr even when the check passes. The observed `go run` exit is a wrapper
exit, not an inferred inner-program exit. No package execution, installed-host
compatibility, eligibility or publication is established. Invocation errors
retain stderr/exit 1. Source flags require observation, which refuses active
`-assess`. Without the explicit package opt-in below, any `-pi-tarball` presence
is also rejected before repository access. Legacy modes
are otherwise unchanged. The report is a view, not an approval credential.

### Opt-in selected-Pi input projections

Add `-observe-pi-inputs` to the Go `-observe-contract` invocation to capture
actual selected-Pi schemas with the existing helper and real TypeBox, then run
`go run ./tools/agent-contract -verify-pi <owned-capture>` from the selected Core.
The independently selected Pi must carry the existing real-TypeBox helper.
Node.js 24 and the already authorized `plugin/pi/node_modules/typebox` 1.3.34
in the repository selected by `-root` are required. The collector only copies
that fixed dependency into its owned workspace; it never installs/downloads,
uses an ancestor/global dependency, or falls back to the schema stub.
Trust must cover the executed selected-Pi/helper code and installed dependency,
not just Core. A package version is not an authenticity attestation.

The optional `piInputs` view records both source identities, contract/capture
SHA-256, TypeBox version, Node capture exit and actual Go-wrapper verification
exit when established. Missing prerequisites, cancellation, mutation or cleanup
uncertainty cannot pass. Capture pipes have bounded EOF waits; descendants are
not universally terminated. Raw captures/diagnostics are not emitted.
Without this flag (or with `-observe-pi-inputs=false`), the old observation
JSON and Go-only behavior remain unchanged. The flag requires active
`-observe-contract`; other modes and the Node sibling remain unchanged.
Even passing projections only prove Go-owned input-shape acceptance using a
stub UI host—not semantic parity, artifact execution, installed-host behavior
or release eligibility. The overall observation remains blocked/exit2/empty stderr.
For mandatory local functional coverage, use
`ENGRAM_PI_SCHEMA_REQUIRE_REAL=1 go test ./tools/release-candidate`;
without prerequisites the real-capture test may skip, never proving this check.
CI's Plugin Tests job runs that focused test with the mandatory flag after its
existing Node24, locked TypeBox install and Go build. A missing prerequisite or
skipped positive capture therefore cannot satisfy that CI evidence. Hosted
results must still be observed for the exact candidate; scheduling is not a pass.

### Opt-in selected-Pi package binding

Add `-observe-pi-package` and a non-empty `-pi-tarball` to the Go source
observation. This is the only opt-in that permits that existing flag combination;
absent/false preserves old observations, conflicts and standalone package behavior.
The collector copies bounded compressed bytes into its owned workspace and
materializes the selected Pi commit separately from the Pi input-capture copy.
It reuses the existing Go archive validator on that same tarball snapshot,
including all framing, metadata, inventory, path/link and size restrictions.
The optional `piPackage` records state, verified Pi source commit, selected Pi
version, actual compressed-tarball SHA-256 and valid file count when established.
The validator runs in the collector, not as a selected-Core subprocess: no
process exit code or selected-Core verifier identity is invented for this view.
Observed source/archive/input changes, unreadable inputs, cancellation or owned
cleanup failure cannot establish a passing binding. Copies are not a sandbox.
No package entrypoint, Node/npm, dependency installation or publisher is invoked
by this check; existing trusted Core freshness execution remains unchanged.
Even a passing binding remains schema1/blocked/exit2/empty stderr. It is neither
registry authenticity, reproducible-build proof, artifact execution nor eligibility.

## Separate real-schema diagnostic

`node tools/pi-schema-diagnostic.mjs` captures real serialized TypeBox schemas
against the existing committed Go contract without regenerating it. First run
`npm ci --ignore-scripts --legacy-peer-deps` in `plugin/pi` (Node.js 24).
See [diagnostic output/exit semantics](PI-TOOL-PROJECTIONS.md#real-schema-diagnostic).
Differences are expected, not an added strict parity/release gate. Even zero
structural differences is not semantic proof or release approval. Baseline freshness
is checked separately by Go; this command does not establish it. The release checker
above still uses its default registration-only stub and always rejects candidates.

## Separate local package validation

A standalone command validates the local npm tarball, **not release eligibility**:

```sh
node tools/validate-pi-package.mjs --help
node tools/validate-pi-package.mjs --core-ref refs/tags/v3.0.0 --pi-ref refs/tags/pi-v0.5.0
```

Run from the candidate repository root (the current directory). As above, these
refs describe source metadata, not a public version choice or remote tag check.
The command uses the Go helper from this checkout, validates candidate metadata
first, and runs local `npm pack` offline with lifecycle scripts disabled. It does
not install dependencies or contact/publish to a registry. Node launches npm's
installed JavaScript entrypoint without a shell, including on Windows; an
unsupported/missing npm installation rejects rather than falling back to a shell.

Go validates the gzip/tar stream without extracting or executing package code.
The package must have the same name, exact version, manifest metadata and file
bytes as the candidate. The current literal `files` allowlist is expanded
recursively, including `assets/` and `test/`; every declared source file and the
required runtime/CLI/template/README files must be present. Glob/negation
allowlists are unsupported and reject. Unknown, duplicate, missing or changed
archive entries, links, unsafe paths, malformed/truncated streams and invalid
gzip trailers reject. Tar termination requires both 512-byte zero end blocks;
physical EOF alone (even after a zero-filled final file) is not a valid terminator.
Limits are 4096 files and 64 MiB of source/decompressed content. Manifest JSON
formatting may differ; decoded metadata may not. Numbers compare as exact JSON
tokens without float64 rounding: `1` and `1.0`, for example, differ.

The Go helper also accepts an already packed local archive:

```sh
go run ./tools/release-candidate -core-ref refs/tags/v3.0.0 -pi-ref refs/tags/pi-v0.5.0 -pi-tarball /path/to/candidate.tgz
```

Both Go JSON results contain legacy `version` (the core version) and `piVersion`
(the independently selected Pi version). Without `-pi-tarball`, `contract` is the
live Go contract. With it, `validation: "local-package-only"` and `files` describe
local archive validation. The manifest, installer pin and packed archive bind to
`piVersion`, never the core `version`; Node uses it for the tarball filename and
checks both returned identity fields. Existing equal-version pairs remain valid.
Neither result is semantic proof or release approval.

The standalone Node command exits zero with exactly
`Local Pi package validated; not release approval.` on stdout (plus newline).
Rejections exit one, leave stdout empty and emit only
`Local Pi package validation failed; no release approval.` on stderr (plus
newline); raw npm/compiler diagnostics and private paths are not forwarded.
Tarball, npm configuration/cache and temporary files live in an invocation-owned
`.pi-package-validation-*` directory under the candidate root and are removed
before success is reported or on ordinary failure. Existing Go caches remain
caller-owned; missing locked modules reject offline. No source, manifests,
versions, tags, workflows or publishers are modified.

The original Node rejection-only checker does **not** run this command and still
always rejects candidates. Package validation does not establish tool/schema
parity, runtime compatibility, passing suites or complete semantic evidence.

Promotion to `latest` remains manual and requires separate authorization.

## Safety and deferred work

The checker uses shell-free, bounded subprocesses and offline, read-only Go
module resolution. It owns disposable sandbox/TEMP files under the checkout and
cleans them up on ordinary success/error paths. Existing Go caches remain
caller-owned. No dependency installation, registry probes, publishing, version
or tag writes, real `node_modules` changes, or source metadata changes occur.
Missing tools, subprocess failures, malformed exports and capture/cleanup errors
reject the candidate without forwarding raw private runtime diagnostics.

The Pi test suite also invokes this live Go exporter. CI and the existing Pi
publish preflight therefore provision Go from `go.mod` and build the helper with
`-mod=readonly` before `npm test`, preparing its locked modules/build cache for
the offline checks. This prerequisite build does not run the checker or approve
a candidate. The publish workflow's triggers, permissions, full-ref guard and
publish command remain unchanged; tests still immediately precede publication.
Local tests require an appropriate Go toolchain and already prepared module
cache; a clean Node-only environment is insufficient.

The original full release gate is **deferred, not implemented or verified**:
genuine semantic evidence, JSON-schema equivalence, mandatory Go/Pi suite
orchestration and integration of local package validation into that full gate
remain future work. Standalone package validation above does not satisfy all
of S2 or change the rejection-only checker. This checker is not a replacement
for those gates.

## Test evidence is not release readiness

The official Pi `npm test` runner executes every `test/*.test.mjs` file with
`--test-concurrency=1`. Go-backed test files therefore do not compete with one
another for the local compiler/build cache. This changes file scheduling only:
no tests are removed, and the production subprocess deadline remains 24 seconds.
Instrumented parallel runs observed both public commands timing out in
`go-metadata`; npm packing/tar validation had not started. Serial scheduling is
not a substitute for observing the suite's outcome on the candidate.

From `plugin/pi`, run `npm test` for the full suite. Focused checks from the root:

```sh
node --test plugin/pi/test/release-candidate.test.mjs
node --test plugin/pi/test/local-package.test.mjs
go test ./tools/release-candidate
```

The Go Core lifecycle tests require Node.js >=24 in PATH. If Node is missing,
cannot run, or is older, the child-lifecycle test and only the runtime subtests of
`TestArtifactInputCLI` skip with a clear prerequisite message; non-runtime
admission checks still run. A separate regression runs with Node.js <24 to verify
that the production helper rejects it before starting Core. Use Node.js 24 for
full supported-runtime coverage; passing with skips is not lifecycle evidence.

A passing checker test suite would demonstrate the rejection-only behavior and
unchanged legacy Pi guard. It would not make current Go/Pi sources release-ready.
Implementation validation and independent review must be observed separately;
this guide does not claim those checks passed or authorize a PR/publication.

## Reviewed staged-input admission

Use `-observe-artifact-inputs` for non-executing admission of an explicitly reviewed #1928 bundle. It never starts Core or imports Pi.
```bash
go run ./tools/release-candidate -observe-artifact-inputs \
  -core-ref "$CORE_REF" -pi-ref "$PI_REF" -core-source "$CORE_SOURCE" -pi-source "$PI_SOURCE" \
  -root "$REVIEWED_REPOSITORY" -staging-bundle "$REVIEWED_ZIP" -staging-context context.json
```

Replace every context pin below with the reviewed value; the regular JSON file is limited to 4,096 bytes and unknown fields/trailing documents reject.
```json
{
  "WorkflowSHA": "FULL_WORKFLOW_COMMIT", "CandidateSHA": "FULL_CORE_COMMIT",
  "RunID": "REVIEWED_RUN_ID", "RunAttempt": "1",
  "CoreVersion": "1.2.3", "PiVersion": "4.5.6", "ZIPHash": "REVIEWED_ZIP_SHA256"
}
```

Refs/versions are intents, not authenticated tags or observed runtime versions. Core source must match the candidate; Pi source is selected independently and its archive bytes must validate against that raw Git snapshot. ZIP/member digests, actual host Go metadata and source-bound Pi materialization are checked, originals rechecked, and owned output removed before reporting. JSON always has outcome `blocked`, exit 2 and empty stderr; `check.state=passed` means admission only, while `workflows.state=unavailable` explicitly records no execution. Missing/mismatched/changed/unavailable evidence cannot pass. Caller-edited context/digests are not publisher authentication, execution permission or a sandbox. Without this opt-in (including false), existing modes are unchanged; do not combine it with other observation modes, assessment or tarball validation.

## Owned Core lifecycle observation

Replace `-observe-artifact-inputs` in the preceding command with `-observe-artifact-workflows` to explicitly execute the reviewed Core after admission. These modes are mutually exclusive; false preserves existing modes. Node24 is required, without installation.
The OS temporary directory must be outside Git ancestry: use an explicitly authorized owned TEMP/TMP/TMPDIR. Unknown isolation or prerequisites cannot establish execution. Only the selected measured binary is copied; no executable/SDK override exists.

The fixed embedded Node owner starts `serve --session-owned`, generates a private token, uses literal owned home/config/data/temp roots without inherited credentials/cloud/transport, and verifies loopback readiness, owned PID, authenticated health and instance identity. Readiness is bounded to 5s, EOF join to 3s, then owned-child kill/join to 2s.
`coreRuntime` records lifecycle state, Node version, measured helper hash, actual exit/join/cleanup and raw health `reportedVersion` separately from version intent/build metadata. Unknown termination retains owned state; killed/failed/unknown cleanup cannot pass. Copies and environment containment are not a malicious-code sandbox.
Pi is not imported in this milestone: `workflows.state` remains `unavailable`, outcome stays `blocked`/exit2, and there is no genuine SDK, semantic parity or release-eligibility claim. The Pi package, original bundle and context are rechecked before starting Core; a failed recheck leaves the runtime not started. Post-execution rechecks remain in place to detect changes during execution. These observations do not prevent hostile concurrent changes or ABA. No publication occurs.

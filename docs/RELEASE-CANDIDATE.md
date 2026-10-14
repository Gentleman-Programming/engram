# Release candidate checker: rejection-only slice

This opt-in checker reports metadata errors and current Pi blockers. **It cannot
approve a release candidate.** Every candidate check exits nonzero, including
hypothetical matching tool inventories. Only help exits zero.

## Run the checker

From the repository, using a local Go toolchain and existing authorized caches:

```sh
node tools/release-candidate.mjs --help
node tools/release-candidate.mjs --core-ref refs/tags/v0.4.2 --pi-ref refs/tags/pi-v0.4.2
```

The second command is a diagnostic example using current source metadata, not
selection of a public version or creation/verification of remote tags. It is
expected to fail, with blockers on stderr and no success JSON on stdout.
Both full refs are mandatory. Strict SemVer, exact version equality, package
name/version and the installer pin are checked by the live Go validator/exporter.
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

Current source inventory gaps are:

- Go-only: `mem_consolidate`, `mem_judge_many`, `mem_session_finalize`.
- Pi-only: `mem_delete`, `mem_stats`, `mem_timeline`.

The Go contract remains `mcp-foundation` with declared Pi differences. These are
honest source rejection reasons, not failures to be bypassed for a release.

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
orchestration, local npm pack and tarball metadata/content verification remain
future work. This checker is not a replacement for those gates.

## Test evidence is not release readiness

```sh
node --test plugin/pi/test/release-candidate.test.mjs
```

A passing checker test suite would demonstrate the rejection-only behavior and
unchanged legacy Pi guard. It would not make current Go/Pi sources release-ready.
Implementation validation and independent review must be observed separately;
this guide does not claim those checks passed or authorize a PR/publication.

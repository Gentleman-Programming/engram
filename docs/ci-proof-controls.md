# Dormant manual-release controls (#2029 / #2030)

These are dormant CI-orchestration controls, **not hosted gate evidence**.
The native and legacy workflows contain default-off boundary adapters. No
repository variable or runtime control is activated by this change. Local models
do not prove native provenance, required-gate selection or queue safety.
Polling only waits for an explicit release; elapsed time is never a witness.

## Workflow admission concurrency (#2096)

CI coalesces only ordinary `pull_request` runs into a workflow-namespaced
PR-number group, with cancellation enabled. Admission requires both an empty
raw `vars.ISSUE_CI_PROOF_CI` and a head branch not starting with `ci/proof-`.
GitHub's `startsWith` is case-insensitive: malformed reserved names and uppercase
variants are protected too. No controller JSON is parsed at workflow admission.
Any nonempty variable, including explicit OFF, malformed JSON or an unmatched
selector, conservatively isolates all PR runs that observe it. This guard does
not activate or authorize a controller; job-level parsed selection is unchanged.

All protected runs use a workflow-namespaced `run_id`/`run_attempt` group with
cancellation disabled, including merge groups and main pushes. Unique groups
protect pending slots as well as running work: `cancel-in-progress: false` alone
would still allow replacement of another pending run in a shared group.
Protected reruns have distinct attempt groups; ordinary PR reruns still share
the PR group and may cancel other runs for that PR. Different PR numbers and
workflow names remain isolated, even when fork branch names match.

Local tests check the expression shape and a limited routing model, not hosted
admission or cancellation. Fork/rerun variable visibility, control transitions
(including empty/nonempty changes between attempts), and scheduling by commit
recency are **not hosted-qualified**. GitHub scheduling is not a newest-commit
ordering guarantee. A workflow-level guard cannot rely on a selector parsed later
inside a job. Controllers remain unchanged and OFF; there is no measured savings
claim or hosted gate/queue proof. Controller activation, reruns and other hosted
experiments require separate authorization.

Run `node --test .github/scripts/ci-technical-contracts.test.mjs` for independently
owned technical workflow contracts: ordinary PR/main/merge-group isolation,
race/toolchain/failure behavior, verification commands/order, permissions, events
and discovery. It needs no proof helpers or qualification data and also validates
a fixture without the hold step. These source checks are not hosted execution
or cancellation evidence.

Run `node --test .github/scripts/issue-ci-concurrency.test.mjs` for the exact
proof admission predicate, reserved prefixes and nonempty raw selectors. Retain
the producer, bootstrap/security/integrity and proof-control suites below; local
technical coverage does not replace live hosted cancellation proof.

## Public API

`.github/scripts/issue-ci-proof-controls.mjs` exports only
`createProofControl(options = {})`, returning `{ hold(result) }`.
`hold` returns a Promise resolving to the exact supplied producer value/object,
without cloning, modifying or evaluating it. It does not mutate inputs.

Options are trusted plain records and trusted injected functions (not proxies,
accessor-bearing configuration, untrusted comments or arbitrary executable data):

- `manifest`: omitted disables the control; `{ enabled: false }` also disables it.
  Disabled construction/holds call no transport, clock, nonce, wait or emitter;
  repeated disabled holds are allowed. Other configurations must explicitly use
  `enabled: true`, or the first hold rejects before reading.
- Active manifest: `actorIds` is a nonempty array of positive safe integer GitHub
  user IDs; `expiresAt` is an integer epoch millisecond deadline, strictly in the
  future and at most 300,000 ms from first hold. `maxReads` is an integer 1–10;
  `intervalMs` is an integer 1–1000. No string-to-number coercion occurs.
- `binding`: `{ case, runId, attempt, phase, head, source }`. `runId` and `attempt`
  are positive safe integers; `head` is the original lowercase 40-hex SHA.
  `case`, `phase`, `source` are 1–240 ASCII characters from
  `A-Z a-z 0-9 _ . : / @ -`. Source should identify the intended workflow/revision.
  Syntax validation does **not** authenticate that source or run ancestry.
- `readRelease(checkpoint)`: required read-only async transport, returning a
  witness object or unrelated data/null. Checkpoint is frozen and contains the
  six binding fields plus `checkpoint` (fresh nonce) and `expiresAt`.
  A release witness must match **every** checkpoint field exactly and include
  an allowlisted numeric `actorId`. Extra fields, including comment bodies,
  are ignored and never emitted or interpreted as instructions.
- `now()`: integer epoch milliseconds, default `Date.now`; backward or invalid
  clock readings fail closed. `nonce()`: default cryptographic `randomUUID`;
  injected nonces must be fresh, 16–128 ASCII alphanumeric/underscore/hyphen.
- `wait(ms)`: async bounded polling wait (default Node timer); never a release.
  `timers`: `{ setTimeout, clearTimeout }`, default Node timers, implements the
  overall deadline. Tests inject these functions and advance time without sleep.
- `emit(record)`: synchronous local observer, default no-op. Receives frozen
  `{ event: 'checkpoint' | 'released', ...checkpoint }`; released records also
  contain the accepted `actorId`. Never receives producer result, arbitrary
  metadata, comment text, transport errors, credentials or invented check IDs.

Bindings and manifest fields are snapshotted at construction. A controller is
single-use for its one bound phase: first active hold reserves it synchronously,
before any await, even if it later fails. Subsequent/concurrent holds reject.
Create a new controller with a fresh nonce for a distinct checkpoint. Injected
nonce freshness is the caller's responsibility; there is no global/durable
replay registry or cross-controller/process idempotency guarantee.

A witness has this shape (values mirror the deterministic test API):

```js
{
  case: 'approval-revocation', runId: 123, attempt: 2, phase: 'after-read',
  head: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  source: 'owner/repo/.github/workflows/policy.yml@refs/heads/main',
  checkpoint: 'fresh-checkpoint-0001', expiresAt: 2000, actorId: 42
}
```

Here `expiresAt: 2000` is a fake-clock example with `now() === 1000`, not a
usable wall-clock release. Tests use allowlist `[42]`, two reads and 10 ms waits.

## Fail-closed and trust boundaries

Rejections are owned `Error` messages `proof-control: CODE`, with no arbitrary
error cause/body. Codes: `configuration`, `used`, `clock`, `expired`, `transport`,
`witness`, `emitter`, `wait`, `exhausted`. No release occurs on deadline, exhausted
reads or errors. A transport error is terminal, not retried. The deadline also
rejects hung reads/waits; it cannot cancel their underlying I/O, but late results
cannot emit release. Transports must be read-only and should bound their own I/O.
Trusted injected clock/timer functions must honor their contracts.

A transport adapter must authenticate actor identity and extract fields as data;
accepting a caller-supplied numeric ID alone is not GitHub authentication. This
library cannot establish native/GitHub provenance, provider identity or ancestry.
Neither a user-supplied source string nor a local test establishes those facts.

Planned approval revocation/head advance are not silently reread or cancelled.
Original producer guards remain the producer's responsibility; the library
neither substitutes policy logic nor rewrites an already supplied decision.
Check IDs are assigned after actual creation elsewhere, never pre-invented here.

## Scope and verification

The policy implementations, guards, router, publisher, resolver and fixtures
remain unchanged. The two producer workflows call the adapter only through
explicit configuration; six required context names/App15368 are preserved.
The #2031 permission exception is described below. No comments, issues, Checks API results, environments, credentials,
queue entry or merge are created/configured. #2026/#1810 remain open.

Run `node --test .github/scripts/issue-ci-proof-adapter.test.mjs .github/scripts/issue-ci-proof-workflow.test.mjs .github/scripts/issue-ci-proof-controls.test.mjs`, then
`node --test .github/scripts/issue-*.test.mjs` locally. Existing Policy Helper
Tests discovers the new `issue-*.test.mjs` on candidate `github.sha`; no workflow
edit is needed for discovery. Discovery is not evidence that hosted CI has actually run.
Local fake transport/clock tests are foundation-contract evidence only, never
canonical production proof or authorization for hosted experiments.

## Producer adapter and actual boundaries (#2030)

`createGitHubProof(options)` in `issue-ci-proof-adapter.mjs` returns
`{ client, complete(value), assert() }`. Native jobs pass `client` to the original
evaluator: the first actual issue GET is held before its response is consumed.
The existing final PR reread/head guard still runs. `assert()` prevents a caught
hold error from becoming reference success. Legacy jobs await `complete(failures)`
after original verdict reporting, before their original GHA job completes.
No Checks API substitute is created; resolver failures/early returns are unchanged
and emit no checkpoint. Merge-group handling never activates these adapters.

Callers supply trusted `context`, host `environment` (or injected `runtime`),
`phase`, actual trusted `checkout` SHA, and optional foundation test dependencies.
The recorded source is `WORKFLOW_SHA/CHECKOUT_SHA`, not an assertion they are equal.
Native workflows pin LF-normalized SHA256 digests of adapter/core before import;
CI checks those pins. Dependency changes must update the protected workflow,
preserving the original router source guard without extra GitHub requests.

## Dormant configuration and authenticated reads

`vars.ENGRAM_CI_PROOF_MANIFEST` feeds the matching environment variable.
**Setting it or posting release comments requires separate authorization.**
Empty/unset or JSON `{ "enabled": false }` skips helper import and all transport,
including on older bases. Active imports use only trusted checked-out code;
an older checkout lacking the helper must never fall back to PR code.

- Raw JSON: at most 8192 characters; `enabled: true`, `case` (1–120 label
  characters), positive `issueNumber`, and foundation
  `actorIds/expiresAt/maxReads/intervalMs` are required.
- Required `target`: `repo` (`owner/repo`), `prNumber`, original `head`, event
  `action`, trusted `checkout`, and `phase`. Phases are `native-read:reference`,
  `native-read:approved`, `legacy-completion:reference`, `legacy-completion:approved`.
- Optional target `runId/attempt/source` narrow a known run. Otherwise these are
  captured from Actions and still bind every release; never guess future run IDs.
  Unrelated selectors are no-ops. Checkpoint case is `owner/repo:case`.

One static GraphQL query reads the last 20 comments on the configured issue in
the same repository. Only an allowlisted numeric `User.databaseId` from server
`author`, with `lastEditedAt: null`, can release; JSON `actorId` is ignored.
Bot/deleted authors, edited records, malformed/bodies over 4096 characters and
stale/mismatching checkpoints cannot release. Comment JSON must match every
emitted field; it is data, never code or receipt text. See GitHub's
[IssueComment](https://docs.github.com/en/graphql/reference/issues) and
[User](https://docs.github.com/en/graphql/reference/users) field definitions.

After the immediate first read, queries are spaced 30 seconds, clamped to the
unchanged five-minute foundation deadline (at most 10 queries/20 records each).
Configuration/source failures use static `proof-adapter: configuration|source`;
transport failures are terminal, redacted foundation errors. Time never releases.
These controls trust the Actions context/client and immutable checkout; they
are not independent ancestry, canonical native provenance, hosted adversarial
evidence or queue protection. Separate grants/proofs remain necessary.

## Independent dormant Unit Tests hold (#2031)

`vars.ISSUE_CI_PROOF_CI` is a separate, **unconfigured** selector. Unset or
`{"enabled":false}` performs no helper import or API request. Unit Tests alone
now has job-scoped `contents: read` plus human-authorized `issues: read`, even OFF.
This is not evidence that issue-comment reads work on hosted fork/queue tokens;
qualification requires a future separately authorized window.

Before checkout or substantive commands, the existing required job awaits a
selected hold. No new context, job skip, deployment gate or error-success escape
is added. Original commands and merge-group identity remain intact.
The enabled manifest uses the foundation limits plus `issueNumber` and `case`.
Its required `target` filters are `repo,event,action,job,checkout,prHead`.
`runId,attempt,workflow,head` are optional preselectors: when present they must
match, but need not be guessed before run/group creation. All four actual runtime
values remain validated and mandatory in the fresh release witness binding.
`head` is actual `github.sha` (synthetic merge for PRs, group SHA for groups);
`prHead` is original PR head or null for groups. `checkout` is the immutable event
base, not candidate checkout. Without a head filter, multiple matching groups may
be held; this is not queue isolation. A future authorized window must establish
matching scope and group membership before drawing queue conclusions.

The bootstrap reads only the bridge/core blobs at that base, verifies explicit
SHA256 pins, and loads them from isolated runner scratch. Missing historical
helpers, corrupt source, malformed/expired controls and failed selected reads
fail Unit Tests itself. There is no candidate fallback. Pin changes require a
reviewed workflow change; this is not protection against rewriting the workflow
itself. Workflow/base source revisions and run/attempt/job/event/head are bound
into the fresh foundation checkpoint. Authentication and bounded read rules above
apply unchanged; arbitrary bodies/errors are never logged. Local mocks establish
neither active transport capability, native required-gate selection nor queue safety.

Focused checks: `node --test .github/scripts/issue-ci-proof-ci.test.mjs .github/scripts/issue-ci-proof-ci-workflow.test.mjs`.
Controls remain OFF; CI, independent verification and fresh review remain required.

## Fork PR configuration transport (#1810)

This alternative transports configuration without relying on fork `vars` visibility
or variable refresh on rerun. It remains dormant: the versioned
`.github/ci-proof-controllers.json` has `version: 1` and empty `controllerIds`.
No real controller is enrolled; implementation/tests are not hosted qualification.

Only a fork `pull_request` targeting canonical `main` may opt in with a reserved
source branch `ci/proof-<issue>-<case>` via the standard runner `GITHUB_HEAD_REF`.
Issue is a canonical positive decimal GraphQL Int (no leading zero); case is
1–100 lowercase letters/digits/dots/underscores/hyphens, starting alphanumeric.
The branch is untrusted routing, never authority. Malformed reserved routing,
missing runtime identity or any nonempty raw variable (including explicit false)
conflicts fail before helper import/API reads. Without this opt-in, existing raw
configuration behavior and zero-import/API unset/false OFF remain unchanged.
Merge groups and push events never activate this alternative.

Executable bridge/core/config helpers are fetched only at the immutable event
base, with exact byte SHA256 pins. Policy DATA is different: read the fixed
controller file from current canonical `main` once and snapshot its bytes/hash.
This avoids historical-base controller authority. Roster removal affects later
reads, not an already snapshotted hold; it is not automatic GitHub-role revocation.
Missing/corrupt sources fail selected Unit Tests; no candidate fallback exists.
The bootstrap and configuration reads share a 15-second absolute preparation
budget; requests are bounded, not retried, and checked again after responses.
This budget does not shorten the existing hold deadline or extend its expiry.

The same canonical issue supplies at most its last 20 comments. Envelope fields
are `schema: "ci-proof-config/v1"`, positive `prNumber`, and the enabled CI
`manifest` described above. Its case/issue, PR number, original head, mandatory
selectors and any supplied optional filters must match the actual runtime.
Only server `User.databaseId` in the policy snapshot with `lastEditedAt: null`
may authorize a configuration. JSON identity claims grant nothing. Bots, edited,
unauthorized, oversized (over 8192 UTF-8 bytes) and unrelated records cannot select
configuration. Authorized, unedited records matching the schema, PR number, case
and original PR head enter the uniqueness check before the remaining runtime
selectors or optional filters are validated. Exactly one such record in the
observed window is required. Records differing only in `runId`, `attempt` or
other runtime selectors still make selection ambiguous. Retire the earlier
record (edit or remove it) before adding a replacement; changing selectors alone
does not disambiguate it. This is neither whole-issue uniqueness nor a global
replay guarantee. Never pick newest-wins; unrelated release/chat records are ignored. Window eviction remains
an availability limitation. An unedited observation cannot prevent later editing.

The reader returns a snapshot of the existing manifest to the unchanged bridge.
A separate `configuration-resolved` receipt exposes only controller ID and
policy/configuration hashes; it proves delivery, not a checkpoint or release.
The fresh full runtime nonce/witness authentication and five-minute ceiling remain
unchanged. Later configuration edits/deletion/expiry never release a held snapshot.
Explicit proof branches are experimental fixtures: missing/expired/ambiguous
configuration fails future selected runs, not a green OFF. Normal branches do
not look up policy/comments. No global variable is written or restored.

Reader failures are static `proof-config: configuration|source|transport|expired`;
the workflow redacts them to its existing `proof-ci: bootstrap`. No arbitrary
body/error or producer result is logged. Existing job permissions, required
contexts/App15368, original commands, native/legacy producers and queue stay intact.
Run `node --test .github/scripts/issue-ci-proof-config.test.mjs .github/scripts/issue-ci-proof-ci-workflow.test.mjs`.
Real fork-token API access and live hold/release still need separately authorized
qualification; no comments, reruns or new experiment are authorized by these docs.

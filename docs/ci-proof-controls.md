# Dormant manual-release controls (#2029 / #2030)

These are dormant CI-orchestration controls, **not hosted gate evidence**.
The native and legacy workflows contain default-off boundary adapters. No
repository variable or runtime control is activated by this change. Local models
do not prove native provenance, required-gate selection or queue safety.
Polling only waits for an explicit release; elapsed time is never a witness.

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

## Authenticated configuration reader (#1810, dormant)

`.github/scripts/issue-ci-proof-config.mjs` exports `readCIConfiguration`.
This standalone reader is not wired into a workflow by this unit; normal CI,
existing raw configuration, job permissions and bridge/core remain unchanged.
The real `.github/ci-proof-controllers.json` contains `version: 1` and empty
`controllerIds`: no controller is enrolled and tests are not hosted qualification.

Callers supply trusted `github`, CI runtime `identity`, `prNumber`, `issueNumber`,
`caseName` and optional absolute `deadline`/clock/timer dependencies. Malformed
identity, including a missing or non-string action, rejects before API requests.
The reader fixes owner/repository/path internally and snapshots policy DATA from
CURRENT canonical `main`, recording its blob/content hashes. Historical base or
fork policy cannot authorize controllers. Roster removal affects later reads,
not an already snapshotted hold, and is not automatic GitHub-role synchronization.

One fixed read-only GraphQL query observes the canonical issue's last 20 comments.
Envelope fields are `schema: "ci-proof-config/v1"`, positive `prNumber`, and the
enabled CI `manifest` described above. Case/issue, exact PR/head and selectors
must match. Only a server `User.databaseId` in the policy with `lastEditedAt: null`
may configure; JSON identity claims grant nothing. Bots, edited, unauthorized,
unrelated and oversized bodies (over 8192 UTF-8 bytes) cannot select configuration.
Exactly one matching candidate in the observed window is required; never choose
newest-wins. This is not global uniqueness or replay prevention; window eviction
is an availability limitation and an unedited observation cannot prevent editing.

Requests share an absolute preparation budget capped at 15 seconds, with no
retries, and acceptance is checked after responses and before returning. Active
manifest deadline/read limits remain unchanged. Static errors are
`proof-config: configuration|source|transport|expired`; no arbitrary body/error
or producer value is logged. Returned `raw` is the manifest snapshot; `receipt`
contains only `configuration-resolved`, controller ID and policy/configuration
hashes, proving delivery only, not hold/release. Later edits/deletion/expiry never
release a held snapshot. Read permissions and actual fork-token access still need
separate hosted qualification; no experiment is authorized here.

Run `node --test .github/scripts/issue-ci-proof-config.test.mjs`.

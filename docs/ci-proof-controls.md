# Manual-release contract foundation (#2029)

This is a dormant CI-orchestration library, **not hosted gate evidence**.
No producer or workflow calls it. It does not prove an in-flight evaluator pause,
independent job hold, read ordering, native provenance, required-gate selection,
or queue safety. Polling only waits for an explicit release.

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

Existing evaluators, validation, router, publisher, resolver, fixtures, six
required contexts and App15368 are untouched. No comments, issues, checks,
environments, credentials, permissions, queue entry, merge or cleanup are
created or configured. Actual evaluator continuation (#2030) and independent
existing-job hold (#2031) are future phases; #2026/#1810 remain open.

Run `node --test .github/scripts/issue-ci-proof-controls.test.mjs`, then
`node --test .github/scripts/issue-*.test.mjs` locally. Existing Policy Helper
Tests discovers the new `issue-*.test.mjs` on candidate `github.sha`; no workflow
edit is needed. Discovery is not evidence that hosted CI has actually run.
Local fake transport/clock tests are foundation-contract evidence only, never
canonical production proof or authorization for hosted experiments.

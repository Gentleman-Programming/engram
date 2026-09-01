# Exploration: Cloud mutation validation for issue #503

## Problem statement

`POST /sync/mutations/push` accepts a legacy `observation` upsert whose payload has empty `content`. The request passes the handler's validation gate, but the same entry is later canonicalized by `chunkcodec` and rejected because observation upserts require non-blank `content`. With the real cloud store this becomes an opaque HTTP 500; with a store implementation that accepts the batch, malformed data can be persisted. The autosync client treats the project push as all-or-nothing, so one poison mutation remains pending and blocks later mutations.

The smallest safe first slice is to make mutation-push validation agree with canonicalization before calling the mutation store, return an actionable repairable HTTP 400, and preserve batch atomicity (no accepted subset and no persistence on validation failure). Dead-lettering, quarantine, and cursor advancement are separate recovery concerns and are not required to correct this validation contract.

## Current repository and upstream state

- The working tree is clean on `fix/delete-project-cross-project-sessions` at `b462396`.
- `origin/main` was refreshed and is `9cf93de` (`feat(tui): complete cloud sync settings (#788)`). The cloud target files below have no diff between the working tree and `origin/main`, so this issue remains present upstream.
- GitHub issue [#503](https://github.com/Gentleman-Programming/engram/issues/503) is still **OPEN**, labeled `type:bug` and `priority:medium`.
- Issue [#814](https://github.com/Gentleman-Programming/engram/issues/814) is **CLOSED**. PR [#892](https://github.com/Gentleman-Programming/engram/pull/892) merged into `main` on 2026-08-31 and repairs blank observation titles. It does not fix blank observation content during mutation push; it must not be reopened or duplicated.
- Issue [#503](https://github.com/Gentleman-Programming/engram/issues/503) remains the delivery tracker for this focused validation fix.

## Current behavior and causal flow

1. `CloudServer.handleMutationPush` decodes the envelope, enforces the 100-entry limit, rejects empty projects, authorizes each project, checks pause policy, and then calls `validateMutationEntry` before `InsertMutationBatch` (`internal/cloud/cloudserver/mutations.go:65-216`).
2. Relation entries use `chunkcodec.ValidateRelationPayload`. Session, observation, and prompt entries go through `validateLegacyPayload`, which is currently an unconditional `("", true)` (`mutations.go:318-344`). This is an intentional legacy-compatibility comment, but it now disagrees with the canonical mutation contract.
3. `CloudStore.InsertMutationBatch` calls `materializedMutationBatchChunks` before starting its SQL transaction (`internal/cloud/cloudstore/cloudstore.go:833-845`). Materialization builds a mutation-backed chunk and calls `chunkcodec.CanonicalizeForProject` (`cloudstore.go:1050-1160`).
4. For an observation upsert, `normalizeMutationPayload` trims and requires `sync_id`, `session_id`, `type`, `title`, `content`, and `scope`. Empty content returns `observation payload content is required for upsert` (`internal/cloud/chunkcodec/chunkcodec.go:415-452`). The resulting error is currently converted to `http.Error(..., 500)` at `mutations.go:203-206`.
5. The transaction therefore has not started for this specific canonicalization failure, but the handler has still exposed a store boundary that accepts the invalid entry. The handler also cannot assume every `MutationStore` implementation performs the same pre-transaction defense.
6. `MutationTransport.PushMutations` treats every non-200 response as an error (`internal/cloud/remote/transport.go:326-360`). `autosync.Manager.push` groups pending mutations by project and refuses to acknowledge any local sequence unless the response contains one accepted sequence for every entry (`internal/cloud/autosync/manager.go:502-568`). A malformed entry therefore keeps the whole project group pending and feeds the existing failure/backoff status path.
7. The client-side queue is ordered by sequence (`internal/store/store.go:4261-4289`). Existing local write guards prevent newly created empty observation content (`store.go:2529-2546`), but historical rows, direct database edits, old clients, and third-party clients remain possible inputs.

## Affected boundaries, files, and symbols

- `internal/cloud/cloudserver/mutations.go`
  - `CloudServer.handleMutationPush` — HTTP validation and all-or-nothing admission boundary.
  - `validateMutationEntry` / `validateLegacyPayload` — current inconsistency; the no-op legacy branch is the immediate defect.
  - `writeActionableError` — existing structured repairable-error convention used by the chunk endpoint.
- `internal/cloud/chunkcodec/chunkcodec.go`
  - `CanonicalizeForProject`, `normalizeChunkMutation`, and `normalizeMutationPayload` — current source of truth for required fields and payload normalization.
  - `ValidateRelationPayload` — existing exported validation pattern, but currently relation-only.
- `internal/cloud/cloudstore/cloudstore.go`
  - `InsertMutationBatch`, `materializedMutationBatchChunks`, and `materializedMutationBatchChunk` — persistence/materialization boundary and defense in depth. No change is required for the minimal fix unless validation is deliberately centralized here.
- `internal/cloud/cloudserver/cloudserver.go`
  - `handlePushChunk` and `validateImportableChunkPayload` — parity reference. Direct chunk ingestion already turns missing observation content into a structured repairable 400 before storage.
- `internal/cloud/remote/transport.go`
  - `MutationTransport.PushMutations` and `newMutationHTTPStatusError` — client handling of the corrected 400; existing parsing already preserves structured error fields.
- `internal/cloud/autosync/manager.go`
  - `Manager.push` — confirms why partial acceptance is unsafe in the current protocol. No cursor or ack change belongs in the minimal slice.
- `internal/store/store.go`
  - `AddObservation` prevents new blank content; `ListPendingSyncMutations` exposes historical pending rows. Existing quarantine/diagnostic facilities are relevant only to a larger recovery slice.

## Issue and acceptance evidence

### Upstream issue evidence

Issue #503 reports the exact mutation-push path, including the canonicalization error, HTTP 500, unchanged cloud mutation count, degraded autosync state, and manual recovery requirement. Its most recent independent confirmation reports seven malformed pending mutations freezing approximately 3,300 others for three days. The same comment confirms that the chunk/export rail returns a repairable 400, establishing a real parity gap rather than a missing validation rule.

### Repository evidence

- `validateLegacyPayload` is demonstrably a no-op for observation upserts.
- `normalizeMutationPayload` already defines the required observation fields and produces a precise content error.
- `handlePushChunk` already emits `error_class=repairable` and `error_code=payload_invalid` for equivalent invalid data.
- `TestHandlerPushRejectsMutationUpsertsMissingRequiredFields` proves canonicalization rejects malformed session, observation, and prompt mutation upserts on the chunk route.
- `TestHandleMutationPush_LegacyObsMissingOptional_Returns200` explicitly preserves acceptance of an observation containing only `sync_id` and `title`; this test will need to be revised if mutation push is aligned with the canonicalizer. Its current name and comment encode the obsolete assumption that legacy upserts have no required fields.
- `TestInsertMutationBatchIsAtomicOnFailure` proves the existing cloudstore transaction rolls back a partial SQL insert failure. This supports retaining all-or-nothing semantics rather than adding partial acknowledgements.

### Focused verification performed

The following existing tests passed on the current checkout:

- `go test ./internal/cloud/cloudserver -run 'TestMutationPush|TestHandleMutationPush|TestMalformedChunk' -count=1`
- `go test ./internal/cloud/chunkcodec ./internal/cloud/remote -count=1`
- `go test ./internal/cloud/autosync -run 'TestManager(RepairableFailure|PushFailedPhase|Backoff)' -count=1`
- `go test ./internal/cloud/cloudstore -run 'TestInsertMutationBatchIsAtomicOnFailure|TestMaterialized' -count=1`

These tests confirm the surrounding behavior, not that #503 is fixed; no production code was changed during exploration.

## Alternatives and tradeoffs

### 1. Shared canonical validation at the mutation-push gate — recommended

Expose or factor a chunkcodec validation function that validates a mutation entry using the same rules as `normalizeMutationPayload`, then invoke it for every entry before `InsertMutationBatch`. Preserve delete-specific rules, validate upserts for session/observation/prompt, retain relation validation, and return a structured repairable 400 containing the offending index, entity, and field/error. Keep the whole batch rejected on any invalid entry.

- **Pros:** removes the validation/canonicalization split; covers empty content and the other required-field failures that would otherwise still become 500s; preserves one canonical rule set; guarantees no store call on invalid input; matches the existing chunk-push error contract.
- **Cons:** tightens the mutation endpoint's legacy behavior; the minimal-payload compatibility test and its contract need updating; requires careful handling of delete payloads and entity-key fallback.
- **Effort:** Medium.

### 2. Validate only observation `content` in `cloudserver`

Decode observation upsert payloads in `validateLegacyPayload` and reject blank content while leaving all other legacy fields lenient.

- **Pros:** smallest code diff and directly fixes the reported reproduction.
- **Cons:** duplicates or partially mirrors canonicalization; title, type, scope, session, session-directory, and prompt-content failures can still become opaque 500s; preserves the architectural mismatch and makes future required-field fixes easy to miss.
- **Effort:** Low, but high residual risk.

### 3. Classify the cloudstore canonicalization error as HTTP 400

Change the handler/store error contract so known canonicalization errors are returned as a structured 400 after `InsertMutationBatch` fails.

- **Pros:** avoids a new handler validator and preserves current accepted legacy inputs.
- **Cons:** validation remains after the persistence boundary; it cannot protect alternate `MutationStore` implementations; the handler must classify wrapped errors reliably; it does not make the admission contract consistent; it is weaker than rejecting before storage.
- **Effort:** Low to Medium; not safe as the primary fix.

### 4. Add push-side quarantine/dead-letter and sequence skipping

On a permanent repairable 4xx, mark the offending local mutation as quarantined/dead, acknowledge or advance past it, and continue pushing later entries.

- **Pros:** self-heals the autosync outage class and addresses the independently reported queue freeze, including future unknown poison payloads.
- **Cons:** changes local disposition, ack/cursor semantics, status counts, operator evidence, and retry policy; risks silent data loss unless evidence and UI/doctor support are complete; requires coordinated client/server protocol and extensive tests.
- **Effort:** High. This is a separate follow-up, not required for the validation defect.

### 5. Repair historical rows before retrying

Extend doctor/upgrade repair to reconstruct or quarantine empty-content observations, similar to the merged title-repair work in #892.

- **Pros:** helps existing local data recover.
- **Cons:** empty content is not safely inferable in general; it does not prevent malformed third-party pushes or make the server response actionable; overlaps the broader product-repair work and should not be bundled into this server contract fix.
- **Effort:** High and data-dependent.

## Proposed scope and non-goals

### In scope

- Validate mutation-push legacy upserts before `InsertMutationBatch` using the canonical mutation rules, with no duplicated field policy if a shared chunkcodec seam is practical.
- Return HTTP 400 with the existing structured repairable payload convention (`error_class=repairable`, `error_code=payload_invalid`) and enough index/entity/field detail for repair or diagnosis.
- Reject the entire batch if any entry is invalid; do not call the store and do not return partial acknowledgements.
- Keep valid observation upserts and valid deletes backward-compatible.
- Add deterministic handler tests for empty observation content, valid observation acceptance, mixed valid/invalid atomic rejection, and structured error details. Update the obsolete minimal-legacy test to reflect the canonical upsert contract.
- Add or adjust focused chunkcodec tests only if a shared validator seam is introduced.

### Explicit non-goals

- No push-side dead-letter table, quarantine disposition, cursor advancement, or autosync ack redesign.
- No attempt to reopen, duplicate, or absorb issue #814; its title-repair behavior is already merged in PR #892.
- No automatic reconstruction of historical empty content.
- No changes to pull behavior, relation deferred replay, project authorization, pause policy, or transaction semantics.
- No broad rewrite of the legacy chunk protocol; retain its existing 400 parity as the reference behavior.

## Risks and mitigations

- **Compatibility risk:** callers relying on incomplete legacy upserts may receive 400 instead of 200. Mitigate by applying required-field checks only to supported upsert semantics already enforced by canonicalization, preserving deletes, and documenting the actionable error.
- **Rule drift risk:** duplicating required fields in `cloudserver` will recreate the defect. Prefer a shared chunkcodec validator or a narrowly scoped wrapper around the existing canonical normalization logic.
- **Response compatibility risk:** relation clients and tests rely on the current `invalid` list. Preserve that shape where possible, or add fields without removing existing ones; cover both relation and legacy errors.
- **Batch semantics risk:** accepting valid entries around an invalid one would violate current autosync assumptions. Reject before the store and test zero store calls plus zero persisted entries.
- **Ordering risk:** validation currently follows authorization and pause checks. Keep authorization/policy checks ahead of payload details so tenant and pause behavior do not leak or change; payload validation must still precede storage.
- **Residual outage risk:** a 400 still leaves a historical poison mutation pending, so autosync may remain degraded until repair. This is intentionally surfaced as a follow-up quarantine/recovery concern, not silently hidden by partial acceptance.

## Test strategy for proposal/design

Use table-driven handler tests with `t.Run` for supported entities and required-field cases:

1. Observation upsert with blank or whitespace-only `content` returns 400, `error_class=repairable`, `error_code=payload_invalid`, and identifies entry 0/entity/field.
2. Complete observation upsert returns 200 and is stored.
3. A batch containing one valid and one invalid observation returns 400, stores nothing, and returns no accepted sequences.
4. Observation delete with its identity fields but no content remains accepted.
5. Valid session, prompt, and relation behavior remains covered; malformed required fields return 400 rather than reaching cloudstore canonicalization.
6. A fake mutation store records zero `InsertMutationBatch` calls for every validation failure.
7. The existing cloudstore transaction rollback test remains green as defense in depth.
8. Remote transport tests verify the structured 400 is preserved as a repairable `HTTPStatusError`; autosync tests verify the current no-partial-ack behavior remains unchanged.

Run the narrow cloudserver/chunkcodec/remote/autosync/cloudstore suites first, then the repository commands from `openspec/config.yaml`: `go test ./...`, `go test -cover ./...`, `go test -tags e2e ./internal/server/...`, `go build ./...`, and `gofmt -l .` during apply/verify.

## Proposal-ready recommendation

Proceed with a focused server contract change: align `POST /sync/mutations/push` validation with the existing `chunkcodec` canonical mutation requirements, perform it before `InsertMutationBatch`, and emit the established actionable repairable 400 for the complete batch. Prefer a shared validation seam over a content-only check so the fix removes the entire known validation/canonicalization inconsistency without changing autosync acknowledgements or persistence semantics. Treat dead-letter/quarantine/cursor recovery as a separately proposed change informed by #503's independent freeze report.

**Ready for proposal:** Yes. The proposal should link #503, explicitly state that #814/#892 are completed and excluded, and define the compatibility decision for incomplete legacy upserts before implementation begins.

## CodeGraph status

CodeGraph was attempted first as required: `gentle-ai codegraph init --cwd /home/reaan/engram`. Initialization could not run because the upstream `codegraph` executable is absent from `PATH`. The structural investigation therefore used the repository's source, tests, git history, and GitHub state directly; no CodeGraph index was available.

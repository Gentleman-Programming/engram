# Proposal: Validate Cloud Mutation Pushes Before Storage

## Intent and User Value

Align `POST /sync/mutations/push` with `chunkcodec`'s canonical required-field rules. An incomplete legacy observation upsert currently becomes an opaque HTTP 500 and remains pending locally. The change returns a repairable HTTP 400 before storage while preserving local-first, all-or-nothing sync.

Delivery is tracked by umbrella issue #854; issue #503 is the target. Closed issue #814 and merged PR #892 (blank-title repair) are excluded.

## Scope and Compatibility Decisions

### In Scope
- Validate every supported mutation upsert before `InsertMutationBatch`, preferably through a shared `chunkcodec` seam.
- Return the established structured repairable 400 (`error_class=repairable`, `error_code=payload_invalid`) with offending index/entity/field detail.
- Reject the complete batch on any invalid entry: zero store call, persistence, or partial acknowledgement.
- Preserve valid observation/session/prompt/relation upserts and deletes; revise the obsolete minimal-legacy-upsert test.

### Out of Scope / Non-goals
- No dead-lettering, quarantine, cursor/ack redesign, or autosync recovery policy.
- No historical reconstruction, pull/deferred-relation, authorization/pause, or transaction changes.
- No #814/#892 title-repair work or issue reopening/duplication.

### Behavior and Compatibility

Canonical required fields apply to supported upserts, including non-blank observation content; deletes retain delete-specific rules. Authorization and pause checks remain ahead of payload details. Existing structured error fields remain compatible; detail is additive.

## Capabilities

### New Capabilities
None.

### Modified Capabilities
- `cloud-autosync`: tighten the mutation-push admission/error contract while retaining complete-batch acknowledgement semantics.

## Approach and Affected Boundaries

Reuse or expose normalization rules from `internal/cloud/chunkcodec`, invoke them in `internal/cloud/cloudserver/mutations.go` before the store boundary, and retain `internal/cloud/cloudstore` defense in depth. Do not change `internal/cloud/autosync` acknowledgements. Update handler, codec, transport, and autosync tests.

## Acceptance and Testing

- Blank/whitespace observation content and other canonical missing fields return structured 400; valid upserts/deletes return 200.
- Mixed valid/invalid batches return 400 with no `InsertMutationBatch` call, accepted sequences, or persisted subset.
- Handler tests cover success, errors, policy ordering, and structured response; codec, remote, and autosync tests cover shared rules, 400 details, and no partial ack.
- Run focused suites, then `go test ./...`, coverage, e2e, build, and `gofmt -l .`.

## Risks and Mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| Incomplete legacy clients receive 400 | Medium | Align only with canonical upsert semantics; preserve deletes and actionable diagnostics. |
| Rule drift or partial acceptance | Low | Shared validator plus zero-store-call atomicity tests. |
| Historical poison rows remain pending | Medium | Document as a separate recovery follow-up; do not silently skip data. |

## Rollback, Dependencies, and Open Decisions

Rollback is a single revert of the server/codec change; the existing store transaction remains intact. No external dependency or product decision is unresolved; implementation may choose the narrowest shared seam preserving this contract.

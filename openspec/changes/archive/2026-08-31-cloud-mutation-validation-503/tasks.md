# Tasks: Validate Cloud Mutation Pushes Before Storage

## Review Workload Forecast

| Field | Value |
|---|---|
| Estimated authored changed lines | 650–780 (generated goldens excluded) |
| 800-line session-budget risk | Medium |
| 400-line budget risk | High |
| Chained PRs recommended | Yes (forecast retained; explicit exception selected) |
| Delivery strategy | exception-ok |
| Chain strategy | not applicable — single existing PR #891 |
| Suggested split | Internal work-unit sequence only: codec seam; handler/API; remote/autosync verification |

Decision needed before apply: No
Chained PRs recommended: Yes
Chain strategy: not applicable — single existing PR #891
400-line budget risk: High
size:exception: explicitly authorized by the user
Review budget: 800 authored changed lines (unchanged)

**Delivery decision reconciliation:** The user explicitly selected and authorized `size:exception`. All work units may append to the existing PR #891; work-unit sequencing is internal and does not imply separate PRs. No chain strategy applies.

## Corrective Apply Reconciliation

Tasks 1.2, 2.2, and 2.3 remain complete because this bounded corrective run recorded fresh, authoritative Strict TDD RED/GREEN evidence for each task. The RED executions were produced by controlled temporary regressions after the new assertions were written; the intended candidate behavior was restored exactly before GREEN and remains the final behavior. Earlier unobservable historical RED claims are not reused.

### Suggested Work Units

| Unit | Goal | Delivery boundary | Focused test command | Runtime harness | Rollback boundary |
|---|---|---|---|---|---|
| 1 | Canonical validator and codec regressions | Existing PR #891 | `go test ./internal/cloud/chunkcodec` | N/A; pure codec seam | Revert `chunkcodec.go` and its tests |
| 2 | Ordered preflight and API contract | Existing PR #891 | `go test ./internal/cloud/cloudserver -run Mutation` | `httptest.Server` route cases | Revert `mutations.go`, constants, server tests |
| 3 | Client/autosync proof and release checks | Existing PR #891 | `go test ./internal/cloud/remote ./internal/cloud/autosync` | N/A; fake transport plus `httptest` | Revert remote/autosync tests only |

Commit guidance: one Conventional Commit per unit where useful; all work-unit commits append to PR #891. Work-unit sequencing is internal and does not imply separate PRs; keep tests with behavior, record commands/harness/rollback, and do not commit by file type.

## Phase 1: Regression-First RED Tests

- [x] 1.1 In `chunkcodec/chunkcodec_test.go`, add table-driven RED cases for every canonical session/observation/prompt/relation upsert field, blank values, non-object payloads, encoded payloads, and `entity_key` mismatch; cover valid relation and identity-only deletes.
- [x] 1.2 In `cloudserver/mutations_test.go`, add RED tests for indexed repairable 400s, all-invalid-entry reporting, mixed-batch zero `InsertMutationBatch`/ack, relation-delete rejection, and authorization→pause→validation ordering; replace invalid `makeMutationEntries` fixtures and the obsolete minimal-legacy-upsert expectation.
- [x] 1.3 Create `cloudserver/mutations_e2e_test.go` with `httptest.Server` assertions for 200 `accepted_seqs`, exact 400 envelope, hidden details on 403/409, and no persisted subset; add RED 400 parsing in `remote/transport_mutations_test.go` and no-ack repairable/short-push cases in `autosync/manager_test.go`.

## Phase 2: Smallest Production Seam

- [x] 2.1 In `chunkcodec/chunkcodec.go`, expose `ValidateMutationEntry(entity, op, entityKey string, payload json.RawMessage)` using existing normalizers, canonical field order, object/operation/delete rules, and identity consistency without changing chunk decoding behavior.
- [x] 2.2 In `constants/constants.go`, add the mutation wire `payload_invalid` separately from the existing chunk upgrade code; preserve additive `reason_code` compatibility.
- [x] 2.3 In `cloudserver/mutations.go`, retain raw bounded input for minimal project extraction, authorize every project, pause-gate every authorized project, then validate all entries before one store call; serialize `{index,entity,field}` and reject atomically.

## Phase 3: Verification and Boundaries

- [x] 3.1 Make canonical session/observation/prompt/relation upserts and supported deletes return existing 200 acknowledgements; keep `cloudstore.InsertMutationBatch` transaction defense in depth unchanged.
- [x] 3.2 Confirm remote parses repairable 400s and autosync never acknowledges failed/short pushes; do not change quarantine, cursor, pull, acknowledgement design, or recovery policy.
- [x] 3.3 Run focused suites, `go test ./...`, `go test -cover ./...`, `go test -tags e2e ./internal/server/...`, `go build ./...`, and `gofmt -l .`; accept pre-storage validation, atomic rejection, ordered policy errors, no #814/#892 work; threat rows are all N/A.

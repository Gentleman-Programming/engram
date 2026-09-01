# Apply Progress: Cloud Mutation Validation Before Storage

## Execution

- **Change:** `cloud-mutation-validation-503`
- **Mode:** Strict TDD
- **Artifact store:** Hybrid OpenSpec + Engram
- **Delivery strategy:** `exception-ok` with explicit `size:exception`
- **PR boundary:** Single existing PR #891 on `fix/delete-project-cross-project-sessions`; all work units may append to it. Work-unit sequencing is internal and does not imply separate PRs. No branch, PR, or push operations performed.
- **Current work unit:** Single bounded corrective run — authoritative Strict TDD evidence for tasks 1.2, 2.2, and 2.3; all prior implementation and Work Unit 1/3 evidence is preserved.
- **Corrective runtime token:** `sha256:b44b2b7d473508102c9a339386eb1169ddd587afbf62f00d2a79a4aaa0f59647`; no additional attempt was acquired or settled.

## Delivery-Decision Reconciliation

- **Authoritative decision:** The user explicitly selected and authorized `size:exception` for all remaining work.
- **Resolved delivery strategy:** `exception-ok`.
- **Chain strategy:** Not applicable — single existing PR #891.
- **Decision needed before apply:** No.
- **Review budget:** 800 authored changed lines, unchanged.
- **Routing:** All work units may append to PR #891; work-unit sequencing is internal and does not imply separate PRs.
- **Forecast preservation:** The original 650–780 authored-line forecast, High 400-line budget risk, and all work-unit boundaries are retained. The forecast's chained-PR recommendation remains a planning signal resolved by the explicitly authorized exception, not a separate-PR requirement.

## Completed Tasks

- [x] 1.1 Add table-driven codec regression cases for canonical upsert fields, blank values, object/encoded payloads, identity mismatch, valid relation, and identity-only deletes.
- [x] 1.2 Add cloudserver admission and policy-ordering regression cases for indexed repairable 400s, complete invalid-entry reporting, atomic mixed batches, relation-delete rejection, and authentication/authorization/pause/validation ordering.
- [x] 2.1 Expose `chunkcodec.ValidateMutationEntry` using the existing canonical normalizer while preserving chunk decoding compatibility.
- [x] 2.2 Add the mutation wire `payload_invalid` constant separately from the chunk upgrade code and preserve the existing chunk code.
- [x] 2.3 Add ordered cloudserver preflight, all-entry validation, indexed repair details, and atomic rejection before the single mutation-store call.
- [x] 1.3 Add network HTTP, remote transport, and autosync regression coverage for repairable mutation failures and acknowledgement safety.
- [x] 3.1 Verify canonical upserts and supported deletes retain successful acknowledgements while the cloudstore transaction defense remains unchanged.
- [x] 3.2 Verify remote repairable-error parsing and autosync no-ack behavior for failed and short pushes.
- [x] 3.3 Run focused suites, repository tests, coverage, e2e, build, formatting, and boundary checks.

## Remaining Tasks

- None — all planned implementation and verification tasks are complete.

## Strict TDD Cycle Evidence

| Task | Test File | Layer | Safety Net | RED | GREEN | TRIANGULATE | REFACTOR |
|---|---|---|---|---|---|---|---|
| 1.1 | `internal/cloud/chunkcodec/chunkcodec_test.go` | Unit | ✅ Existing package: 7/7 passed before edits | ✅ `go test ./internal/cloud/chunkcodec -count=1 -run 'TestValidateMutationEntry'` failed before production code with undefined `ValidateMutationEntry`/`MutationValidationIssue` | ✅ Contract tests passed after the seam was implemented | ✅ 52 table subcases cover all canonical fields, blank and non-string values, object rules, operations, deletes, relation compatibility, and key mismatch | ✅ Test helpers keep the table deterministic; final package tests and formatting checks passed |
| 2.1 | `internal/cloud/chunkcodec/chunkcodec_test.go` | Unit | ✅ Existing package: 7/7 passed before production edits | ✅ The same RED contract suite referenced the not-yet-existing exported seam | ✅ `go test ./internal/cloud/chunkcodec -count=1 -run 'TestValidateMutationEntry|TestCanonicalizeForProjectRetainsEncodedMutationPayloadCompatibility'` passed | ✅ Derived identity, typed-field reporting, and encoded chunk-payload compatibility exercise distinct paths beyond canonical valid/invalid cases | ✅ Final implementation is formatted and the full focused package remains green |
| 1.2 | `internal/cloud/cloudserver/mutations_test.go` | Integration (`httptest` handler) | ✅ `go test ./internal/cloud/cloudserver -count=1` exited 0 before the corrective test was written | ✅ After writing `TestMutationPushReportsAllInvalidEntriesWithRepairableEnvelope`, a controlled temporary `if !ok && len(invalid) == 0` regression produced an actual failure: the focused command exited 1 because only index 1 was reported instead of indexes 1 and 3 | ✅ Restored the intended `if !ok` admission loop and `go test ./internal/cloud/cloudserver -count=1 -run '^TestMutationPushReportsAllInvalidEntriesWithRepairableEnvelope$' -v` exited 0 | ✅ `TestMutationPushReportsAllInvalidEntriesPreservesInputOrderForOperationAndPayload` added and its focused command exited 0; operation and object failures exercise distinct paths | ✅ `gofmt -d` produced no output and the final mutation-focused command exited 0; the temporary production regression was fully restored |
| 2.2 | `internal/cloud/constants/constants_test.go` | Unit | ✅ `go test ./internal/cloud/constants -count=1 -v` exited 0 before the corrective assertion was written | ✅ After writing `TestMutationWirePayloadInvalidCodeIsStable`, temporarily removing the existing mutation constant made `go test ./internal/cloud/constants -count=1 -run '^TestMutationWirePayloadInvalidCodeIsStable$' -v` fail at compile time with `undefined: MutationErrorCodePayloadInvalid` (exit 1) | ✅ Restored `MutationErrorCodePayloadInvalid = "payload_invalid"`; the two mutation constant tests exited 0 | ➖ Structural constant contract; triangulation skipped under the strict-TDD exception because there is one exact mutation wire value, while the existing test preserves the chunk code and namespace separation | ✅ `gofmt -d` produced no output and `go test ./internal/cloud/constants -count=1 -v` exited 0 with 2 top-level tests |
| 2.3 | `internal/cloud/cloudserver/mutations.go` + `mutations_test.go` | Integration (`httptest` handler) | ✅ `go test ./internal/cloud/cloudserver -count=1` exited 0 before the corrective multi-project test was written | ✅ After writing `TestMutationPushChecksAllProjectPoliciesBeforeValidation`, a controlled temporary `if !enabled && false` regression produced an actual failure: the focused command exited 1 with expected 409 but received the validation 400 and `payload_invalid` details | ✅ Restored the intended `if !enabled` pause gate; the focused command exited 0 with HTTP 409 and no validation details | ✅ `TestMutationPushValidatesAuthorizedMultiProjectBatchAtomically` added; after correcting its fixture's expected field, the two-test command exited 0 and covered authorized multi-project atomic rejection | ✅ `gofmt -d` produced no output and the final mutation-focused command exited 0; no temporary production regression remains |
| 1.3 | `internal/cloud/cloudserver/mutations_e2e_test.go`, `internal/cloud/remote/transport_mutations_test.go`, `internal/cloud/autosync/manager_test.go` | E2E/network + integration | ✅ Existing cloudserver, remote, and autosync mutation suites passed before edits; new E2E file had no safety-net requirement | ✅ `go test ./internal/cloud/cloudserver -count=1 -run 'TestMutationPushHTTPServer' -v` initially failed on the new exact acknowledgement assertion because the existing fake sequence fixture generated non-contiguous values; no production defect was exposed | ✅ After correcting the test fixture, cloudserver E2E tests passed; remote repairable-400 and autosync failure/short-ack tests also passed | ✅ Network cases cover canonical upserts/deletes, exact repairable 400, hidden 403/409 details, and mixed-batch non-persistence; remote and autosync cases cover distinct failure paths | ✅ Test-only fixture sequencing was corrected, all changed Go files are formatted, and no production code was required |
| 3.1 | `internal/cloud/cloudserver/mutations_e2e_test.go` + `internal/cloud/cloudstore/project_controls_test.go` | E2E/network + integration | ✅ Existing cloudstore materialization and atomicity tests passed before edits | ✅ The new network acknowledgement test was written before the supporting fixture correction; its first run failed on the fixture's sequence generation | ✅ Network test passed with seven exact acknowledgements for session/observation/prompt/relation upserts and supported deletes; cloudstore atomicity/materialization tests passed | ✅ One batch exercises all supported canonical upsert entities plus identity-only deletes and verifies one storage call | ✅ No cloudstore production code changed; the fake store now assigns contiguous test sequence values deterministically |
| 3.2 | `internal/cloud/remote/transport_mutations_test.go` + `internal/cloud/autosync/manager_test.go` | Integration | ✅ Existing mutation transport and manager push suites passed before edits | ➖ Existing production behavior already satisfied the regression contract; the new tests passed immediately and no RED production failure was observed | ✅ Repairable 400 status fields and both repairable/short push no-ack cases passed | ✅ Separate transport parsing, repairable failure, and short acknowledgement scenarios exercise the client boundary without changing recovery policy | ✅ Tests remain table-driven and production remote/autosync code is unchanged |

## Test Summary

- **Cumulative tests written:** Work Unit 1 contains 3 codec test functions with 52 table subcases plus 2 compatibility/identity scenarios. Work Unit 2 adds 5 targeted cloudserver tests (including 4 indexed cases and 4 ordering cases), preserves the existing relation/legacy regression fixtures, and adds 1 constants contract test; the resumed run adds exact-one-store-call and chunk-code-preservation assertions. Work Unit 3 adds 4 network cloudserver tests, 1 remote repairable-400 test, and 1 table-driven autosync no-ack test. This corrective run adds 5 scoped regression test functions: 2 cloudserver tests for 1.2, 1 constants test for 2.2, and 2 cloudserver tests for 2.3.
- **Focused cloudserver result:** `go test ./internal/cloud/cloudserver -count=1 -run '^TestMutationPush(InvalidEntriesReturnIndexedRepairable400|ReportsAllInvalidEntries|ReportsAllInvalidEntriesWithRepairableEnvelope|ReportsAllInvalidEntriesPreservesInputOrderForOperationAndPayload|MixedBatchRejectsAtomically|RejectsRelationDelete|AdmissionOrdering|ChecksAllProjectPoliciesBeforeValidation|ValidatesAuthorizedMultiProjectBatchAtomically)$' -v` exited 0; 9 top-level tests and 8 table subtests passed.
- **Focused codec result:** `go test ./internal/cloud/chunkcodec -count=1 -run 'TestValidateMutationEntry|TestCanonicalizeForProjectRetainsEncodedMutationPayloadCompatibility' -v` exited 0; the canonical table (52 subcases) and 2 compatibility/identity tests passed.
- **Focused constants result:** `go test ./internal/cloud/constants -count=1 -v` exited 0; 2 top-level wire-constant tests passed.
- **Additional suite result:** `go test ./...` exited 0; all discovered packages passed, with only `internal/cloud/syncguidance` reporting no test files.
- **Quality checks:** `go vet ./internal/cloud/chunkcodec ./internal/cloud/cloudserver ./internal/cloud/constants` exited 0; `gofmt -l` over all changed Go files produced no output.
- **Repository verification:** `go test ./...`, `go test -cover ./...`, `go test -tags e2e ./internal/server/...`, `go build ./...`, and `git diff --check` all exited 0. Coverage included autosync 94.2%, cloudserver 78.4%, cloudstore 44.2%, and remote 82.9%; the repository has no configured positive threshold.
- **Layers:** Unit (codec/constants), integration via handler-level and network `net/http/httptest` (cloudserver), remote transport, autosync manager, and build-tagged server E2E.
- **Approval tests:** None — this work adds an admission seam and regression coverage rather than refactoring an existing API.
- **Pure validation seam:** `ValidateMutationEntry` is deterministic and side-effect free.
- **Formatting note:** Repository-wide `gofmt -l .` still lists six pre-existing unrelated files; all changed Go files are clean, and none of the listed files was modified by this change.
- **Vet note:** `go vet ./...` reports pre-existing unreachable code at `internal/project/detect.go:434`; the changed cloud packages pass focused vet, and `internal/project/detect.go` was not modified.

## Work Unit 1 Evidence

| Evidence | Result |
|---|---|
| Focused test command and exact result | `go test ./internal/cloud/chunkcodec -count=1` — exit 0; package `ok`, all 10 top-level tests passed, including 52 canonical table subcases. |
| Runtime harness command/scenario and exact result | `N/A` — Work Unit 1 is a pure `chunkcodec` seam; the HTTP `httptest.Server` boundary is assigned to Work Unit 2. Existing chunk canonicalization compatibility is covered by `TestCanonicalizeForProjectRetainsEncodedMutationPayloadCompatibility`. |
| Rollback boundary | Revert `internal/cloud/chunkcodec/chunkcodec.go` and `internal/cloud/chunkcodec/chunkcodec_test.go`; this removes the pre-storage seam and its codec regressions without touching cloudserver, constants, remote, autosync, quarantine, cursor, or historical-repair behavior. |

## Work Unit 2 Evidence

| Evidence | Result |
|---|---|
| Focused test command and exact result | `go test ./internal/cloud/cloudserver -count=1 -run 'Mutation' -v` — exit 0; every matching cloudserver test and table subtest passed. `go test ./internal/cloud/chunkcodec -count=1 -run 'TestValidateMutationEntry|TestCanonicalizeForProjectRetainsEncodedMutationPayloadCompatibility' -v` — exit 0; the canonical table and 2 compatibility/identity tests passed. `go test ./internal/cloud/constants -count=1 -v` — exit 0; the wire-constant contract passed. |
| Runtime harness command/scenario and exact result | `go test ./internal/cloud/cloudserver -count=1 -run '^TestMutationPush(InvalidEntriesReturnIndexedRepairable400|ReportsAllInvalidEntries|MixedBatchRejectsAtomically|RejectsRelationDelete|AdmissionOrdering)$' -v` — exit 0; 5 top-level handler-route tests and 8 table subtests passed through `CloudServer.Handler().ServeHTTP` with `httptest.NewRecorder`, covering indexed 400s, all-invalid reporting, atomic rejection/no acknowledgement, relation-delete rejection, and authentication/authorization/pause/validation ordering. Network `httptest.Server` coverage remains with pending task 1.3 and was not implemented in this work unit. |
| Rollback boundary | Revert only `internal/cloud/cloudserver/mutations.go`, `internal/cloud/cloudserver/mutations_test.go`, `internal/cloud/cloudserver/principal_auth_test.go`'s canonical fixture update, `internal/cloud/constants/constants.go`, and `internal/cloud/constants/constants_test.go`; this removes the mutation preflight/error contract and its related regressions without reverting the Work Unit 1 codec seam or touching remote, autosync, cloudstore transactions, quarantine, cursor, pull, or historical-repair behavior. |

## Work Unit 3 Evidence

| Evidence | Result |
|---|---|
| Focused test command and exact result | `go test ./internal/cloud/cloudserver -count=1 -run 'TestMutationPushHTTPServer' -v`, `go test ./internal/cloud/remote -count=1 -run 'TestMutationTransportPush(Repairable400|Accepted)' -v`, and `go test ./internal/cloud/autosync -count=1 -run 'TestManagerPushDoesNotAck(RepairableOrShortPush|WhenTransportFails|WhenAcceptedSeqCountMismatchesBatch)' -v` — all exited 0. The combined affected-cloud suite also exited 0. |
| Runtime harness command/scenario and exact result | The network `httptest.Server` cloudserver suite exited 0 with canonical session/observation/prompt/relation upserts, supported deletes, exact repairable 400, hidden 403/409 details, and mixed-batch no-persistence assertions. The remote suite exercised a real `MutationTransport` against an `httptest.Server`; autosync exercised its manager push path with deterministic fakes. |
| Rollback boundary | Revert `internal/cloud/cloudserver/mutations_e2e_test.go`, the added remote/autosync regression tests, the test-only contiguous-sequence fixture correction, and the aligned `DOCS.md` paragraph. This removes Work Unit 3 proof and documentation without reverting the Work Unit 1 codec seam or Work Unit 2 server contract. |

## Deviations and Issues

- **Deviations:** Production implementation follows the design. The validator requires the wire payload to be a JSON object, while the existing chunk normalizer continues accepting encoded payloads; that compatibility distinction is explicitly tested. Work Unit 3 adds the required network `httptest.Server` boundary. Remote/autosync production code remained unchanged because the existing client behavior already preserved structured repairable errors and refused failed/short acknowledgements. `DOCS.md` was aligned with the corrected mutation validation envelope because the API behavior changed.
- **Issues:** CodeGraph initialization was attempted but unavailable because the upstream `codegraph` executable is not on `PATH`; repository analysis used the required filesystem fallback. The earlier preserved partial Work Unit 2 RED executions remain historically unobservable and are not reused; this corrective run records fresh RED failures from controlled temporary regressions and restores the intended candidate. Repository-wide vet and formatting report unrelated pre-existing issues at `internal/project/detect.go:434` and six unchanged files listed by `gofmt -l .`; focused changed packages and tests are clean. No implementation blocker remains.

## Workload / PR Boundary

- **Mode:** `size:exception` within the existing single PR #891.
- **Boundary:** This corrective run starts from the completed implementation and prior Work Unit 1/2/3 state and ends after fresh evidence for tasks 1.2, 2.2, and 2.3. It adds only scoped regression assertions and reconciled artifacts; no branch, PR, linkage, push, or GitHub operation was performed. Sequencing does not imply separate PRs.
- **Estimated review impact:** The corrective run adds 5 test functions plus artifact evidence; cumulative authored changes remain above the default 400-line threshold and are covered by the explicitly user-approved `size:exception` within the unchanged existing PR #891 boundary.

## Previous Artifact-Only Correction Evidence

| Evidence | Result |
|---|---|
| Focused validation command and exact result | `git diff --check` — exit 0 on the current tracked diff; direct post-write reads of both OpenSpec files and Engram observations #2640/#2642 succeeded. No Go test command was run because no production code or tests changed. |
| Runtime harness command/scenario and exact result | `N/A` — this metadata-only reconciliation has no runtime boundary; no HTTP or other runtime harness was needed or run. |
| Rollback boundary | Restore only `openspec/changes/cloud-mutation-validation-503/tasks.md`, `openspec/changes/cloud-mutation-validation-503/apply-progress.md`, and Engram observations #2640/#2642; no production or test file rollback is needed. |

- **Run type:** Corrected persisted delivery-decision metadata only; no production code or tests were changed, no implementation task was rerun, and no new code task was marked complete.
- **Strict TDD:** Production RED/GREEN/TRIANGULATE/REFACTOR cycles were not rerun because no code changed; the original Work Unit 1 evidence remains unchanged.
- **Runtime attempt token:** `sha256:9d642f88125948c5a84d2b3c176d585e8fbb4836f2e83d2a3e459b2312b862d5`.
- **Attempt ownership:** No additional attempt was acquired or settled; the orchestrator owns settlement.
- **Task-state invariant at correction time:** Only 1.1 and 2.1 were complete; Work Unit 2 had not yet resumed.

## Resumed Work Unit 2 Run Evidence

- **Run type:** Continued from the maintainer-authorized runtime reset over preserved uncommitted changes; no reset, discard, duplicate implementation, branch, PR, linkage, push, or GitHub operation was performed.
- **Native attempt token:** `sha256:b9b98c6ca57b0728e065184fab70e2d41655c69be93d9749a25654cbe8fbd0b9`.
- **Attempt ownership:** No additional attempt was acquired or settled; the orchestrator owns settlement.
- **Task-state invariant:** Only 1.1, 1.2, 2.1, 2.2, and 2.3 are complete; 1.3, 3.1, 3.2, and 3.3 remain pending.

## Corrective Bounded Apply Run

- **Run type:** Single corrective Strict TDD apply run focused only on tasks 1.2, 2.2, and 2.3. The historical unobservable RED executions were not relabeled or reused.
- **Runtime token:** `sha256:b44b2b7d473508102c9a339386eb1169ddd587afbf62f00d2a79a4aaa0f59647`.
- **Attempt ownership:** No attempt was acquired or settled in this run.
- **Delivery:** `exception-ok` with explicit `size:exception`, existing PR #891 on `fix/delete-project-cross-project-sessions`, review budget 800. No branch, PR, push, or GitHub operation was performed.
- **Scope guard:** Five new regression test functions and the two hybrid SDD artifact updates were the only final changes from this run. Temporary controlled edits to `internal/cloud/cloudserver/mutations.go` and `internal/cloud/constants/constants.go` were restored exactly; no unrelated implementation was reset, discarded, duplicated, or changed.

### Corrective TDD Cycle Commands and Results

#### Task 1.2 — cloudserver structured all-invalid reporting

- **Safety net:** `go test ./internal/cloud/cloudserver -count=1` — exit 0; the existing cloudserver package passed before the corrective assertion was written.
- **RED:** After writing `TestMutationPushReportsAllInvalidEntriesWithRepairableEnvelope`, the current admission loop was temporarily constrained from `if !ok` to `if !ok && len(invalid) == 0`. `go test ./internal/cloud/cloudserver -count=1 -run '^TestMutationPushReportsAllInvalidEntriesWithRepairableEnvelope$' -v` — exit 1. Actual failure: `mutations_test.go:1453: invalid details: got [{Index:1 Entity:observation Field:content}], want [{Index:1 Entity:observation Field:content} {Index:3 Entity:prompt Field:session_id}]`.
- **GREEN:** The intended `if !ok` loop was restored as the minimum implementation. `go test ./internal/cloud/cloudserver -count=1 -run '^TestMutationPushReportsAllInvalidEntriesWithRepairableEnvelope$' -v` — exit 0; the new envelope, indexed details, no acknowledgement, and zero storage calls passed.
- **TRIANGULATE:** `go test ./internal/cloud/cloudserver -count=1 -run '^TestMutationPushReportsAllInvalidEntries(WithRepairableEnvelope|PreservesInputOrderForOperationAndPayload)$' -v` — exit 0; 2 top-level tests passed, covering observation/prompt canonical fields plus relation-operation and non-object payload failures.
- **REFACTOR:** `gofmt -d internal/cloud/cloudserver/mutations.go internal/cloud/cloudserver/mutations_test.go` — no output. The final mutation-focused suite remained green after formatting review.

#### Task 2.2 — mutation wire error-code compatibility

- **Safety net:** `go test ./internal/cloud/constants -count=1 -v` — exit 0; the existing wire-constant test passed before the corrective assertion was written.
- **RED:** After writing `TestMutationWirePayloadInvalidCodeIsStable`, the existing `MutationErrorCodePayloadInvalid` declaration was temporarily removed. `go test ./internal/cloud/constants -count=1 -run '^TestMutationWirePayloadInvalidCodeIsStable$' -v` — exit 1 at compile time with actual `undefined: MutationErrorCodePayloadInvalid` diagnostics in `constants_test.go` lines 6, 7, 12, and 18.
- **GREEN:** `MutationErrorCodePayloadInvalid = "payload_invalid"` was restored exactly. `go test ./internal/cloud/constants -count=1 -run 'TestMutation(PayloadInvalidErrorCodeIsWireSpecific|WirePayloadInvalidCodeIsStable)$' -v` — exit 0; 2 top-level tests passed.
- **TRIANGULATE:** Skipped under the strict-TDD structural-constant exception: there is one exact mutation wire value, and the existing preservation test already exercises the distinct chunk upgrade code.
- **REFACTOR:** `gofmt -d internal/cloud/constants/constants.go internal/cloud/constants/constants_test.go` — no output; `go test ./internal/cloud/constants -count=1 -v` — exit 0 with 2 top-level tests.

#### Task 2.3 — ordered multi-project preflight

- **Safety net:** `go test ./internal/cloud/cloudserver -count=1` — exit 0 before the corrective multi-project assertion was written.
- **RED:** After writing `TestMutationPushChecksAllProjectPoliciesBeforeValidation`, the pause branch was temporarily constrained from `if !enabled` to `if !enabled && false`. `go test ./internal/cloud/cloudserver -count=1 -run '^TestMutationPushChecksAllProjectPoliciesBeforeValidation$' -v` — exit 1. Actual failure: `mutations_test.go:1688: expected 409 for paused project, got 400 body="{\"error\":\"invalid mutation payload\",\"error_class\":\"repairable\",\"error_code\":\"payload_invalid\",\"invalid\":[{\"entity\":\"observation\",\"field\":\"content\",\"index\":0}],\"reason_code\":\"validation_error\"}"`.
- **GREEN:** The intended `if !enabled` pause gate was restored exactly. `go test ./internal/cloud/cloudserver -count=1 -run '^TestMutationPushChecksAllProjectPoliciesBeforeValidation$' -v` — exit 0; HTTP 409 was returned without validation details or storage calls.
- **TRIANGULATE:** `TestMutationPushValidatesAuthorizedMultiProjectBatchAtomically` was added for two active authorized projects. Its first run exposed a test-fixture expectation mismatch (the payload had blank `content`, not blank `session_id`); after correcting that fixture, `go test ./internal/cloud/cloudserver -count=1 -run '^TestMutationPush(ChecksAllProjectPoliciesBeforeValidation|ValidatesAuthorizedMultiProjectBatchAtomically)$' -v` — exit 0; 2 top-level tests passed.
- **REFACTOR:** `gofmt -d internal/cloud/cloudserver/mutations.go internal/cloud/cloudserver/mutations_test.go` — no output; the final mutation-focused suite remained green and no temporary production condition remains.

### Corrective Work Unit Evidence

| Evidence | Required result |
|---|---|
| Focused test command and exact result | `go test ./internal/cloud/cloudserver -count=1 -run '^TestMutationPush(InvalidEntriesReturnIndexedRepairable400\|ReportsAllInvalidEntries\|ReportsAllInvalidEntriesWithRepairableEnvelope\|ReportsAllInvalidEntriesPreservesInputOrderForOperationAndPayload\|MixedBatchRejectsAtomically\|RejectsRelationDelete\|AdmissionOrdering\|ChecksAllProjectPoliciesBeforeValidation\|ValidatesAuthorizedMultiProjectBatchAtomically)$' -v` — exit 0; 9 top-level tests and 8 table subtests passed. `go test ./internal/cloud/constants -count=1 -v` — exit 0; 2 top-level tests passed. |
| Runtime harness command/scenario and exact result | The cloudserver command exercised the real `CloudServer.Handler().ServeHTTP` path with `httptest.NewRecorder` for indexed repairable 400s, all-invalid reporting, atomic no-store/no-ack behavior, relation-delete rejection, authentication/authorization/pause/validation ordering, and authorized multi-project preflight; exit 0. The constants task is structural and has no separate runtime boundary; its unit command exited 0. Work Unit 3 network/remote/autosync tests were not rerun. |
| Rollback boundary | Remove only the five corrective test functions in `internal/cloud/cloudserver/mutations_test.go`, the corrective test in `internal/cloud/constants/constants_test.go`, and the corrective sections/notes in both SDD artifacts. Restore the pre-run versions of those artifact files if the evidence reconciliation is reverted. Do not revert `internal/cloud/cloudserver/mutations.go`, `internal/cloud/constants/constants.go`, or any Work Unit 1/2/3 implementation: their temporary RED conditions were restored and leave no final candidate change. |

### Corrective Final-State Invariant

- Tasks 1.2, 2.2, and 2.3 are `[x]` only because the fresh RED failures, GREEN passes, triangulation, and refactor checks above occurred in this run.
- The final production candidate is the preserved implementation: the all-invalid loop reports every issue, the mutation constant remains distinct from the chunk upgrade code, and policy gates precede full validation.
- No quarantine, dead-letter, cursor, pull, historical repair, recovery policy, #814, or #892 scope was added.

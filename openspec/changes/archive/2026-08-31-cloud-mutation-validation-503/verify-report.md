```yaml
schema: gentle-ai.verify-result/v1
evidence_revision: sha256:472e3380fbdf2895877cc6a19baa26b52e33d4f04df0f562058d565aaf502978
verdict: pass_with_warnings
blockers: 0
critical_findings: 0
requirements: 3/3
scenarios: 8/8
test_command: go test ./...
test_exit_code: 0
test_output_hash: sha256:8b8f0bfc9032a2cad03b2e1e7c9cafa6af96302a8005c8368ce70128d21fe243
build_command: go build ./...
build_exit_code: 0
build_output_hash: sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

## Verification Report

**Change**: cloud-mutation-validation-503  
**Version**: N/A — OpenSpec delta `cloud-autosync`  
**Mode**: Strict TDD  
**Date**: 2026-08-31

### Verification Scope

The completed implementation validates cloud mutation payloads before storage, reports indexed repairable errors, preserves valid upsert/delete acknowledgements, and keeps authorization and pause policy ahead of payload details. All work remains bounded to existing PR #891 on `fix/delete-project-cross-project-sessions`; no branch, PR, push, or GitHub state was changed. The explicitly user-approved `size:exception` is retained for the 800-line review budget under `exception-ok`; the forecasted 400-line risk is not a new verification blocker.

### Completeness

| Metric | Value |
|---|---:|
| Tasks total | 9 |
| Tasks complete | 9 |
| Tasks incomplete | 0 |
| Requirements total | 3 |
| Requirements complete | 3 |
| Scenarios total | 8 |
| Scenarios compliant | 8 |

All nine task checkboxes are `[x]` in `tasks.md`; `apply-progress.md` reports no remaining tasks. Task 3.3 is the command-only verification task and is evidenced by the executed checks below; the other eight implementation tasks have Strict TDD cycle rows.

### Build & Tests Execution

| Check | Exact command | Exit | Output hash | Result |
|---|---|---:|---|---|
| Focused affected packages | `go test ./internal/cloud/chunkcodec ./internal/cloud/cloudserver ./internal/cloud/constants ./internal/cloud/remote ./internal/cloud/autosync -count=1` | 0 | `sha256:361b08ff1445058e14550269bb0085144aad25071db24857eccaabe91197e192` | PASS; five packages |
| Full test runner | `go test ./...` | 0 | `sha256:8b8f0bfc9032a2cad03b2e1e7c9cafa6af96302a8005c8368ce70128d21fe243` | PASS; all discovered packages; `syncguidance` has no test files |
| Full coverage | `go test -cover ./...` | 0 | `sha256:cecdf4a4f6978bd1ecdc63e8a090bcbf52ae26ae59d70cf8614eec550e640b7c` | PASS |
| Requested server E2E suite | `go test -tags e2e ./internal/server/...` | 0 | `sha256:bb743e9b24ce56fb6193b5d3af83c20aeef86e4e0b45c34aa78111a6e489d1e9` | PASS |
| Build | `go build ./...` | 0 | `sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` | PASS; no output |
| Focused vet | `go vet ./internal/cloud/chunkcodec ./internal/cloud/cloudserver ./internal/cloud/constants ./internal/cloud/remote ./internal/cloud/autosync` | 0 | `sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` | PASS; changed cloud packages clean |
| Repository vet | `go vet ./...` | 1 | `sha256:4cd4b124700c9b89255ebd946724ccca6404df8f94d302f41e23d28bc64ce07d` | WARNING; only pre-existing `internal/project/detect.go:434:2: unreachable code` |
| Repository formatting | `gofmt -l .` | 0 | `sha256:f7cec4167cf5ca8904a87b9b51002d184ac14fe46110a7bdd7ced6eb83363ecd` | WARNING; six pre-existing unrelated files listed |
| Changed Go formatting | `gofmt -l internal/cloud/autosync/manager_test.go internal/cloud/chunkcodec/chunkcodec.go internal/cloud/chunkcodec/chunkcodec_test.go internal/cloud/cloudserver/mutations.go internal/cloud/cloudserver/mutations_test.go internal/cloud/cloudserver/mutations_e2e_test.go internal/cloud/cloudserver/principal_auth_test.go internal/cloud/constants/constants.go internal/cloud/constants/constants_test.go internal/cloud/remote/transport_mutations_test.go` | 0 | `sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` | PASS; no changed Go files listed |
| Tracked diff whitespace | `git diff --check` | 0 | `sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` | PASS |

The focused runtime evidence was rerun with `-count=1`; the configured full runner also completed successfully. No implementation test or build command failed.

```text
go test ./internal/cloud/cloudserver -count=1 -run '^TestMutationPush(InvalidEntriesReturnIndexedRepairable400|ReportsAllInvalidEntriesWithRepairableEnvelope|ReportsAllInvalidEntriesPreservesInputOrderForOperationAndPayload|MixedBatchRejectsAtomically|RejectsRelationDelete|AdmissionOrdering|ChecksAllProjectPoliciesBeforeValidation|ValidatesAuthorizedMultiProjectBatchAtomically)$' -v
exit 0; eight top-level tests and eight table subtests passed; all expected TestMutationPush cases executed
```

### Targeted Runtime Evidence

| Boundary | Exact command | Exit | Output hash | Runtime result |
|---|---|---:|---|---|
| Canonical codec seam | See fenced command below | 0 | `sha256:9c9f7ba0ae4c946b10f352c7b31df3c59ca9e7e9c6ecefbfe49e694e7516352e` | 52 canonical table subcases plus derived-key and encoded-chunk compatibility tests passed |
| Handler admission | See fenced command below | 0 | `sha256:49f326b07419f22d0cd54c8fa425ba17125cc772122173af608b987ff807e591` | Eight top-level tests and eight table subtests passed through `CloudServer.Handler().ServeHTTP`; all expected `TestMutationPush` cases executed |
| Network HTTP route | `go test ./internal/cloud/cloudserver -count=1 -run '^TestMutationPushHTTPServer' -v` | 0 | `sha256:fddf8ddf9453ddba05c451ea0d3d8c8a14446fdede40630e33dc377c538bb4af` | Four network tests, including policy subcases, passed against `httptest.Server` |
| Remote transport | See fenced command below | 0 | `sha256:4296f90b73aaa2fc97060890e45823bc21b3a69996f5008d72e29fc31394bd02` | Accepted sequences and structured repairable 400 parsing passed |
| Autosync acknowledgement safety | See fenced command below | 0 | `sha256:6851e2a884745df5a491e2ba4c6d32619d5e1ba7b6293ec1e7590897b560a2c23f` | Failed, short, empty, long, and nil acknowledgement cases passed without local ack |

```text
go test ./internal/cloud/chunkcodec -count=1 -run 'TestValidateMutationEntry|TestCanonicalizeForProjectRetainsEncodedMutationPayloadCompatibility' -v
exit 0; 52 canonical table subcases plus derived-key and encoded-chunk compatibility tests passed

go test ./internal/cloud/remote -count=1 -run '^TestMutationTransportPush(Accepted|Repairable400PreservesStructuredStatus)$' -v
exit 0; accepted sequences and structured repairable 400 parsing passed

go test ./internal/cloud/autosync -count=1 -run '^TestManagerPushDoesNotAck(RepairableOrShortPush|WhenTransportFails|WhenAcceptedSeqCountMismatchesBatch)$' -v
exit 0; failed, short, empty, long, and nil acknowledgement cases passed without local ack
```

### Coverage

The repository aggregate coverage output reported: autosync 94.2%, cloudserver 78.4%, cloudstore 44.2%, chunkcodec 82.8%, and remote 82.9%; no positive repository threshold is configured. Package-specific profiles were also generated for changed production files.

| Changed production file | Statement coverage | Uncovered ranges from profile | Rating |
|---|---:|---|---|
| `internal/cloud/chunkcodec/chunkcodec.go` | 82.8% (308/372) | Existing/error branches including L15-17, L22-23, L285-307, L337-341, L378-389, L396-412, L428-470, L483-484, L501-502, L519-520, L528-529, L566-567, L572-573, L592-593, L598-599, L637-638, L641, L648-657 | Acceptable |
| `internal/cloud/cloudserver/mutations.go` | 82.4% (112/136) | L89-90, L95-97, L119-121, L139-141, L164-166, L188-189, L212-214, L243-245, L273-274, L294-296, L324-333, L337-339, L359-361 | Acceptable |
| `internal/cloud/constants/constants.go` | No executable statements | N/A | Informational |

Coverage output hashes: chunkcodec profile report `sha256:f7bc96cf065b7dea5e8936be10c4df9a6bf2300849caccc49d77613ecc57be48`; cloudserver profile report `sha256:7359dc22b2bf785bb78558349b6360ff3fcbb61bce2d92de5dd7aead4a5b1f30`; constants profile report `sha256:31597fc59a9676e6d4b7ca8b879ad30cdd73d77f40b81ddaecc3d26c757188d5`.

### TDD Compliance

| Check | Result | Details |
|---|---|---|
| TDD Evidence reported | PASS | `apply-progress.md` contains the Strict TDD Cycle Evidence table. |
| All implementation tasks have tests | PASS | 8/8 TDD-scoped implementation task rows have test files; task 3.3 is command-only. |
| RED confirmed | PASS | All listed test files exist; 7 rows report written RED tests, and task 3.2 explicitly records RED as not applicable because existing behavior already satisfied the contract. |
| GREEN confirmed | PASS | Every listed GREEN command passed in the apply evidence and the corresponding current focused tests passed. |
| Triangulation adequate | PASS WITH NOTE | Distinct cases cover canonical fields, object/operation failures, indexed all-invalid reporting, policy ordering, network behavior, and ack safety; task 2.2 is a one-value structural constant and is documented as single-case. |
| Safety net | PASS WITH NOTE | Existing package safety nets are documented for modified production paths; the new network E2E file is correctly treated as new and has no prior-file safety-net requirement. |

**TDD Compliance**: 6/6 verification checks passed, with documented N/A/single-case exceptions.

### Test Layer Distribution

| Layer | Regression cases | Files | Tools |
|---|---:|---:|---|
| Unit | 56 | 2 | Go testing; codec table and constants contract |
| Integration | 26 | 2 | `httptest` handler plus fake autosync transport/store |
| E2E/network | 8 | 2 | `httptest.Server` and real HTTP client transport |
| **Total** | **90** | **6** | |

The distribution counts targeted regression cases/subcases, not every pre-existing test in modified files. The pure validator, handler, remote, and autosync tests are all backed by available Go tooling; no browser E2E tool is required for this HTTP API change.

### Assertion Quality

**Assertion quality**: PASS — all inspected changed/created Go test assertions call production code or a real HTTP boundary and verify status, fields, error envelopes, persistence/ack side effects, or canonical identities. No tautologies, ghost loops, orphan empty checks, smoke-only tests, CSS/internal-state coupling, or mock-heavy test files were found.

### Spec Compliance Matrix

| Requirement | Scenario | Runtime test evidence | Result |
|---|---|---|---|
| REQ-215 | Blank observation content is repairably rejected | `mutations_e2e_test.go > TestMutationPushHTTPServerReturnsExactRepairable400Envelope`; handler table `InvalidEntriesReturnIndexedRepairable400/observation content` | COMPLIANT |
| REQ-215 | Other canonical fields are enforced | `mutations_test.go > TestMutationPushInvalidEntriesReturnIndexedRepairable400/{session directory,prompt session id,relation marked by kind}`; codec table covers every required field missing/blank and non-string cases | COMPLIANT |
| REQ-215 | Complete relation remains compatible | `mutations_e2e_test.go > TestMutationPushHTTPServerAcceptsCanonicalUpsertsAndDeletes`; codec `valid relation upsert` | COMPLIANT |
| REQ-216 | Valid upserts and deletes are preserved | `mutations_e2e_test.go > TestMutationPushHTTPServerAcceptsCanonicalUpsertsAndDeletes` — seven exact accepted sequences for session/observation/prompt/relation upserts and supported deletes | COMPLIANT |
| REQ-216 | Mixed valid and invalid entries are atomic | `mutations_e2e_test.go > TestMutationPushHTTPServerRejectsMixedBatchWithoutPersistence`; handler `TestMutationPushMixedBatchRejectsAtomically` | COMPLIANT |
| REQ-217 | Structured details identify all offenders | `mutations_test.go > TestMutationPushReportsAllInvalidEntriesWithRepairableEnvelope`; input-order triangulation test; network exact-envelope test | COMPLIANT |
| REQ-217 | Authorization and pause precede payload validation | `mutations_e2e_test.go > TestMutationPushHTTPServerHidesValidationDetailsBehindPolicyErrors/{unauthorized project,paused project}`; handler ordering and multi-project tests | COMPLIANT |
| REQ-217 | Valid requests retain acknowledgement behavior | `mutations_e2e_test.go > TestMutationPushHTTPServerAcceptsCanonicalUpsertsAndDeletes`; full test runner and build also pass | COMPLIANT |

**Compliance summary**: 8/8 scenarios compliant; 3/3 requirements complete.

### Correctness (Static Evidence)

| Requirement | Status | Notes |
|---|---|---|
| REQ-215 canonical validation | Implemented | `chunkcodec.ValidateMutationEntry` centralizes supported entities, operation rules, native/encoded object payloads, canonical non-blank fields, typed field mapping, and entity-key consistency; the handler invokes it before storage. |
| REQ-216 delete compatibility and atomic admission | Implemented | Delete identity requirements are operation-specific; relation delete is rejected; all entries are scanned before the single `InsertMutationBatch` call, preventing partial storage or acknowledgement. |
| REQ-217 structured errors and policy ordering | Implemented | Mutation-specific `payload_invalid` is separate from chunk upgrade codes; the handler authorizes every project, pause-gates every authorized project, then emits indexed repairable details. |

### Coherence (Design)

| Decision | Followed? | Notes |
|---|---|---|
| Validation ownership | Yes | Exported `chunkcodec.ValidateMutationEntry` reuses the canonical normalizer; handler owns index and HTTP presentation. |
| Admission boundary | Yes | Full input-order validation precedes one storage call, with zero-store invalid-batch tests. |
| Wire compatibility | Yes | `{error_class, error_code, error, invalid}` plus additive `reason_code` is emitted; chunk upgrade code remains unchanged. |
| Policy ordering | Yes | Bounded project-only preflight authorizes all projects and pause-gates all before nested payload validation. |

### Scope and Boundary Checks

- `cloudstore.InsertMutationBatch` transaction defense in depth remains unchanged.
- Remote/autosync production behavior remains unchanged; tests prove structured 400 parsing and no acknowledgement on failed/short pushes.
- No quarantine, dead-letter, cursor, pull, historical repair, recovery-policy, #814, or #892 work was added.
- `DOCS.md` now documents the mutation validation envelope and exact wire codes.
- Existing PR #891 and branch `fix/delete-project-cross-project-sessions` remain the sole delivery boundary; the user-approved `size:exception` is explicit and recorded.
- The orchestrator-provided runtime attempt token was not acquired, settled, or replaced: `sha256:16a0f202574e0df83c147b468878e2a618714ae4723201905d05d711d33b16b9`.

### Issues Found

**CRITICAL**: None.  
**WARNING**:
1. `go vet ./...` exits 1 on pre-existing unreachable code at `internal/project/detect.go:434`; that file is unchanged and focused changed-cloud vet exits 0.
2. `gofmt -l .` exits 0 but lists six pre-existing unrelated files; every changed Go file is clean under the explicit changed-file formatting check.
3. CodeGraph initialization was attempted as required but the upstream `codegraph` executable is unavailable on `PATH`; structural inspection used the normal filesystem fallback. This is an environment limitation, not an implementation failure.
4. Package aggregate coverage for `internal/cloud/cloudserver` is 78.4%, below 80%; changed production file profile coverage is 82.4%. This is informational under the Strict TDD coverage rule and does not leave a required scenario unproven.

**SUGGESTION**: Keep the unrelated repository vet/format findings tracked separately, and install/enable CodeGraph before future structural verification if repository policy requires indexed exploration.

### Verdict

**PASS WITH WARNINGS** — all 9 tasks, 3 requirements, and 8 required scenarios are complete and have passing runtime evidence. The warnings are pre-existing repository/tooling or informational coverage limitations; no new implementation failure or required-scenario gap was found.

### Evidence Revision Preimage

```text
change=cloud-mutation-validation-503
mode=Strict TDD
requirements=3/3
scenarios=8/8
tasks=9/9
focused=sha256:361b08ff1445058e14550269bb0085144aad25071db24857eccaabe91197e192
all_tests=sha256:8b8f0bfc9032a2cad03b2e1e7c9cafa6af96302a8005c8368ce70128d21fe243
coverage=sha256:cecdf4a4f6978bd1ecdc63e8a090bcbf52ae26ae59d70cf8614eec550e640b7c
e2e=sha256:bb743e9b24ce56fb6193b5d3af83c20aeef86e4e0b45c34aa78111a6e489d1e9
build=sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
vet_all=sha256:4cd4b124700c9b89255ebd946724ccca6404df8f94d302f41e23d28bc64ce07d
gofmt_all=sha256:f7cec4167cf5ca8904a87b9b51002d184ac14fe46110a7bdd7ced6eb83363ecd
diff_check=sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
codegraph=unavailable:codegraph executable missing from PATH
```

## Structured Result

- **status**: success
- **executive_summary**: Strict TDD verification passed all nine tasks and all eight REQ-215/216/217 scenarios against current runtime tests. The final verdict is PASS WITH WARNINGS solely for pre-existing repository vet/format findings, unavailable CodeGraph tooling, and informational coverage context.
- **artifacts**: `openspec/changes/archive/2026-08-31-cloud-mutation-validation-503/verify-report.md`; Engram topic `sdd/cloud-mutation-validation-503/verify-report`
- **next_recommended**: pending PR issue-link action
- **risks**: Pre-existing `go vet ./...` and `gofmt -l .` findings; CodeGraph executable unavailable; no implementation blocker.
- **skill_resolution**: paths-injected — all requested SDD, strict-TDD, architecture, business-rule, API, testing, structure, docs, cultural, Go-testing, and work-unit guidance files were read directly; repository-local skill aliases were unavailable through the dynamic loader, so injected paths were used.
- **final_verdict**: PASS WITH WARNINGS
- **exact_verification_evidence**: The evidence revision is `sha256:472e3380fbdf2895877cc6a19baa26b52e33d4f04df0f562058d565aaf502978`; all command hashes and exits are recorded above and the exact report bytes are the persistence preimage.

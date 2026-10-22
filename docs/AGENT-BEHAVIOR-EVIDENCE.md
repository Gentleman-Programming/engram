# Agent behavior evidence, not a parity certificate

This matrix maps the Go `ProfileAgent` surface to representative evidence from
#1985, with a source-only review reconciliation after #2011 / #2014 below.
**Source-present is not executed**; neither a mock nor a source test establishes
installed-host or release compatibility. The [contract](contracts/agent-contract-v1.json)
remains `mcp-foundation`.

## Historical #1985 identity and evidence levels

- Source baseline and worker HEAD: `945750bfa321b0067c9d94d1ca3a03c333920353`.
  This delivery changes only tests/docs. Its exact final commit SHA and clean-tree
  freshness result belong in the owner's candidate verification record, outside
  this commit to avoid a self-referential hash. Results below do not certify later main.
- Observed worker runtime: Node `v24.14.0`, Go `go1.26.4 windows/amd64`.
  Parent independently verified Pi package `0.5.0` and local TypeBox `1.3.34`.
  The real-workflow sandbox uses schema/TUI stubs, not the installed Pi SDK.
- **R**: registered native tool against isolated real Go HTTP/store; public
  setup/readback only. **M**: mocked adapter/dispatch. **S**: source-present
  test, not freshly executed here. Artifact checking is structural, not behavioral.
- Reused audit: observation46036. #1902 / PR1905 is merged at
  `39a3ad3b5acd1c98723271eb192c7b62edadf582`
  (head `04bbf31d82503e859b150866e01bbbeb4ccfa45e`), included in the base.
  #1903 / PR1906 remains open/unmerged at head
  `8571e684ebb708d1ba1ef6347ad553937338a356`. Its PATH/integration
  qualification is **not** evidence for this candidate; none was imported.

## Post-merge review reconciliation

[PR #2014](https://github.com/Gentleman-Programming/engram/pull/2014) closed #2011
and merged as `b3c399a12a115f556ff95430cd1c3b2b4e065936`
(head `e55e2a9e401591b976623fcebfd323b6b83cb782`). Source readback at that merge
confirms that MCP `mem_review(action=mark_reviewed)` now reads the explicit
project, requires explicit/current project resolution and always calls
`MarkReviewedForProject`; the old global fallback is removed from this path.
The generated contract's `project_policy` and `target_rule` both describe this
confinement. Omitted-project `action=list` remains global.

The scoped regression sources are `internal/mcp/review_project_contract_test.go`,
`internal/server/review_project_contract_test.go` and
`internal/store/review_project_guard_test.go` (**S**). This reconciliation is
source/metadata readback, not a new execution of those tests. Candidate execution
and review evidence belong to #2014's exact-head qualification record; neither
that evidence nor the historical #1985 results automatically certify later main.

The review implementation discrepancy is resolved. Residual behavioral-proof
applicability and an aggregate verdict for an exact closure candidate remain
outstanding for #1347. The write-outcome, installed-host and published-pair limits
below remain explicit; no stronger artifact status or closure follows.

## Operation and rule map

Names below were read from the current Go artifact and test sources. This is a
snapshot, not an inventory generator or frozen-count acceptance gate. Live Go
registration is authoritative; `TestAgentContractInventoryAndMCPParity` and
`plugin/pi/test/agent-profile.test.mjs` dynamically protect the inventory.
Go paths below are relative to `internal/mcp/`; Pi paths to `plugin/pi/test/`.
All entries are **S** unless the execution section explicitly upgrades them.
Representative coverage does not assert every input/transport permutation.

| Current operation(s) | Rules and named source evidence | Remaining boundary |
| --- | --- | --- |
| `mem_search` | `search_compact_parity_test.go`: `TestSearchCompactTransportParity`; Pi `search-compact.test.mjs` | HTTP compact vs legacy envelopes differ from MCP |
| `mem_search_prompts` | `prompt_search_parity_test.go`: `TestPromptSearchTransportParity`, `TestPromptSearchStoreFailure`; Pi `prompt-search.test.mjs` | Historical content is not current authority |
| `mem_get_observation` | `observation_history_parity_test.go`: `TestObservationHistoryTransportParity`, `TestObservationHistoryPublicMCPValidation`; Pi `observation-history.test.mjs` | Bounded history/cursors, not automatic pagination |
| `mem_context` | `context_history_test.go`: `TestOlderHistoryMCPRealRendering`; `context_budget_test.go`: `TestMemContextBudgetTotalResultBound`, `TestMemContextBudgetNonPositiveFallsBackToDefault`; Pi `context-older-history.test.mjs` | Pi positive-integer model domain differs from MCP fallback |
| `mem_save` | `prompt_capture_parity_test.go`: `TestHandleSaveRejectsInvalidCapturePrompt`, `TestPromptCaptureCrossTransportDedupe`; `mcp_test.go`: `TestHandleSave_CandidatesReturned`; Pi `replay-recovery.test.mjs` | Capture defaults/dedupe and save-only receipt recovery, not universal retry |
| `mem_update` | `ownership_update_test.go`: `TestHandleUpdateOwnerAssertionIndependentOfProcessProject`; Pi `mem-update.test.mjs` | Ownership, exact errors/no-ops/history effects; no general rollback claim |
| `mem_consolidate` | `consolidate_test.go`: `TestConsolidateMCPPreviewAndConfirm`, `TestConsolidateMCPRefusals`, `TestConsolidateMCPChangedPreview`; Pi `agent-workflows.test.mjs` (**M**), `real-agent-workflows.test.mjs` (**R**) | Preview is read-only; confirmation requires exact Go plan |
| `mem_suggest_topic_key` | `mcp_test.go`: `TestHandleSuggestTopicKeyReturnsFamilyBasedKey`, `TestHandleSuggestTopicKeyRequiresInput`; Pi `topic-key-contract.test.mjs`, real-workflow vectors | Shared Go algorithm; no local fallback |
| `mem_save_prompt`, `mem_session_summary` | `mcp_test.go`: `TestHandlePromptContextStatsTimelineAndSessionHandlers`; `host_binding_test.go`: `TestHostBindingRuleAcrossOmittedSessionWriters`; Pi `native-host-lifecycle.test.mjs` | Host attribution/recovery inputs differ by transport |
| `mem_session_start`, `mem_session_end` | `mcp_test.go`: `TestHandlePromptContextStatsTimelineAndSessionHandlers`; `host_binding_test.go`: `TestEndedHostBindingRejectsOmittedSessionIDWithSoleActiveSession`; Pi `native-host-lifecycle.test.mjs` | Pi cannot end an arbitrary model-selected session |
| `mem_session_finalize` | `mcp_session_finalize_test.go`: `TestSessionFinalizeMCPAtomicRetry`, `TestSessionFinalizeMCPRejectedInputsNoWrites`; Pi real-workflow finalization | Original receipt identity on explicit retry; not auto-replay |
| `mem_current_project` | `project_transition_test.go`: `TestProjectTransitionFirstGitDetection`; Pi `native-tool-contract.test.mjs` | Canonical cwd projection; configured project may prevail |
| `mem_doctor` | `mcp_test.go`: `TestMemDoctorRegisteredAndReturnsEnvelope`, `TestMemDoctorUnknownProjectReturnsStructuredError`; Pi `doctor-caller-binding.test.mjs` | Caller binding is diagnostic, not registration |
| `mem_capture_passive` | `mcp_test.go`: `TestHandleCapturePassiveExtractsAndSaves`, `TestHandleCapturePassiveDefaultsSourceAndSession`; host-binding tests; Pi `passive-capture.test.mjs` | Parse/dedupe counts; no arbitrary destination identity |
| `mem_review` | `mcp_test.go`: `TestHandleReviewListAndMarkReviewed`, `TestHandleReviewMarkReviewedEnforcesResolvedProjectAtMutation`; scoped MCP/HTTP/store sources in the post-merge section (**S**); Pi `native-tool-contract.test.mjs` | Mark-reviewed requires an explicit or resolved current project, never a global fallback; omitted-project list remains global. The #2011 / #2014 source reconciliation is not fresh execution or complete cross-runtime certification |
| `mem_judge` | `mcp_judge_test.go`: `TestHandleJudge_HappyPath`, `TestHandleJudge_OptionalFieldsStayNull`, `TestHandleJudge_Idempotent_Overwrite` | Pi's omitted provenance uses host ID; batch omission does not. Complete real Pi single-judgment success/error proof remains unverified here |
| `mem_judge_many` | `mcp_judge_many_test.go`: `TestJudgeManyPersistenceIsolationSync`, `TestJudgeManyInvalidInputNoMutation`; Pi new real public-scan vector below | Independent item transactions, ordered outcomes, no batch rollback |
| `mem_compare` | `mcp_compare_test.go`: `TestHandleCompare_HappyPath`, `TestHandleCompare_NotConflictPersists`, `TestHandleCompare_Idempotency` | Public compare persists a judged relation, not a pending seed. Complete real Pi compare success/error proof remains unverified here |
| `mem_list_projects` | `mcp_test.go`: `TestMemListProjects_ReturnsProjectsWithStats`; Pi `native-tool-contract.test.mjs` | Inventory/read envelopes differ |
| `mem_pin`, `mem_unpin` | `mcp_test.go`: `TestHandlePinAndUnpinObservation`; Pi `native-tool-contract.test.mjs` | Unpin removes a pin, not an observation |

Public HTTP source counterparts include `internal/server/agent_workflows_test.go`
(`TestAgentWorkflow`, `TestAgentWorkflowAuth`), plus `TestSearchCompactHTTPContract`
and `TestObservationHistoryHTTPInputs` in the server suite. These remain **S**;
store-backed handler tests are not installed-host tests.

## New public judgment proof

`real Pi judgments persist successful and mixed outcomes from public scan seeds`
uses native saves followed by public `POST /conflicts/scan` with `apply:true`.
The deterministic JWT records establish at least two real pending relations;
no SQL/private-store seed, endpoint, production seam or external LLM is used.
`mem_compare` can persist a judged relation but is unnecessary for this seed.

- All-success batch: confidence zero survives and public relation readback is
  `compatible` / `judged`.
- Mixed batch: missing relation, committed `conflicts_with` at confidence one,
  invalid verb, then out-of-range confidence. Exact error strings and ordered
  indexes/IDs/statuses are asserted. Later failures do not undo the successful item.
- Public detail and stats verify pending decreases by two, judged increases by
  two, and both verdict counters increase by one. Full observation rows do not change.
- Empty-envelope rejection is HTTP 400 with exact text; prior persisted relation
  and counters remain unchanged. Nonsemantic scan counters are explicitly zero.
- Existing all-error batch, topic validation, consolidation authorization/preview
  and finalization receipt/lifecycle vectors remain, not replacements for these cases.

### Exactly two pending relations

The same public workflow judges any extra scan candidates before taking baseline
stats, then asserts exactly two pending relations. After judging those two, it
checks that Go omits the zero-valued `pending` key. The original direct comparison
reproduced `undefined !== 0`; the assertion now interprets only a nullish pending
count as zero with `?? 0`, retaining exact judged/verdict increments and no-write
assertions for rejected input. No private setup, new skip or runtime change is
introduced. Earlier execution results below retain their original snapshot scope;
the correction's exact candidate and results are recorded separately by the owner.

## Write outcomes: separate proofs, separate limits

| Stage | Evidence / missing proof |
| --- | --- |
| Pre-mutation rejection | New real batch envelope rejection and existing all-error vector protect public relation/observation state. Existing real consolidation unauthorized request leaves observations unchanged. These are operation-specific assertions |
| Successful mutation despite later item error | New mixed real batch proves one committed relation remains visible after two later rejected items; not an atomic batch or universal rollback |
| Actual Go post-mutation reload fault | **Unavailable through this authorized public fixture.** `JudgeRelation` commits then reloads; ordinary public calls do not deterministically fail that read. No private DB hook or production fault API is authorized. No synthetic proxy error is presented as a Go reload fault |
| Unknown delivery | Pi `agent-workflows.test.mjs`, `replay-recovery.test.mjs` and `timeout-semantics.test.mjs` provide **M**, exercised by the full Pi suite below. The timeout cases use a synthetic Node HTTP server/request counter, not committed Go-store readback. No new mock duplicates them; real-server lost-ack/fault injection remains missing |
| Save-only recovery | `replay-recovery.test.mjs`: `typed receipt miss authorizes one byte-identical replay to frozen canonical destinations`, `expired receipt lookup remains unknown without replay or another lookup`, `invalid committed receipt remains unknown instead of claiming a save` (**M/S**). Only authoritative save-result lookup permits the bounded observation-save exception; no general write replay |

## Execution and final scope

Tests/docs-only assertions already satisfied current production behavior: there is
no meaningful product RED and no runtime change to make green. The first focused
run and final worker rerun each passed all eight real-workflow tests (zero
failures/skips). Final `node --check plugin/pi/test/real-agent-workflows.test.mjs`
and `git diff --check` returned 0 with empty stdout/stderr; the final
`node --test plugin/pi/test/real-agent-workflows.test.mjs` returned 0 (8/8).
Other suites were not executed by that focused worker run. Parent qualification
subsequently recorded:

- `node --test plugin/pi/test/real-agent-workflows.test.mjs`: 0; eight passed,
  zero failed/skipped. The public successful/mixed judgment case ran, not skipped.
- `npm --prefix plugin/pi test`: initial exit 1; 634 total, 629 passed,
  three failed, two skipped. The new eighth workflow exposed literal seven-test
  expectations in `real-workflows-package.test.mjs`. This failed run stays failed.
- After updating those strict expectations to eight (no new skip conditions),
  `node --test plugin/pi/test/real-workflows-package.test.mjs`: 0; three passed,
  one prebuilt-binary opt-in skip. Syntax/whitespace checks returned 0.
- Corrected `npm --prefix plugin/pi test`: 0; **634 total, 632 passed, zero
  failed/cancelled, two skipped**. The skipped installed-host lifecycle needs
  `ENGRAM_PI_LIFECYCLE_CLI`; the packaged positive workflow needs
  `ENGRAM_PI_TEST_SERVER_BINARY`. Neither omitted proof is promoted to pass.
- Ten selected MCP/HTTP inventory, search, history and batch-judgment top-level
  tests passed without skips, with `GOPROXY=off GOSUMDB=off`, `-count=1 -v`.
  The exact selection command is in the owner record; this is not a full Go suite.
- Structural readback found each of the current 23 Go operations exactly once in
  the matrix and all 44 cited Go test functions in source. That is not 44 executed
  tests. Two probe failures were harness-only (an operation-map/array assumption
  and Windows pattern escaping), corrected without a generator or new gate.

The full Pi run executes runnable referenced Pi cases, preserving their **M/R**
proof levels. Other Go entries remain **S** unless selected in that recorded run.

`go run ./tools/agent-contract -check` returned 0 with empty stdout/stderr on the
worker checkout. The artifact was not regenerated. This does not erase the
historical #1347 focused Go exit 1 or certify a future candidate. Parent owns the
final clean LF-controlled checkout/candidate-specific freshness qualification.

At the #1985 snapshot, S2/S3 mapped and retained existing evidence; S4 added
successful/mixed public judgments; S5 retained missing reload/lost-ack proof;
S6/S7 required final owner qualification and candidate SHA; S8 kept
`mcp-foundation` and the then-existing review discrepancy. #2011 / #2014 resolved
that implementation discrepancy as recorded above, not the other proof limits.
No installed-host, published-pair, package compatibility, full-parity or parent
closure verdict follows. Continue with the [projection audit](PI-TOOL-PROJECTIONS.md)
and [contract authority](AGENT-CONTRACT.md), not stronger artifact status.

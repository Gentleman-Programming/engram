# Contribution policy transition

**PRE-CUTOVER / NOT ACTIVE.** This is staged target guidance, not current admission policy or authorization to change GitHub settings. The [active issue-first workflow](../CONTRIBUTING.md#contribution-workflow) still requires approved, assigned issues, issue references and PR type labels. Required-check producers must remain operational while their contexts are required.

The [accepted decisions](https://github.com/Gentleman-Programming/engram/issues/2120#issuecomment-6394230953) and [operational cutover runbook](https://github.com/Gentleman-Programming/engram/issues/2120#issuecomment-6394275706) in #2120 govern this transition. Administrative cutover requires an identified administrator and specific authorization; local preparation does not authorize push, PR, merge or settings changes.

## Target: implement an agreed contract directly; discuss contract decisions first

After verified cutover, a self-contained PR may correct or implement an already agreed contract without a compulsory prior issue. Discuss first when deciding how Engram should work. Scope and consequences decide the route, not a generic score or line count.

| Self-contained PR under the target policy | Prior design discussion under the target policy |
| --- | --- |
| Fix documented behavior, with a reproduction and regression test preserving the agreed contract | Change an incompatible CLI, API, wire or plugin contract, including a proposed bug fix that changes promised behavior |
| Correct docs or add tests for existing behavior | Decide new product behavior or consequential architecture |
| Refactor or optimize while preserving observable behavior, with relevant correctness/performance evidence | Change persistence formats, state ownership or sync/conflict semantics |
| Update a compatible dependency with applicable checks and dependency vetting | Change authentication/authorization or security boundaries; introduce major dependencies or consequential dependency changes |
| Implement a previously accepted design, linking its decision | Change release policy or project governance |

A short linked issue or Discussion recording the problem, proposed contract, relevant tradeoffs and accepted direction is enough; a separate RFC is not compulsory. Link that decision from the implementation PR. If compatibility or ownership is uncertain, resolve it with maintainers before implementation. Security-sensitive reports still follow [SECURITY.md](../SECURITY.md), not a public design thread.

Direct PR does not mean automatic acceptance: focused evidence, human review, technical checks, dependency/security safeguards and the merge queue remain. Release authorization and the [release policy](RELEASE-POLICY.md) are unchanged.

## Ordered cutover — administrator action remains pending

1. **Inventory before changing anything.** Use #2120's operational runbook to identify every producer and consumer of the retiring contexts, including native PR/merge-group jobs, legacy and refresh publishers, rerun routers/adapters, policy helpers, mixed-purpose workflows and residual waits. Record which responsibilities remain technical or security-critical. Identify branch-name and commit-message consumers separately; do not infer that a metadata-check retirement removes them.
2. **Remove only three required names, with producers still running.** An identified, specifically authorized administrator removes only `Check Issue Reference`, `Check Issue Has status:approved` and `Check PR Has type:* Label` from required checks. Do not remove their producers first, fabricate passing checks, bypass the queue or weaken unrelated rules.
3. **Verify effective protection.** Confirm the effective rules no longer wait for those three contexts, while `Unit Tests`, `E2E Tests`, `Plugin Tests`, the active merge queue and unrelated technical/security protections remain intact. Verify PR and merge-group paths and residual consumers/waits; record live evidence in #2120. Local documentation checks are not hosted cutover proof.
4. **Only then retire solely dependent producers.** Retire jobs/helpers used exclusively by the removed requirements after verified removal. For mixed-purpose workflows, retain technical, queue and security responsibilities and resolve residual waits before removing obsolete paths. Update active contributor guidance only with the verified cutover, not during this preparation.

No fixture cleanup, branch-regex change or Conventional Commit enforcement change is included now. Branch-name freedom is a target only after all consumers are resolved and separately authorized controls are changed. The target release convention applies Conventional Commit semantics to the final squash commit; its enforcement mechanism remains unresolved. Existing branch and commit rules remain active until an explicit, verified transition.

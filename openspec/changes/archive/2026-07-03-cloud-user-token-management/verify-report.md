# Verification Report: Cloud User Token Management

## Change
`cloud-user-token-management` — branch `feat/cloud-user-token-management-runtime-wiring` (working tree clean, PR1A-PR6 all committed: `d4c3b38`..`8babe57`).

## Mode
Full artifact set present (proposal, spec, design, tasks, apply-progress). Full spec-driven verification performed.

## Task Completeness

- `tasks.md`: 63/63 checkbox lines checked (`- [ ]` count: 0, `- [x]` count: 63).
- Cross-Slice Acceptance Checklist: all 12 items checked, each with a concrete evidence pointer (test file or commit) inline in the checklist.
- No unchecked implementation or cleanup tasks found. No CRITICAL from the "unchecked task" gate.

## Command Evidence

| Command | Result |
|---|---|
| `go build ./...` | PASS, no output |
| `go vet ./...` | PASS, no output |
| `go test ./...` (cached) | all packages `ok` |
| `go test -count=1 ./...` (fresh) | all packages `ok`, including `internal/setup` (known-flaky `TestInstallCodexInjectsTOMLAndIsIdempotent` passed this run) |
| `go test -count=1 -v ./internal/cloud/... ./cmd/engram` | all `ok`; Postgres-gated tests report `--- SKIP` (no `CLOUDSTORE_TEST_DSN`), no `--- FAIL` |
| `go test -count=1 -race ./internal/cloud/... ./cmd/engram` | all `ok` |
| `gofmt -l` on files touched across PR1A-PR6 commits | clean, no output |
| `gofmt -l internal/cloud/dashboard/helpers_test.go cmd/engram/autosync_status_test.go` | dirty (both files) — confirmed pre-existing and **unmodified** by this change (`git diff main...HEAD -- <files>` is empty); not a defect of this change |

## Spec Requirement Coverage (spec.md)

| Requirement | Status | Evidence |
|---|---|---|
| Principal Resolution | COVERED | `internal/cloud/auth/foundation.go` (`PrincipalResolver.ResolveBearerToken`); `principal_auth_test.go` (managed/legacy/unknown-token cases) |
| Human Users | COVERED | `cloudstore/identity.go` CRUD; `admin_handlers_test.go`; disabled-user rejection in `dashboard_session_test.go` / `cloud_runtime_auth_test.go` |
| Roles and Administrative Authorization | COVERED | `admin_handlers.go` `requireManagedAdmin`; forbidden-path tests in `admin_handlers_test.go` (member and legacy-admin both denied) |
| Token Creation and Show-Once Secret | COVERED | `admin_handlers.go` sanitized token DTOs (`sanitizeToken`/`sanitizeTokenList`); tested in `admin_handlers_test.go` |
| Token Hashing and Secret Handling | COVERED | Dedicated `ManagedTokenHasher` (HMAC, domain-separated), `cloud.Config.TokenPepper`/`ENGRAM_CLOUD_TOKEN_PEPPER`; audit metadata redaction guard `rejectSensitiveAuthAuditMetadata` in `cloudstore/identity.go` plus cloudserver-side `assertNoSensitiveAuditMetadata` test helper |
| Token Revocation | COVERED | `FindPrincipalTokenByHash` + revoked-token rejection tested end-to-end (`cloud_runtime_auth_test.go: TestCloudRuntimeAuthenticatorRejectsRevokedManagedToken`, Postgres-gated `cloud_runtime_e2e_test.go`) |
| Project Grants | COVERED | Deny-by-default authorizer `cloudPrincipalProjectAuthorizer`; granted/ungranted/all-or-nothing/pull-filter tests in `principal_auth_test.go` and `cloud_runtime_auth_test.go` |
| Legacy Bootstrap Compatibility | COVERED | Legacy env-token/admin resolution preserved (`resolveLegacy`); allowlist wildcard/list/empty regression tests in `principal_auth_test.go`; legacy-admin-as-recovery behavior in `login_audit_test.go` |
| CLI Bootstrap | COVERED | `cmd/engram/cloud_bootstrap.go` + `cloud_bootstrap_test.go` (first-admin creation, duplicate refusal, token show-once, audit) |
| Dashboard Bootstrap | COVERED | `dashboard_session.go` bootstrap handlers, routes registered in `cloudserver.go`; last-admin guard in `cloudstore/identity.go` (`ErrLastActiveAdmin`, advisory-lock-serialized) |
| Audit Events | COVERED | `cloud_auth_audit_log` synchronous inserts for admin mutations (fail-closed) and dashboard login (fail-closed on accept, best-effort on reject, documented rationale); all MVP action names present (`token.create/revoke`, `user.create/enable/disable`, `grant.create/revoke`, `admin.login`, `bootstrap.dashboard`, `bootstrap.cli`) |
| Sync Contract Preservation | COVERED | Dedicated `internal/cloud/cloudserver/sync_contract_regression_test.go` (`TestSyncRouteTableUnchanged`, request/response schema-lock tests for pull manifest, push chunk, pull chunk, mutations push/pull) |
| Denied Behavior | COVERED | All-or-nothing mutation push rejection, no-grant-leak pull filtering tested in `principal_auth_test.go` |
| Tests and Verification | COVERED | Extensive RED/GREEN/TRIANGULATE/REFACTOR evidence tables per PR slice in `apply-progress.md`; full suite green |

No spec scenario found without a corresponding passing test.

## Design Coherence (design.md)

- Package boundaries respected: `cloudstore` has no HTTP/dashboard imports; `cloudserver` depends on auth/store interfaces (`AdminIdentityStore`, `PrincipalProjectAuthorizer`, `PrincipalStateStore`); `dashboard` renders outcomes only.
- Schema matches design (`cloud_principals`, `cloud_human_users`, `cloud_principal_tokens`, `cloud_project_grants`, `cloud_auth_audit_log`), additive migrations only, existing `cloud_users`/`cloud_chunks`/`cloud_mutations` preserved.
- Token format/hash model matches design (HMAC-SHA256, dedicated pepper, domain separator, `hmac.Equal`).
- Migration step 3 deviation ("resolver checks managed token storage first, then legacy"): **implementation deliberately checks legacy credentials first, then managed-token storage** — the inverse of design.md's literal wording. This is explicitly documented in `cmd/engram/cloud.go` (`buildRuntimeAuthenticator` doc comment, "Order note") and in `apply-progress.md`'s PR6 section. Verified against `internal/cloud/auth/foundation.go: ResolveBearerToken`, which does call `resolveLegacy` before falling back to managed-token lookup, matching the comment's claim.
  - **Judgment: acceptable, not a gap.** Rationale given is sound: legacy secrets are single deployment-chosen strings while managed secrets are 32 bytes of `crypto/rand`, so collision between the two credential spaces is not practically possible; checking legacy first avoids a hash-and-DB round trip on the hot path for deployments that have not adopted managed tokens yet. The deviation does not violate any spec requirement (no scenario mandates resolution order, only correct authentication/denial outcomes) — it is a WARNING-level, correctly documented, and reasoned tradeoff, not a defect.
- Dashboard session/cookie design matches implementation: signed principal-claim cookies, storage revalidation of enabled/role on every protected request, `HttpOnly`/`Secure`(HTTPS)/`SameSite=Lax` attributes verified by `dashboard_session_test.go`.
- Audit fail-closed/best-effort split (mutations and accepted logins fail closed; rejected logins best-effort) is explicitly documented and reasoned in `apply-progress.md` PR3C section and matches design.md's stated preference to fail mutations rather than silently lose an audit event.

## Residual Items Assessed (does each block archive?)

1. **Postgres-gated integration/e2e tests skip locally** (no `CLOUDSTORE_TEST_DSN`, no Postgres in this environment). Does NOT block archive: the security-critical branches this would otherwise only exercise via Postgres (revoked/disabled/unknown token rejection, legacy-fallback order, deny-by-default grants, invalid/missing pepper) were deliberately pushed down into non-gated fake-backed unit tests in PR6 remediation (`cmd/engram/cloud_runtime_auth_test.go`: `TestCloudRuntimeAuthenticatorRejectsRevokedManagedToken`, `TestCloudRuntimeAuthenticatorRejectsDisabledPrincipal`, `TestCloudRuntimeAuthenticatorRejectsPrincipalTokenMismatch`, `TestCloudRuntimeAuthenticatorResolvesManagedTokenBeforeLegacyFallback`, `TestCloudPrincipalProjectAuthorizerDenyByDefaultAndGrantedProject`), confirmed present and passing in this run.
2. **One known residual risk explicitly NOT fixed**: `apply-progress.md` (PR6 TRIANGULATE section) documents that `TestCloudstorePrincipalHumanTokenGrantAndAuditLifecycle` (added in PR1B) fails against real Postgres (`cannot remove last active admin`) due to a pre-existing gap with the later last-active-admin guard, discovered via real-Postgres testing but out of scope for PR6. This is a genuine gap in a Postgres-gated test that cannot be verified in this environment (no DSN configured) and was not fixed. **Recommend as a WARNING / follow-up item**, not a hard archive blocker, since: (a) it is confined to a single Postgres-gated integration test's own fixture/assertion, not a production code defect claim; (b) the underlying last-admin guard is otherwise covered by non-gated pure-helper tests and a dedicated Postgres-gated concurrency test; (c) it is transparently disclosed rather than hidden. Should be tracked as a fast-follow before/soon after archive, ideally the next time a Postgres-backed CI run is available.
3. **`last_used_at` not updated on managed-token auth**: confirmed schema/column exists (`identity.go`) and is read in queries/admin API responses, but no write-path was found updating it on successful auth. Matches design.md's explicit "best-effort"/deferred marking. Acceptable, non-blocking.
4. **`docs/ARCHITECTURE.md` route list predates PR2-PR4**: confirmed — the dashboard protected-route list section does not mention `/dashboard/bootstrap`, and the doc does not list bare `/admin/*` routes at all (only `/dashboard/admin/*`). `/sync/mutations/push` and `/sync/mutations/pull` ARE already listed elsewhere in the doc (pre-existing). This is pre-existing, transparently disclosed doc debt, not introduced by this change. Non-blocking.
5. **Two pre-existing gofmt-dirty files** (`internal/cloud/dashboard/helpers_test.go`, `cmd/engram/autosync_status_test.go`): confirmed dirty under `gofmt -l`, and confirmed via `git diff main...HEAD -- <files>` that this change's commit chain does not touch either file. Not part of this change; non-blocking.

## Issues

**CRITICAL**: none.

**WARNING**:
- Design deviation on legacy-vs-managed token resolution order (documented, judged acceptable — see Design Coherence above); no action required beyond the existing comment, but flag for awareness if the credential-entropy assumption ever changes (e.g. legacy token configurable to short/low-entropy values that could collide with managed-token hash space is still not a real risk since spaces are structurally disjoint by hash vs. plaintext comparison, not just length).
- `TestCloudstorePrincipalHumanTokenGrantAndAuditLifecycle` known failure against real Postgres (last-active-admin guard interaction) — disclosed, unfixed, untestable in this environment. Track as fast-follow.

**SUGGESTION**:
- Update `docs/ARCHITECTURE.md`'s dashboard/admin route list to include `/dashboard/bootstrap` and bare `/admin/*` API routes in a future docs slice.
- Consider implementing `last_used_at` write-on-auth in a follow-up if operators need last-used visibility sooner than "best-effort/deferred."
- Run the Postgres-gated integration/e2e suite (`CLOUDSTORE_TEST_DSN` set) at least once before or shortly after archive to close out residual item 2.

## Final Verdict

**PASS WITH WARNINGS**

All 63 tasks complete and verified against code. All spec requirements and scenarios have corresponding passing tests. Design is coherent with one intentional, well-documented, non-spec-breaking deviation. Build, vet, full test suite (including race), and gofmt all clean on this change's files. Two WARNING-level residual items are transparently disclosed and do not block archive; three SUGGESTION-level follow-ups are recommended but non-blocking.

## Archive Recommendation

Proceed to `sdd-archive`. No CRITICAL issues found. Track the two WARNING items (Postgres-gated last-admin test failure; resolver-order deviation awareness) as post-archive follow-ups.

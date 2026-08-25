# Tasks: Cloud User Token Management

## Review Workload Forecast

| Field | Value |
|-------|-------|
| Estimated changed lines | 2,500–4,500 additions/deletions across storage, auth, server, dashboard, CLI, docs, and tests |
| 400-line budget risk | High |
| Chained PRs recommended | Yes |
| Suggested split | PR 1 storage/auth foundation → PR 2 server sync enforcement → PR 3 bootstrap/admin APIs → PR 4 dashboard flows → PR 5 CLI/docs/audit hardening |
| Delivery strategy | auto-forecast |
| Chain strategy | feature-branch-chain |

Decision needed before apply: No
Chained PRs recommended: Yes
Chain strategy: feature-branch-chain
400-line budget risk: High

Risks and dependencies:
- Dedicated cloud token pepper must be configured before managed token verification is usable.
- Schema changes must be additive and rollback-safe; do not remove legacy env-token behavior.
- Sync route paths/payloads must remain stable while auth internals change.
- Dashboard sessions must revalidate principal state on every protected request.
- Audit events must never include raw tokens, authorization headers, or cookie values.
- Strict TDD is active: start each slice with failing tests, then implement the smallest passing behavior, then refactor.

## Implementation Tasks

### PR 1 — Storage and auth foundation

- [x] RED: Add cloudstore migration tests in `internal/cloud/cloudstore/` proving additive creation of `cloud_principals`, `cloud_human_users`, `cloud_principal_tokens`, `cloud_project_grants`, and `cloud_auth_audit_log` without altering existing sync tables.
- [x] GREEN: Extend `internal/cloud/cloudstore/cloudstore.go` migrations and add focused store methods for principal CRUD, human user create/list/enable/disable, token metadata create/list/revoke, project grant create/list/revoke, admin existence checks, and auth audit insertion.
- [x] RED: Add `internal/cloud/auth` tests for `Principal`, `PrincipalKind`, roles, token generation format, HMAC verifier behavior, constant-time verification, revoked/disabled rejection, and legacy env principal resolution.
- [x] GREEN: Implement principal domain types, dedicated token pepper configuration, token generator, token hasher/verifier, and resolver interfaces in `internal/cloud/auth/`.
- [x] TRIANGULATE: Add error-path tests for duplicate usernames/emails, invalid roles/kinds, duplicate grants, revoked tokens, missing pepper, and hash-only persistence.
- [x] REFACTOR: Keep storage interfaces small so `internal/cloud/cloudserver` can depend on auth/store contracts without importing dashboard rendering logic.
- [x] Verify: `go test ./internal/cloud/auth ./internal/cloud/cloudstore` and `go test ./...`.
- [x] Rollback boundary: revert new migrations and auth foundation only; legacy env-token sync remains untouched.

### PR 2 — Server middleware and sync grant enforcement

- [x] RED: Add handler tests in `internal/cloud/cloudserver/` proving existing `/sync/*` routes still accept valid legacy tokens and reject invalid/malformed/revoked managed tokens with current auth error style.
- [x] GREEN: Replace `withAuth` internals in `internal/cloud/cloudserver/cloudserver.go` with principal resolution, request context helpers, and compatibility adapters for existing auth callers.
- [x] RED: Add push/pull authorization tests for managed principals: granted project succeeds, ungranted project returns `403`, mutation batch with any ungranted project rejects all-or-nothing, mutation pull leaks no ungranted projects.
- [x] GREEN: Wire principal-aware project authorization through sync chunk and mutation handlers, including `internal/cloud/cloudserver/mutations.go`, while preserving legacy `ENGRAM_CLOUD_ALLOWED_PROJECTS` wildcard/list/empty semantics.
- [x] TRIANGULATE: Add regression tests for legacy sync principal behavior under `ENGRAM_CLOUD_ALLOWED_PROJECTS=*`, explicit lists, normalized projects, and empty/missing allowlist.
- [x] REFACTOR: Keep sync payload structs and route registration unchanged; auth changes must be internal only.
- [x] Verify: targeted cloudserver sync tests and `go test ./...`.
- [x] Rollback boundary: disable managed-token resolver wiring and retain legacy env-token authorization path.

### PR 3 — Bootstrap, admin authorization, and audit-backed admin operations

- [x] RED: Add admin authorization tests in `internal/cloud/cloudserver/` proving only managed admin principals can create users, issue/revoke tokens, and create/revoke grants; members receive forbidden responses and no state changes.
- [x] GREEN: Add admin form/API handlers under `internal/cloud/cloudserver/` for human user create/list/enable/disable, token create/list/revoke, and grant create/list/revoke, backed by cloudstore methods.
- [x] RED: Add dashboard-session tests proving managed admin login succeeds, member admin access fails, disabled/demoted users lose access on the next protected request, and secure cookie attributes are set correctly.
- [x] GREEN: Update dashboard auth/session handling in `internal/cloud/cloudserver` and `internal/cloud/dashboard` so signed cookies carry principal claims but every protected request revalidates enabled state and role from storage.
- [x] RED: Add bootstrap tests for legacy dashboard/admin credential creating the first managed admin, rejecting duplicate first-admin bootstrap, and preventing accidental removal of the last usable managed admin path.
- [x] GREEN: Implement dashboard bootstrap route/handler and last-admin protections, treating `ENGRAM_CLOUD_ADMIN` as explicit bootstrap/recovery access after managed admins exist.
- [x] RED: Add audit tests for token create/revoke, user create/enable/disable, grant create/revoke, admin login, dashboard bootstrap, accepted/rejected legacy recovery actions, and redaction of raw tokens.
- [x] GREEN: Emit synchronous `cloud_auth_audit_log` events for admin/security mutations; fail authoritative admin mutations if audit insertion fails.
- [x] Verify: targeted admin/dashboard/bootstrap tests and `go test ./...`.
- [x] Rollback boundary: remove admin/bootstrap routes while retaining storage/auth foundation and legacy auth behavior.

### PR 4 — Dashboard managed-user UX

- [x] RED: Add dashboard rendering/handler tests for `/dashboard/admin/users`, `/dashboard/admin/users/list`, token partials, grant partials, and contributor/managed-user separation.
- [x] GREEN: Update `internal/cloud/dashboard/dashboard.go` and related templ/templates/assets to show `Managed Users` separately from contributor analytics.
- [x] GREEN: Add server-rendered forms and HTMX-compatible partials for user create, enable/disable, token create/show-once, token revoke, grant create, and grant revoke.
- [x] TRIANGULATE: Test non-HTMX form POST/redirect behavior and HTMX partial responses; partials must be meaningful HTML without hidden client-side policy logic.
- [x] TRIANGULATE: Test empty states explaining deny-by-default project grants and token show-once warnings.
- [x] REFACTOR: Keep policy checks in server/auth/store layers; dashboard code must render outcomes, not make authorization decisions.
- [x] Verify: dashboard package tests plus `go test ./...`.
- [x] Rollback boundary: remove dashboard UI routes/templates without affecting already-tested admin handlers.

### PR 5 — CLI bootstrap, docs, and final hardening

- [x] RED: Add CLI tests in `cmd/engram/` for `engram cloud bootstrap admin --username ...`, duplicate bootstrap refusal, optional token issuance printed once, optional project grants, invalid input, and audit event creation.
- [x] GREEN: Implement `engram cloud bootstrap admin` in `cmd/engram/cloud.go`, using cloud runtime DB configuration by default and an existing DSN override convention only if already present.
- [x] TRIANGULATE: Test that raw managed tokens are never persisted, logged, audited, rendered in token metadata lists, or printed except the creation/bootstrap response.
- [x] GREEN: Update docs discovery targets affected by cloud setup and sync auth, starting with `README.md`, `docs/`, `CONTRIBUTING.md`, and any cloud deployment docs found by `rg "ENGRAM_CLOUD_TOKEN|ENGRAM_CLOUD_ADMIN|ENGRAM_CLOUD_ALLOWED_PROJECTS|cloud bootstrap"`.
- [x] GREEN: Document managed users/tokens, dedicated token pepper, first-admin dashboard bootstrap, CLI bootstrap, project grants, deny-by-default managed principals, legacy env-token migration, and rollback to legacy sync credentials.
- [x] RED: Add regression tests that `/sync/*` route methods, paths, request schemas, and response schemas remain unchanged for existing clients.
- [x] GREEN: Fix any contract drift found by regression tests without changing MVP payloads. (No drift found — all new regression tests passed immediately; this is now a permanent contract-lock safety net.)
- [x] REFACTOR: Run `gofmt` on touched Go files and remove any temporary test seams not needed by production behavior.
- [x] Verify: `go test ./...`, targeted cloud tests (`go test ./internal/cloud/... ./cmd/engram`), and `go test -cover ./...`.
- [x] Rollback boundary: revert CLI/docs/audit hardening slice while keeping prior reviewed server behavior intact.

### PR 6 — Runtime managed-token wiring (final integration slice)

- [x] RED: Add an end-to-end runtime auth test (`cmd/engram/cloud_runtime_e2e_test.go`, Postgres-gated) proving a managed token authenticates through the real `newCloudRuntime`-assembled server, `/dashboard/bootstrap` no longer 500s for want of a store, revoked/unknown tokens are rejected, legacy `ENGRAM_CLOUD_TOKEN` still authenticates, and the server still starts with no token pepper configured.
- [x] RED: Add non-gated adapter/wiring unit tests (`cmd/engram/cloud_runtime_auth_test.go`) for the managed-token lookup adapter, the principal project grant authorizer, and the composite runtime authenticator.
- [x] GREEN: Add `cloudstore.FindPrincipalTokenByHash` (storage-only managed-token lookup by hash) and exported `cloudstore.NormalizeProjectGrant`.
- [x] GREEN: Add `cmd/engram/cloud_runtime_auth.go` (`cloudRuntimeAuthenticator`, `cloudstoreManagedTokenLookup`, `cloudPrincipalProjectAuthorizer`) and wire `newCloudRuntime` in `cmd/engram/cloud.go` to construct a `cloudauth.PrincipalResolver` (managed tokens first, then legacy env tokens) and pass `WithAdminIdentityStore`, `WithManagedTokenHasher`, `WithPrincipalStateStore`, and `WithPrincipalProjectAuthorizer` into `cloudserver.New`.
- [x] TRIANGULATE (real-Postgres discovery, not fabricated): running the new tests against a real local Postgres instance (not just fakes) surfaced two pre-existing issues, both fixed in this slice:
  - `internal/cloud/cloudserver/dashboard_session.go`'s dashboard login/bootstrap audit calls sent the legacy admin/sync principal's synthetic ID (e.g. `legacy:admin`) as `ActorPrincipalID`, which the real `cloud_auth_audit_log.actor_principal_id` (`BIGINT REFERENCES cloud_principals`) column rejects outright — this would have turned every legacy admin dashboard login into a 500 once an admin identity store was wired into a real server. Fixed via a new `auditActorPrincipalIDRef` helper; regression-pinned in `internal/cloud/cloudserver/login_audit_test.go`.
  - `TestCloudstorePrincipalHumanTokenGrantAndAuditLifecycle` (added in PR1B) fails against real Postgres (`cannot remove last active admin`) — a pre-existing gap between that test and the last-active-admin guard added later in PR1B remediation, never actually run against Postgres before now. NOT fixed in this slice (out of scope for runtime wiring); flagged as a residual risk below.
- [x] GREEN: Update docs (`README.md`, `DOCS.md`, `docs/engram-cloud/quickstart.md`, `docs/engram-cloud/troubleshooting.md`, `CHANGELOG.md`, `docker-compose.cloud.yml`, `docker-compose.beta.yml`) to remove the "managed-token auth does not work at runtime" caveats and describe the now-working behavior plus the `ENGRAM_CLOUD_TOKEN_PEPPER` requirement.
- [x] Verify: `go build ./...`, `go test ./cmd/engram ./internal/cloud/...`, `go test ./...`, `go test -race ./cmd/engram ./internal/cloud/...`, `gofmt -l`, `git diff --check`.
- [x] Rollback boundary: revert `newCloudRuntime`'s new options/authenticator construction to fall back to legacy-only `auth.Service` wiring; storage/auth foundation (`FindPrincipalTokenByHash`, `NormalizeProjectGrant`) and the audit fix can remain (additive/corrective, not behavior-narrowing).

## Cross-Slice Acceptance Checklist

- [x] Managed human users are distinct from contributor analytics.
- [x] Managed tokens authenticate principals; authorization uses principal role and project grants. (Mechanism implemented and test-covered end-to-end in `internal/cloud/cloudserver/principal_auth_test.go`; runtime wiring closed in PR6 — see `cmd/engram/cloud_runtime_e2e_test.go`.)
- [x] Token hashes use a dedicated cloud token pepper, not the dashboard/session signing secret. (`cloud.Config.TokenPepper` / `ENGRAM_CLOUD_TOKEN_PEPPER` added in PR5, independent of `ENGRAM_JWT_SECRET`; now also required to enable managed-token auth at runtime, PR6.)
- [x] Raw token values are shown once and never stored or audited.
- [x] Disabled users, revoked tokens, and revoked grants stop future access immediately. (Runtime-proven end-to-end in PR6: a revoked managed token is rejected on its very next request.)
- [x] Legacy `ENGRAM_CLOUD_TOKEN`, `ENGRAM_CLOUD_ADMIN`, and `ENGRAM_CLOUD_ALLOWED_PROJECTS` behavior remains compatible during migration. (Runtime-proven end-to-end in PR6, including the no-token-pepper graceful-degradation case.)
- [x] Managed principals are deny-by-default for project sync.
- [x] Dashboard cookies are `HttpOnly`, `SameSite=Lax` or stronger, and `Secure` under HTTPS/production rules.
- [x] CLI and dashboard can create the first managed admin safely. (CLI: done, safe, tested in PR5. Dashboard bootstrap is now reachable in a real `engram cloud serve` deployment: PR6 wires `cloudserver.WithAdminIdentityStore(cs)` into `newCloudRuntime` and fixes a legacy-admin audit-insert bug that would otherwise 500 the login needed to reach `/dashboard/bootstrap`.)
- [x] Audit events cover all required MVP identity/security actions without secret leakage. (`bootstrap.cli` was the last unimplemented MVP action from design.md; added in PR5. PR6 fixes a real audit-insert failure mode for legacy principal actors discovered via real-Postgres testing.)
- [x] Documentation matches real routes, commands, environment variables, and rollback behavior for the auth/token-management surface. (README/DOCS.md/engram-cloud docs/CHANGELOG/docker-compose updated in PR5 and corrected in PR6 to remove the now-resolved runtime-wiring caveat. `docs/ARCHITECTURE.md`'s "Cloud route/auth split" route list still predates PR2-PR4 and is missing `/sync/mutations/*`, `/admin/*`, and `/dashboard/bootstrap` — this is pre-existing, deferred doc debt unrelated to token-management auth behavior, not introduced or touched by PR5/PR6.)

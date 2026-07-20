# Exploration: Cloud User Token Management

## Summary

Engram Cloud currently uses static token configuration rather than first-class managed users. The cloud server accepts a single bearer sync token (`ENGRAM_CLOUD_TOKEN`) and optionally a separate dashboard admin token (`ENGRAM_CLOUD_ADMIN`). Project access is enforced through global runtime allowlists such as `ENGRAM_CLOUD_ALLOWED_PROJECTS`, not through per-user grants.

The existing enforcement points are centralized enough to evolve toward database-backed users, tokens, roles, and project grants without rewriting sync or dashboard routing.

## Related GitHub issues

| Issue | State | Relevance |
| --- | --- | --- |
| [#348](https://github.com/Gentleman-Programming/engram/issues/348) `feat(cloud): replace global cloud tokens with managed users and per-user sync tokens` | Closed, `status:needs-review` | Exact match for managed users + per-user sync tokens. Closed as design-gated, not rejected. |
| [#404](https://github.com/Gentleman-Programming/engram/issues/404) `feat(cloud): add standalone DB-backed admin policy for tokens and projects` | Open, `status:stale` | Related foundation for DB-backed token/project policy without full managed users. |
| [#274](https://github.com/Gentleman-Programming/engram/issues/274) `feat(cloud): support project-scoped bearer tokens for multi-project teams` | Open, `status:needs-review` | Narrower scoped-token proposal; overlaps project grants and server-side enforcement. |
| [#349](https://github.com/Gentleman-Programming/engram/issues/349) `feat(cloud): pluggable external URL-based authentication with configurable response contract` | Open, `status:stale` | Adjacent external-auth direction; should remain out of MVP unless explicitly chosen. |

The planning should treat #348 as the closest product intent, while borrowing #404's DB-backed policy foundation and #274's project-scope concerns. The prior maintainer comments explicitly asked for a consolidated design covering identity, token rotation/revocation, audit, migration from global tokens, and server-side enforcement.

## Current model

| Area | Current behavior |
| --- | --- |
| Sync authentication | Clients send `Authorization: Bearer <token>` using the configured cloud token. |
| Dashboard authentication | Dashboard login exchanges a bearer/admin token for a signed dashboard cookie. |
| Admin status | Admin status is derived from the configured admin token/session checks. |
| Project permissions | Project scope is controlled by global allowlists and enrollment controls. |
| Managed users | `cloud_users` exists as a legacy/minimal table, but it is not the active auth source. |
| Token lifecycle | There is no first-class create/revoke/list token lifecycle per user. |

## Source evidence

- `internal/cloud/auth/auth.go`
  - Single expected bearer token authentication.
  - Optional dashboard admin token/session validation.
  - Project allowlist authorizer.
- `internal/cloud/cloudserver/cloudserver.go`
  - Sync routes are wrapped by bearer auth.
  - Dashboard login exchanges bearer/admin token for signed cookie.
  - Admin status depends on token-derived checks.
- `internal/cloud/cloudserver/mutations.go`
  - Mutation push authorizes every project in a batch.
  - Mutation pull filters by enrolled projects provider.
- `internal/cloud/cloudstore/cloudstore.go`
  - Existing schema includes `cloud_users`, `cloud_chunks`, `cloud_mutations`, `cloud_project_controls`, and `cloud_sync_audit_log`.
  - It does not provide active user/token/permission tables for auth enforcement.
- `cmd/engram/cloud.go`
  - Runtime configuration uses `ENGRAM_CLOUD_TOKEN`, `ENGRAM_CLOUD_ADMIN`, and `ENGRAM_CLOUD_ALLOWED_PROJECTS`.
  - Client `cloud.json` stores `server_url` and `token`.
- `internal/cloud/remote/transport.go`
  - Clients send bearer tokens for chunk and mutation sync.
- `internal/cloud/dashboard/dashboard.go`
  - `/dashboard/admin/users` currently lists contributors, not managed user accounts.

## Key findings

1. The product has two static secret strings, not managed identities.
   - Normal sync token: `ENGRAM_CLOUD_TOKEN`.
   - Optional dashboard/admin token: `ENGRAM_CLOUD_ADMIN`.

2. Project boundaries are global, not user-specific.
   - Current allowlists apply at the server/runtime level.
   - There is no per-user project grant model.

3. Server-side enforcement points already exist.
   - `withAuth` can resolve a bearer token into a principal instead of a boolean.
   - Dashboard auth can resolve a user/session with admin capability.
   - `authorizeProjectScope` and mutation pull filtering can enforce per-user grants.

4. The data model needs first-class auth entities.
   - Users.
   - Token records with hashed token secrets.
   - Roles/capabilities.
   - Project grants.
   - Audit events for token/user/permission changes.

5. Backward compatibility matters.
   - Existing `ENGRAM_CLOUD_TOKEN` and `ENGRAM_CLOUD_ADMIN` deployments should keep working during migration.
   - Legacy env tokens can act as bootstrap/root credentials until database-backed users are configured.

## Initial design pressure

The target model should preserve Engram's local-first story: local stores remain the source of truth for memories, while cloud owns organization-wide security controls. Because permissions are an org/cloud policy, enforcement must be server-side and deterministic.

The most likely architecture is:

1. Add an auth identity layer in `internal/cloud/auth` that returns a principal.
2. Add cloudstore persistence for users, token hashes, roles, project grants, and audit events.
3. Update cloudserver middleware and project authorization to use principals.
4. Add admin API/dashboard flows to create users, issue/revoke tokens, and manage project grants.
5. Keep env-token compatibility as bootstrap/legacy access.

## Risks and open questions

- Whether permissions should be role-based only, project-grant based only, or both.
- Whether tokens belong to users, service accounts, or both from the first slice.
- How to expose token values safely: show once, store only hashes.
- How to migrate existing deployments without locking out admins.
- How to keep dashboard user management distinct from current contributor/user analytics.

## Recommended next phase

Move to proposal/PRD questions before writing the proposal. The proposal should decide:

- first-slice scope;
- user and token model;
- role/capability vocabulary;
- project grant behavior;
- bootstrap/legacy token migration;
- admin dashboard/API requirements;
- audit expectations.

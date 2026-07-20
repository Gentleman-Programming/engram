# Proposal: Principal-Based Cloud User and Token Management

Engram Cloud should replace its global environment-token operational model with managed principals, hashed tokens, project grants, and runtime admin controls. This change consolidates the managed-user goal from #348, the DB-backed token/project policy foundation from #404, and the project-scoped token need from #274 into one coherent product direction, while keeping external authentication from #349 explicitly out of the MVP.

## Intent

Today, cloud authentication is based on static deployment secrets: one sync bearer token, an optional dashboard admin token, and global project allowlists. That model is simple, but it becomes hard to operate safely once a cloud instance has multiple humans, multiple projects, or credentials that need to be revoked without rotating the whole deployment.

The target direction is a principal-based auth model:

- A **principal** is the authenticated actor.
- `principal.kind` must support `human` and `service_account` from the start.
- The first MVP implements real human users first.
- Service accounts are non-human automation principals and are schema/model-ready, but their full lifecycle can land after the first slice.

This preserves Engram's local-first model: local SQLite remains the source of truth for memories, while cloud owns organization-wide security controls for who may sync which projects.

## Product goals

- Replace shared global sync credentials with per-principal sync tokens.
- Store token secrets only as hashes; raw token values are shown only once at creation.
- Enforce project access server-side through principal project grants.
- Provide runtime admin controls for creating humans, issuing/revoking tokens, and assigning project access.
- Preserve existing `/sync/*` route and payload contracts for clients.
- Keep existing environment admin/sync tokens working as bootstrap/legacy credentials during migration.
- Avoid locking out existing admins during rollout.
- Prepare the domain model for future service accounts and external auth without implementing them in the MVP.

## Scope

### In scope for the first product slice

| Area | Decision |
| --- | --- |
| Principals | Add a principal model with `kind = human | service_account`; implement human lifecycle first. |
| Human users | Admins can create, list, disable, and manage human users. |
| Tokens | Admins can issue, list metadata for, and revoke per-principal sync tokens. Tokens are hashed at rest. |
| Project grants | Managed principals are deny-by-default for projects. Admins can grant or revoke project access per principal, and sync authorization checks those grants. |
| Roles | MVP roles are simple: `admin` and `member`. Project grants carry project-level access; more granular roles are deferred. |
| Bootstrap/legacy | `ENGRAM_CLOUD_TOKEN` and `ENGRAM_CLOUD_ADMIN` keep working as bootstrap/legacy credentials. |
| Sync compatibility | Existing `/sync/*` routes and payloads remain compatible; only auth resolution changes. |
| Auditability | MVP audit events are required for token create/revoke, user create, user enable/disable, grant create/revoke, admin login, and bootstrap actions. |
| Dashboard/API alignment | Dashboard controls must map to real server-side business rules, not presentation-only toggles. |

### Out of scope for MVP

- External URL-based authentication from #349.
- OAuth/OIDC/SAML/social login.
- Full service-account dashboard lifecycle, unless required to keep the data model coherent.
- Token self-service flows for non-admin users.
- Fine-grained memory-level permissions.
- Changes to `/sync/*` request or response payload schemas.
- Replacing local-first storage semantics with cloud as the source of truth.
- Removing environment-token compatibility in the same slice.

## Affected areas

| Area | Expected impact |
| --- | --- |
| `internal/cloud/auth` | Resolve bearer tokens into principals instead of simple boolean authentication. Preserve env-token compatibility as bootstrap/legacy resolution. |
| `internal/cloud/cloudstore` | Persist principals, human user records, token hashes, project grants, roles/capabilities, and audit events. |
| `internal/cloud/cloudserver` | Enforce principal-aware auth for dashboard/admin and `/sync/*` routes without changing sync payload contracts. |
| Sync mutation/chunk authorization | Authorize push and pull by principal project grants, while preserving current route behavior. |
| Dashboard | Distinguish managed human users from contributor analytics; expose admin controls for humans, tokens, and project grants. |
| CLI/config/docs | Document legacy env-token bootstrap behavior and managed-token migration path. |
| Tests | Cover token resolution, revocation, grant enforcement, bootstrap compatibility, and admin lockout prevention. |

## Business rules

1. **Local-first remains non-negotiable.** Cloud controls access and replication policy; it does not become the canonical memory store.
2. **Server-side policy is authoritative.** Project grants and token revocation must be enforced by the cloud server, not trusted to clients.
3. **Tokens are credentials, not identities.** A token authenticates a principal; policy decisions are made from the principal and its grants/capabilities.
4. **Raw token values are never stored.** Store only hashed token secrets and token metadata.
5. **Revocation must be immediate for new requests.** A revoked token must stop authorizing subsequent sync/admin requests.
6. **Admins must not be locked out during migration.** Existing environment admin/sync tokens remain valid bootstrap/legacy credentials until a deliberate deprecation path exists.
7. **Project access must be deterministic.** If a principal lacks a grant for a project, sync should fail clearly rather than silently dropping data.
8. **Managed users are not contributor analytics.** Dashboard user-management screens must not conflate human accounts with observed contributors in synced data.
9. **The model must not block service accounts.** Even if MVP UI focuses on humans, persistence and auth should support `principal.kind = service_account` later without a schema rewrite.
10. **External auth is an integration layer, not the core identity model.** #349 can map future external identities to principals, but it should not define the MVP's internal auth source.

## Bootstrap and legacy behavior

Existing deployments may have only `ENGRAM_CLOUD_TOKEN`, `ENGRAM_CLOUD_ADMIN`, and runtime project allowlists. The migration path must preserve access:

- `ENGRAM_CLOUD_ADMIN` continues to authenticate admin bootstrap actions.
- `ENGRAM_CLOUD_TOKEN` continues to authenticate legacy sync clients.
- Legacy credentials should preserve their existing external behavior, but internally resolve to bootstrap/legacy principals so downstream enforcement can use one principal-aware path.
- The legacy sync principal is constrained by the existing `ENGRAM_CLOUD_ALLOWED_PROJECTS` semantics, including current wildcard, empty, and normalized-project behavior.
- Admins can create the first managed human admin and issue managed sync tokens without taking the server offline.
- The first managed admin can be created through either the legacy dashboard bootstrap path or an explicit CLI bootstrap command, so headless/self-hosted deployments are not forced through a browser-only setup.
- Legacy credentials should be documented as migration aids, not the long-term operational model.
- The MVP must not remove, invalidate, or silently reinterpret existing env-token behavior in a way that breaks current deployments.

## Success criteria

- Admins can create a managed human user and issue a sync token for that user.
- The server stores only token hashes and metadata, never raw token secrets.
- A managed token can sync only projects granted to its principal.
- Revoking a token prevents future requests authenticated by that token.
- Existing env admin/sync tokens continue working during migration.
- `/sync/*` route paths and payload contracts remain backward-compatible.
- Dashboard/admin UI labels and APIs clearly separate managed users from contributor analytics.
- Tests prove successful sync, denied sync, revoked-token behavior, and bootstrap compatibility.
- Documentation explains the migration path from env tokens to managed users/tokens.

## Risks

| Risk | Mitigation |
| --- | --- |
| Admin lockout during migration | Keep env admin/sync tokens as bootstrap/legacy credentials; require tests for first-admin creation and legacy access. |
| Scope creep into external auth | Treat #349 as out-of-MVP; design principal mapping points but do not implement external providers. |
| Service-account overbuild | Include `principal.kind` and schema readiness, but focus first UI/API flows on humans. |
| Breaking sync clients | Preserve `/sync/*` route and payload contracts; change authentication internals only. |
| Permission ambiguity | Define project grants as server-side allow rules and fail loudly when missing. |
| Token leakage | Show raw token once, hash at rest, avoid logging secrets, and test redaction-sensitive paths. |
| Dashboard confusion | Separate managed account controls from contributor-derived views. |

## Rollback plan

If the managed-principal implementation causes operational issues, operators must be able to fall back to the current env-token model while preserving data:

1. Keep legacy env-token authentication paths available throughout the MVP rollout.
2. Avoid destructive migrations of existing cloud data.
3. Gate managed-principal enforcement so legacy credentials can continue to authorize existing clients.
4. Preserve project allowlist behavior until principal grants fully replace it through an explicit migration step.
5. Document how to disable managed-token usage operationally if needed.

## Non-goals

- This proposal does not implement external identity providers.
- This proposal does not make cloud the source of truth for memories.
- This proposal does not redesign sync payloads or introduce new sync routes.
- This proposal does not require all deployments to migrate immediately.
- This proposal does not promise service-account management UI in the first slice.

## Proposal question round for user review

The proposal above is written from the current product decisions. Before moving to spec/design, the user should confirm or correct these assumptions:

1. Confirmed: the first managed admin should be creatable through both the legacy dashboard bootstrap path and an explicit CLI bootstrap command.
2. Confirmed: managed principals are deny-by-default for project access unless explicitly granted, even if a global allowlist exists.
3. Confirmed: MVP roles stay simple with `admin` and `member`; project grants carry project-level access initially.
4. Confirmed: legacy env sync tokens keep their current external behavior, but internally map to a bootstrap/legacy principal constrained by existing `ENGRAM_CLOUD_ALLOWED_PROJECTS` semantics.
5. Confirmed: MVP audit events must cover token create/revoke, user create, user enable/disable, grant create/revoke, admin login, and bootstrap actions.

## Next phase

Proceed to spec after the proposal assumptions are confirmed. The spec should define acceptance criteria for principal resolution, token hashing/revocation, project grant enforcement, admin bootstrap compatibility, and sync contract preservation.

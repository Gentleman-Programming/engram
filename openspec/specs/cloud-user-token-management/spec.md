# Specification: Cloud User Token Management

## Purpose

Engram Cloud MUST support principal-based user and token management for human users while preserving existing sync routes, payload contracts, and legacy environment-token behavior during migration. The MVP MUST enforce server-side project access, token revocation, token hashing, role-based administration, bootstrap paths, and auditability without making cloud the source of truth for local memory data.

## Requirements

### Requirement: Principal Resolution

The system MUST resolve every authenticated cloud request to a principal before making authorization decisions.

A principal MUST include a stable identifier and `kind`, where `kind` supports `human` and `service_account`. The MVP MUST implement human principals and MUST keep the model/schema ready for future service account principals without requiring a later identity-model rewrite.

Tokens MUST authenticate principals; tokens MUST NOT be treated as identities for authorization decisions.

#### Scenario: Managed token resolves to a human principal

- GIVEN a stored active human principal with an active token
- WHEN a request presents that token as a bearer credential
- THEN the request is authenticated as that human principal
- AND authorization decisions use the resolved principal and its grants/role

#### Scenario: Unknown token is denied

- GIVEN no active managed token or legacy credential matches a presented bearer token
- WHEN a cloud request is made with that bearer token
- THEN the request is rejected as unauthorized
- AND the request does not fall back to anonymous or global access

#### Scenario: Principal kind supports future service accounts

- GIVEN the principal model is persisted or serialized
- WHEN a principal is represented by the system
- THEN `principal.kind` accepts `human` and `service_account`
- AND MVP user-management flows are limited to human principals unless explicitly extended later

### Requirement: Human Users

The system MUST allow administrators to create, list, enable, disable, and inspect managed human users. Managed users MUST be distinct from contributor analytics or observed authors in synced memory data.

Human users MUST have an MVP role of either `admin` or `member`.

Disabled human users MUST NOT be able to authorize new admin or sync requests through managed tokens, even if their token records are otherwise active. Dashboard/session authorization MUST revalidate the managed principal's current enabled state and role from authoritative storage on every protected dashboard/admin request; stale signed cookie claims MUST NOT preserve access after disablement or demotion.

#### Scenario: Admin creates a managed human user

- GIVEN an authenticated admin principal
- WHEN the admin creates a human user with a valid role
- THEN the system creates a human principal and associated user record
- AND the user appears in managed user listings
- AND contributor analytics views are not treated as managed user records

#### Scenario: Disabled user cannot authenticate

- GIVEN a human user has been disabled
- AND the user has an otherwise active managed token
- WHEN a request presents that token
- THEN the request is rejected
- AND no project sync or admin operation is authorized for that principal

### Requirement: Roles and Administrative Authorization

The system MUST support `admin` and `member` roles for MVP managed human users.

Only admin principals MUST be authorized to manage users, tokens, roles, grants, bootstrap state, and administrative dashboard/API operations. Member principals MAY sync projects only when they have explicit project grants.

#### Scenario: Admin can manage identity policy

- GIVEN a principal with role `admin`
- WHEN the principal requests a supported user, token, or grant management operation
- THEN the operation is authorized if the request is otherwise valid

#### Scenario: Member cannot manage identity policy

- GIVEN a principal with role `member`
- WHEN the principal requests a user, token, grant, role, or bootstrap management operation
- THEN the operation is rejected as forbidden
- AND no policy state is changed

### Requirement: Token Creation and Show-Once Secret

The system MUST allow administrators to create managed sync tokens for principals.

The raw token secret MUST be returned only at creation time. After creation, token list and detail responses MUST expose metadata only and MUST NOT expose the raw secret or reusable credential material.

Token metadata SHOULD include enough information for operators to identify and revoke a token, such as owner principal, creation time, status, and an operator-provided label when supported.

#### Scenario: Created token secret is shown once

- GIVEN an authenticated admin principal
- WHEN the admin creates a token for a managed principal
- THEN the response includes the raw token secret exactly for that creation response
- AND subsequent token list or detail operations do not return the raw token secret

#### Scenario: Token metadata remains inspectable

- GIVEN a managed token exists
- WHEN an admin lists tokens
- THEN the token metadata is visible
- AND the raw token secret is not visible

### Requirement: Token Hashing and Secret Handling

The system MUST store managed token secrets only as cryptographic hashes or verifier values suitable for token verification. The system MUST NOT persist raw managed token secrets in the database, logs, audit events, or dashboard-rendered metadata.

#### Scenario: Raw token is not persisted

- GIVEN an admin creates a managed token
- WHEN the token record is persisted
- THEN the stored record contains a hash or verifier
- AND the stored record does not contain the raw token secret

#### Scenario: Token secret is not logged or audited

- GIVEN a token creation request succeeds or fails
- WHEN logs or audit events are produced
- THEN they do not include the raw token secret
- AND they may include safe token metadata or a non-secret identifier

### Requirement: Token Revocation

The system MUST allow administrators to revoke managed tokens. Revocation MUST take effect for subsequent requests without requiring process restart or deployment secret rotation.

A revoked token MUST NOT authorize sync or admin operations.

#### Scenario: Revoked token stops authorizing future requests

- GIVEN a managed token successfully authorized previous requests
- WHEN an admin revokes that token
- THEN future requests using that token are rejected
- AND no project sync or admin operation is performed for that token

#### Scenario: Revoking one token does not revoke unrelated tokens

- GIVEN a principal has multiple managed tokens
- WHEN one token is revoked
- THEN that token no longer authorizes requests
- AND other active tokens remain governed by their own status, the principal status, and project grants

### Requirement: Project Grants

The system MUST enforce project access through explicit principal project grants for managed principals.

Managed principals MUST be deny-by-default for project access. A managed principal without a grant for a project MUST NOT push, pull, or otherwise sync that project.

Project grants carry project-level access in the MVP. Fine-grained memory-level permissions are out of scope.

#### Scenario: Granted project sync succeeds

- GIVEN a managed principal has an active token
- AND the principal has an explicit grant for project `alpha`
- WHEN the principal syncs project `alpha`
- THEN the sync request is authorized subject to all other existing sync validation rules

#### Scenario: Ungranted project sync is denied

- GIVEN a managed principal has an active token
- AND the principal does not have a grant for project `beta`
- WHEN the principal attempts to sync project `beta`
- THEN the request is rejected clearly
- AND the system does not silently drop, partially apply, or leak data for that project

#### Scenario: Grant revocation stops future access

- GIVEN a managed principal has a grant for project `alpha`
- WHEN an admin revokes that project grant
- THEN subsequent sync requests for project `alpha` by that principal are rejected

### Requirement: Legacy Bootstrap Compatibility

The system MUST preserve existing external behavior for `ENGRAM_CLOUD_TOKEN`, `ENGRAM_CLOUD_ADMIN`, and `ENGRAM_CLOUD_ALLOWED_PROJECTS` during the MVP migration.

Internally, legacy environment credentials MUST resolve through the principal-aware path as bootstrap or legacy principals. The legacy sync principal MUST remain constrained by existing `ENGRAM_CLOUD_ALLOWED_PROJECTS` semantics, including current wildcard, empty, and normalized-project behavior.

The MVP MUST NOT remove, invalidate, or silently reinterpret existing environment-token behavior in a way that breaks current deployments.

`ENGRAM_CLOUD_ADMIN` MUST be treated as bootstrap/recovery access after at least one managed admin exists, not as a silent permanent bypass around managed roles, token revocation, or audit. Any non-bootstrap admin operation performed through the legacy admin credential after managed admins exist MUST be explicitly documented, auditable, and constrained to recovery-safe behavior.

#### Scenario: Legacy sync token keeps current project behavior

- GIVEN a deployment configured with `ENGRAM_CLOUD_TOKEN` and `ENGRAM_CLOUD_ALLOWED_PROJECTS`
- WHEN a client uses the legacy sync token
- THEN the token authenticates through a legacy/bootstrap principal
- AND project authorization preserves the existing allowed-project semantics

#### Scenario: Legacy admin credential can bootstrap managed access

- GIVEN a deployment configured with `ENGRAM_CLOUD_ADMIN`
- WHEN an operator authenticates with the legacy admin credential before a managed admin exists
- THEN the operator can perform supported first-admin bootstrap actions
- AND the credential remains documented as a migration aid, not the long-term managed identity model

#### Scenario: Legacy admin credential is constrained after managed admin exists

- GIVEN at least one managed admin exists
- WHEN an operator authenticates with the legacy admin credential
- THEN the system treats the credential as explicit bootstrap/recovery access only
- AND normal managed admin operations require a managed admin principal unless a documented recovery operation is being performed
- AND every accepted or rejected legacy-admin recovery operation is audited

### Requirement: CLI Bootstrap

The system MUST provide a CLI bootstrap path that can create the first managed admin without requiring dashboard access.

The CLI bootstrap path MUST be safe for headless/self-hosted deployments and MUST NOT require disabling existing legacy credentials.

#### Scenario: Operator creates first admin through CLI

- GIVEN a deployment has no managed admin user
- WHEN an authorized operator runs the supported CLI bootstrap command with valid input
- THEN the system creates the first managed human admin
- AND records a bootstrap audit event
- AND the admin can subsequently use managed admin flows

#### Scenario: CLI bootstrap does not create duplicate first admins silently

- GIVEN a managed admin already exists
- WHEN the first-admin CLI bootstrap command is run again
- THEN the system rejects or requires an explicit safe behavior for the duplicate bootstrap attempt
- AND the result is auditable

### Requirement: Dashboard Bootstrap

The system MUST provide a dashboard bootstrap path that can create the first managed admin using the legacy dashboard/admin bootstrap credential.

Dashboard bootstrap MUST avoid admin lockout during migration and MUST clearly distinguish bootstrap actions from normal managed-user administration.

The system MUST prevent routine admin mutations from removing the last usable managed admin path. Disabling, demoting, or otherwise invalidating the last active managed admin MUST be rejected unless a documented recovery path remains available and the operation is explicitly marked and audited.

#### Scenario: Operator creates first admin through dashboard bootstrap

- GIVEN a deployment has no managed admin user
- AND the operator authenticates with a valid legacy dashboard/admin bootstrap credential
- WHEN the operator submits valid first-admin details
- THEN the system creates the first managed human admin
- AND records a bootstrap audit event
- AND the new admin can use normal managed admin flows

#### Scenario: Dashboard bootstrap is unavailable after managed admin exists unless explicitly allowed

- GIVEN at least one managed admin exists
- WHEN an operator attempts first-admin dashboard bootstrap
- THEN the system rejects the first-admin bootstrap operation or requires an explicit safe recovery path
- AND does not silently overwrite existing admin state

#### Scenario: Last managed admin cannot be removed accidentally

- GIVEN exactly one usable managed admin path remains
- WHEN an admin attempts to disable, demote, or revoke all effective admin access for that path
- THEN the operation is rejected or requires an explicit documented recovery operation
- AND the result is audited

### Requirement: Audit Events

The system MUST record audit events for security-sensitive identity and access changes.

MVP audit coverage MUST include token create, token revoke, user create, user enable, user disable, grant create, grant revoke, admin login, CLI bootstrap, dashboard bootstrap, and legacy/bootstrap actions.

Audit events MUST include enough non-secret context to support operational review, such as event type, actor principal or bootstrap identity, target entity, timestamp, and result. Audit events MUST NOT include raw token secrets.

#### Scenario: Token lifecycle is audited

- GIVEN an admin creates or revokes a managed token
- WHEN the operation completes
- THEN an audit event is recorded with the actor, target token metadata, action, timestamp, and result
- AND the audit event does not include the raw token secret

#### Scenario: User and grant lifecycle is audited

- GIVEN an admin creates, enables, or disables a user
- OR creates or revokes a project grant
- WHEN the operation completes
- THEN an audit event is recorded with the actor, target, action, timestamp, and result

#### Scenario: Bootstrap and admin login are audited

- GIVEN an operator performs CLI bootstrap, dashboard bootstrap, legacy/bootstrap authentication, or admin login
- WHEN the operation is accepted or rejected
- THEN an audit event is recorded with non-secret context and the result

### Requirement: Sync Contract Preservation

The system MUST preserve existing `/sync/*` route shapes, request payload contracts, and response payload contracts for existing clients.

Authentication and authorization internals MAY change to principal resolution and grant enforcement, but clients MUST NOT be required to change sync paths or payload schemas for the MVP.

#### Scenario: Existing sync client payload remains valid

- GIVEN a client uses an existing `/sync/*` route with the current request payload shape
- AND the request is authenticated and authorized under either legacy or managed-token rules
- WHEN the request is processed
- THEN the server accepts the same route and payload contract as before
- AND returns a response compatible with the existing client contract

#### Scenario: Auth failure does not change sync schema

- GIVEN a client uses an existing `/sync/*` route with an invalid, revoked, or unauthorized token
- WHEN the request is rejected
- THEN the rejection uses the server's established HTTP error style for auth or authorization failures
- AND does not require clients to adopt a new sync payload schema

### Requirement: Denied Behavior

The system MUST fail loudly and deterministically when authentication or authorization fails.

Denied sync requests MUST NOT silently drop chunks, mutations, project data, or policy changes. Unauthorized requests MUST NOT reveal data from projects that the principal cannot access.

#### Scenario: Unauthorized push is not partially applied

- GIVEN a managed principal attempts to push data for at least one ungranted project
- WHEN the server detects the missing grant
- THEN unauthorized project data is not applied
- AND the client receives a clear failure

#### Scenario: Unauthorized pull does not leak data

- GIVEN a managed principal requests pull data
- AND the principal lacks grants for one or more projects
- WHEN the server prepares the response
- THEN the response contains no data for ungranted projects
- AND authorization behavior is deterministic and test-covered

### Requirement: Tests and Verification

The change MUST include automated tests proving principal resolution, human user management, role authorization, token hashing/show-once behavior, token revocation, project grant enforcement, legacy bootstrap compatibility, CLI bootstrap, dashboard bootstrap, audit events, sync contract preservation, and denied behavior.

Tests MUST cover success and error paths for new or changed HTTP/API behavior. Tests touching sync MUST cover both authorized and denied push/pull paths where applicable.

Documentation or operational examples affected by the change MUST be updated and validated in the same delivery slice.

#### Scenario: Managed-token behavior is regression tested

- GIVEN the test suite runs for cloud auth and sync behavior
- WHEN managed token tests execute
- THEN they prove successful token authentication, show-once secret exposure, hash-only persistence, and revoked-token denial

#### Scenario: Project authorization is regression tested

- GIVEN the test suite runs for sync authorization
- WHEN managed principal grant tests execute
- THEN they prove granted project access succeeds
- AND ungranted or revoked-grant access is denied without silent data loss

#### Scenario: Bootstrap and legacy compatibility are regression tested

- GIVEN the test suite runs for bootstrap and legacy compatibility
- WHEN legacy env-token and first-admin bootstrap tests execute
- THEN they prove existing env-token behavior remains compatible
- AND both CLI and dashboard bootstrap can create the first managed admin under valid conditions

#### Scenario: API and documentation contracts are verified

- GIVEN new or changed admin, dashboard, CLI, or sync-facing behavior exists
- WHEN verification runs
- THEN handler/API tests cover success and error paths
- AND user-facing docs or examples match the implemented routes, commands, and payload contracts

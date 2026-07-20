package cloudstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Gentleman-Programming/engram/internal/store"
)

const (
	PrincipalKindHuman          = "human"
	PrincipalKindServiceAccount = "service_account"

	PrincipalRoleAdmin  = "admin"
	PrincipalRoleMember = "member"
)

var (
	ErrInvalidPrincipalKind   = errors.New("cloudstore: invalid principal kind")
	ErrInvalidPrincipalRole   = errors.New("cloudstore: invalid principal role")
	ErrLastActiveAdmin        = errors.New("cloudstore: cannot remove last active admin")
	ErrSensitiveAuditMetadata = errors.New("cloudstore: sensitive auth audit metadata is not allowed")
	ErrAuthAuditInsertFailed  = errors.New("cloudstore: auth audit insert failed")
	ErrAdminAlreadyExists     = errors.New("cloudstore: a managed admin already exists")
	ErrPrincipalNotFound      = errors.New("cloudstore: principal not found")
	ErrPrincipalDisabled      = errors.New("cloudstore: principal is disabled")
	ErrPrincipalTokenNotFound = errors.New("cloudstore: principal token not found")
)

type Principal struct {
	ID          string
	Kind        string
	DisplayName string
	Role        string
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CreatePrincipalParams struct {
	Kind        string
	DisplayName string
	Role        string
	Enabled     *bool
}

type UpdatePrincipalParams struct {
	Role    string
	Enabled bool
}

type HumanUser struct {
	PrincipalID string
	Username    string
	Email       string
	DisplayName string
	Role        string
	Enabled     bool
	CreatedAt   time.Time
}

type CreateHumanUserParams struct {
	Username    string
	Email       string
	DisplayName string
	Role        string
}

type PrincipalToken struct {
	ID                   string
	PrincipalID          string
	TokenPrefix          string
	TokenHash            string
	Name                 string
	CreatedByPrincipalID string
	CreatedAt            time.Time
	LastUsedAt           *time.Time
	RevokedAt            *time.Time
	RevokedByPrincipalID string
	RevocationReason     string
}

type CreatePrincipalTokenParams struct {
	PrincipalID          string
	TokenPrefix          string
	TokenHash            string
	Name                 string
	CreatedByPrincipalID string
}

type ProjectGrant struct {
	PrincipalID          string
	Project              string
	GrantedByPrincipalID string
	CreatedAt            time.Time
}

type CreateProjectGrantParams struct {
	PrincipalID          string
	Project              string
	GrantedByPrincipalID string
}

type AuthAuditEvent struct {
	ID                string
	OccurredAt        time.Time
	ActorPrincipalID  string
	ActorSource       string
	TargetPrincipalID string
	Project           string
	Action            string
	Outcome           string
	ReasonCode        string
	Metadata          map[string]any
}

type AuthAuditQuery struct {
	Limit int
}

func (cs *CloudStore) CreatePrincipal(ctx context.Context, params CreatePrincipalParams) (Principal, error) {
	if cs == nil || cs.db == nil {
		return Principal{}, fmt.Errorf("cloudstore: not initialized")
	}
	kind := strings.TrimSpace(params.Kind)
	if !validPrincipalKind(kind) {
		return Principal{}, ErrInvalidPrincipalKind
	}
	role := strings.TrimSpace(params.Role)
	if !validPrincipalRole(role) {
		return Principal{}, ErrInvalidPrincipalRole
	}
	displayName := strings.TrimSpace(params.DisplayName)
	if displayName == "" {
		return Principal{}, fmt.Errorf("cloudstore: display name is required")
	}
	enabled := true
	if params.Enabled != nil {
		enabled = *params.Enabled
	}

	const q = `
		INSERT INTO cloud_principals (kind, display_name, role, enabled)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text, kind, display_name, role, enabled, created_at, updated_at`
	return scanPrincipal(cs.db.QueryRowContext(ctx, q, kind, displayName, role, enabled))
}

func (cs *CloudStore) GetPrincipal(ctx context.Context, id string) (Principal, error) {
	if cs == nil || cs.db == nil {
		return Principal{}, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `SELECT id::text, kind, display_name, role, enabled, created_at, updated_at FROM cloud_principals WHERE id = $1`
	principal, err := scanPrincipal(cs.db.QueryRowContext(ctx, q, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, fmt.Errorf("cloudstore: principal not found")
	}
	return principal, err
}

func (cs *CloudStore) ListPrincipals(ctx context.Context) ([]Principal, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	rows, err := cs.db.QueryContext(ctx, `SELECT id::text, kind, display_name, role, enabled, created_at, updated_at FROM cloud_principals ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list principals: %w", err)
	}
	defer rows.Close()
	principals := make([]Principal, 0)
	for rows.Next() {
		principal, err := scanPrincipal(rows)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: scan principal: %w", err)
		}
		principals = append(principals, principal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate principals: %w", err)
	}
	return principals, nil
}

func (cs *CloudStore) UpdatePrincipal(ctx context.Context, id string, params UpdatePrincipalParams) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	role := strings.TrimSpace(params.Role)
	if !validPrincipalRole(role) {
		return ErrInvalidPrincipalRole
	}
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: begin update principal tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := guardLastActiveAdminTx(ctx, tx, strings.TrimSpace(id), role, params.Enabled); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE cloud_principals SET role = $2, enabled = $3, updated_at = NOW() WHERE id = $1`, strings.TrimSpace(id), role, params.Enabled)
	if err != nil {
		return fmt.Errorf("cloudstore: update principal: %w", err)
	}
	if err := requireAffected(res, "principal"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: commit update principal: %w", err)
	}
	return nil
}

func (cs *CloudStore) CreateHumanUser(ctx context.Context, params CreateHumanUserParams) (HumanUser, error) {
	if cs == nil || cs.db == nil {
		return HumanUser{}, fmt.Errorf("cloudstore: not initialized")
	}
	username := strings.TrimSpace(params.Username)
	if username == "" {
		return HumanUser{}, fmt.Errorf("cloudstore: username is required")
	}
	role := strings.TrimSpace(params.Role)
	if !validPrincipalRole(role) {
		return HumanUser{}, ErrInvalidPrincipalRole
	}
	displayName := strings.TrimSpace(params.DisplayName)
	if displayName == "" {
		displayName = username
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return HumanUser{}, fmt.Errorf("cloudstore: begin human user tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var principal Principal
	const principalQ = `
		INSERT INTO cloud_principals (kind, display_name, role, enabled)
		VALUES ($1, $2, $3, TRUE)
		RETURNING id::text, kind, display_name, role, enabled, created_at, updated_at`
	if err := tx.QueryRowContext(ctx, principalQ, PrincipalKindHuman, displayName, role).Scan(&principal.ID, &principal.Kind, &principal.DisplayName, &principal.Role, &principal.Enabled, &principal.CreatedAt, &principal.UpdatedAt); err != nil {
		return HumanUser{}, fmt.Errorf("cloudstore: create human principal: %w", err)
	}

	const humanQ = `
		INSERT INTO cloud_human_users (principal_id, username, email)
		VALUES ($1, $2, $3)
		RETURNING principal_id::text, username, COALESCE(email, ''), created_at`
	var human HumanUser
	if err := tx.QueryRowContext(ctx, humanQ, principal.ID, username, nullableText(params.Email)).Scan(&human.PrincipalID, &human.Username, &human.Email, &human.CreatedAt); err != nil {
		return HumanUser{}, fmt.Errorf("cloudstore: create human user: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return HumanUser{}, fmt.Errorf("cloudstore: commit human user: %w", err)
	}
	human.DisplayName = principal.DisplayName
	human.Role = principal.Role
	human.Enabled = principal.Enabled
	return human, nil
}

// CreateFirstAdminHumanUser atomically checks for an existing active admin
// and creates the first managed admin human user within a single
// transaction, using the same transaction-scoped advisory lock as
// guardLastActiveAdminTx (key "engram_cloud_active_admin_guard"). This
// closes a check-then-act (TOCTOU) race: two callers (the CLI bootstrap
// command and/or the dashboard first-admin bootstrap route) invoking
// HasActiveAdmin then CreateHumanUser as two separate calls could both
// observe "no active admin" and both create a first admin. Callers MUST use
// this method instead of that check-then-act sequence for first-admin
// bootstrap.
//
// Returns ErrAdminAlreadyExists (no mutation) if an active admin already
// exists. params.Role is ignored — this method always creates an
// admin-role principal, matching its sole purpose.
func (cs *CloudStore) CreateFirstAdminHumanUser(ctx context.Context, params CreateHumanUserParams) (HumanUser, error) {
	if cs == nil || cs.db == nil {
		return HumanUser{}, fmt.Errorf("cloudstore: not initialized")
	}
	username := strings.TrimSpace(params.Username)
	if username == "" {
		return HumanUser{}, fmt.Errorf("cloudstore: username is required")
	}
	displayName := strings.TrimSpace(params.DisplayName)
	if displayName == "" {
		displayName = username
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return HumanUser{}, fmt.Errorf("cloudstore: begin first admin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Same advisory lock key as guardLastActiveAdminTx: first-admin creation
	// serializes with concurrent admin-removal/demotion guard transactions
	// as well as with concurrent first-admin bootstrap attempts.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('engram_cloud_active_admin_guard'))`); err != nil {
		return HumanUser{}, fmt.Errorf("cloudstore: lock active admin guard: %w", err)
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM cloud_principals WHERE role = $1 AND enabled = TRUE)`, PrincipalRoleAdmin).Scan(&exists); err != nil {
		return HumanUser{}, fmt.Errorf("cloudstore: check existing admin: %w", err)
	}
	if exists {
		return HumanUser{}, ErrAdminAlreadyExists
	}

	var principal Principal
	const principalQ = `
		INSERT INTO cloud_principals (kind, display_name, role, enabled)
		VALUES ($1, $2, $3, TRUE)
		RETURNING id::text, kind, display_name, role, enabled, created_at, updated_at`
	if err := tx.QueryRowContext(ctx, principalQ, PrincipalKindHuman, displayName, PrincipalRoleAdmin).Scan(&principal.ID, &principal.Kind, &principal.DisplayName, &principal.Role, &principal.Enabled, &principal.CreatedAt, &principal.UpdatedAt); err != nil {
		return HumanUser{}, fmt.Errorf("cloudstore: create first admin principal: %w", err)
	}

	const humanQ = `
		INSERT INTO cloud_human_users (principal_id, username, email)
		VALUES ($1, $2, $3)
		RETURNING principal_id::text, username, COALESCE(email, ''), created_at`
	var human HumanUser
	if err := tx.QueryRowContext(ctx, humanQ, principal.ID, username, nullableText(params.Email)).Scan(&human.PrincipalID, &human.Username, &human.Email, &human.CreatedAt); err != nil {
		return HumanUser{}, fmt.Errorf("cloudstore: create first admin human user: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return HumanUser{}, fmt.Errorf("cloudstore: commit first admin: %w", err)
	}
	human.DisplayName = principal.DisplayName
	human.Role = principal.Role
	human.Enabled = principal.Enabled
	return human, nil
}

func (cs *CloudStore) ListHumanUsers(ctx context.Context) ([]HumanUser, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		SELECT h.principal_id::text, h.username, COALESCE(h.email, ''), p.display_name, p.role, p.enabled, h.created_at
		FROM cloud_human_users h
		JOIN cloud_principals p ON p.id = h.principal_id
		ORDER BY h.username ASC`
	rows, err := cs.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list human users: %w", err)
	}
	defer rows.Close()
	users := make([]HumanUser, 0)
	for rows.Next() {
		var user HumanUser
		if err := rows.Scan(&user.PrincipalID, &user.Username, &user.Email, &user.DisplayName, &user.Role, &user.Enabled, &user.CreatedAt); err != nil {
			return nil, fmt.Errorf("cloudstore: scan human user: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate human users: %w", err)
	}
	return users, nil
}

func (cs *CloudStore) SetHumanUserEnabled(ctx context.Context, principalID string, enabled bool) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cloudstore: begin set human enabled tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var currentRole string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM cloud_principals WHERE id = $1 AND kind = $2 FOR UPDATE`, strings.TrimSpace(principalID), PrincipalKindHuman).Scan(&currentRole); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("cloudstore: human user not found")
	} else if err != nil {
		return fmt.Errorf("cloudstore: read human user: %w", err)
	}
	if err := guardLastActiveAdminTx(ctx, tx, strings.TrimSpace(principalID), currentRole, enabled); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE cloud_principals SET enabled = $2, updated_at = NOW() WHERE id = $1 AND kind = $3`, strings.TrimSpace(principalID), enabled, PrincipalKindHuman)
	if err != nil {
		return fmt.Errorf("cloudstore: set human enabled: %w", err)
	}
	if err := requireAffected(res, "human user"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cloudstore: commit set human enabled: %w", err)
	}
	return nil
}

func (cs *CloudStore) CreatePrincipalToken(ctx context.Context, params CreatePrincipalTokenParams) (PrincipalToken, error) {
	if cs == nil || cs.db == nil {
		return PrincipalToken{}, fmt.Errorf("cloudstore: not initialized")
	}
	values, err := normalizeCreatePrincipalTokenParams(params)
	if err != nil {
		return PrincipalToken{}, err
	}
	const q = `
		WITH target_principal AS (
			SELECT id
			FROM cloud_principals
			WHERE id = $1 AND enabled = TRUE
			FOR UPDATE
		)
		INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name, created_by_principal_id)
		SELECT p.id, $2, $3, $4, $5
		FROM target_principal p
		RETURNING id::text, principal_id::text, token_prefix, '' AS token_hash, name, COALESCE(created_by_principal_id::text, ''), created_at, last_used_at, revoked_at, COALESCE(revoked_by_principal_id::text, ''), COALESCE(revocation_reason, '')`
	token, err := scanPrincipalToken(cs.db.QueryRowContext(ctx, q, values.principalID, values.tokenPrefix, values.tokenHash, values.name, values.createdByPrincipalID))
	if errors.Is(err, sql.ErrNoRows) {
		return PrincipalToken{}, principalTokenTargetError(ctx, cs.db, values.principalID)
	}
	return token, err
}

func (cs *CloudStore) CreatePrincipalTokenWithAudit(ctx context.Context, params CreatePrincipalTokenParams, audit AuthAuditEvent) (PrincipalToken, error) {
	if cs == nil || cs.db == nil {
		return PrincipalToken{}, fmt.Errorf("cloudstore: not initialized")
	}
	values, err := normalizeCreatePrincipalTokenParams(params)
	if err != nil {
		return PrincipalToken{}, err
	}
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return PrincipalToken{}, fmt.Errorf("cloudstore: begin principal token audit tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const q = `
		WITH target_principal AS (
			SELECT id
			FROM cloud_principals
			WHERE id = $1 AND enabled = TRUE
			FOR UPDATE
		)
		INSERT INTO cloud_principal_tokens (principal_id, token_prefix, token_hash, name, created_by_principal_id)
		SELECT p.id, $2, $3, $4, $5
		FROM target_principal p
		RETURNING id::text, principal_id::text, token_prefix, '' AS token_hash, name, COALESCE(created_by_principal_id::text, ''), created_at, last_used_at, revoked_at, COALESCE(revoked_by_principal_id::text, ''), COALESCE(revocation_reason, '')`
	token, err := scanPrincipalToken(tx.QueryRowContext(ctx, q, values.principalID, values.tokenPrefix, values.tokenHash, values.name, values.createdByPrincipalID))
	if errors.Is(err, sql.ErrNoRows) {
		return PrincipalToken{}, principalTokenTargetError(ctx, tx, values.principalID)
	}
	if err != nil {
		return PrincipalToken{}, err
	}
	if err := insertAuthAuditEvent(ctx, tx, audit); err != nil {
		return PrincipalToken{}, fmt.Errorf("%w: %w", ErrAuthAuditInsertFailed, err)
	}
	if err := tx.Commit(); err != nil {
		return PrincipalToken{}, fmt.Errorf("cloudstore: commit principal token audit tx: %w", err)
	}
	return token, nil
}

type createPrincipalTokenValues struct {
	principalID          string
	tokenPrefix          string
	tokenHash            string
	name                 string
	createdByPrincipalID any
}

func normalizeCreatePrincipalTokenParams(params CreatePrincipalTokenParams) (createPrincipalTokenValues, error) {
	principalID := strings.TrimSpace(params.PrincipalID)
	if principalID == "" {
		return createPrincipalTokenValues{}, fmt.Errorf("cloudstore: principal id is required")
	}
	tokenPrefix := strings.TrimSpace(params.TokenPrefix)
	if tokenPrefix == "" {
		return createPrincipalTokenValues{}, fmt.Errorf("cloudstore: token prefix is required")
	}
	tokenHash := strings.TrimSpace(params.TokenHash)
	if tokenHash == "" {
		return createPrincipalTokenValues{}, fmt.Errorf("cloudstore: token hash is required")
	}
	return createPrincipalTokenValues{
		principalID:          principalID,
		tokenPrefix:          tokenPrefix,
		tokenHash:            tokenHash,
		name:                 strings.TrimSpace(params.Name),
		createdByPrincipalID: nullableID(params.CreatedByPrincipalID),
	}, nil
}

type principalTokenTargetQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func principalTokenTargetError(ctx context.Context, queryer principalTokenTargetQueryer, principalID string) error {
	var enabled bool
	err := queryer.QueryRowContext(ctx, `SELECT enabled FROM cloud_principals WHERE id = $1`, strings.TrimSpace(principalID)).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPrincipalNotFound
	}
	if err != nil {
		return fmt.Errorf("cloudstore: read token principal: %w", err)
	}
	if !enabled {
		return ErrPrincipalDisabled
	}
	return fmt.Errorf("cloudstore: token principal is unavailable")
}

func (cs *CloudStore) ListPrincipalTokens(ctx context.Context, principalID string) ([]PrincipalToken, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	const q = `
		SELECT id::text, principal_id::text, token_prefix, '' AS token_hash, name, COALESCE(created_by_principal_id::text, ''), created_at, last_used_at, revoked_at, COALESCE(revoked_by_principal_id::text, ''), COALESCE(revocation_reason, '')
		FROM cloud_principal_tokens
		WHERE principal_id = $1
		ORDER BY created_at ASC, id ASC`
	rows, err := cs.db.QueryContext(ctx, q, strings.TrimSpace(principalID))
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list principal tokens: %w", err)
	}
	defer rows.Close()
	tokens := make([]PrincipalToken, 0)
	for rows.Next() {
		token, err := scanPrincipalToken(rows)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: scan principal token: %w", err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate principal tokens: %w", err)
	}
	return tokens, nil
}

// FindPrincipalTokenByHash looks up a managed token record and its owning
// principal by the token's HMAC verifier hash. It is the storage-only
// production lookup consumed (through a package-boundary-safe adapter, since
// internal/cloud/auth already imports cloudstore and a direct cloudstore ->
// auth import would cycle) by cloudauth.ManagedTokenLookup at runtime, so
// managed cloud tokens can actually authenticate against a running
// `engram cloud serve` process rather than only in tests.
//
// Returns ErrPrincipalTokenNotFound (not sql.ErrNoRows) when no token matches
// tokenHash, so callers can map it to the auth package's own "unknown token"
// sentinel without leaking a database-specific error type. The returned
// PrincipalToken's TokenHash field is intentionally left blank, matching the
// same "never return the stored hash to a caller" convention already used by
// ListPrincipalTokens/CreatePrincipalToken — the caller already holds the
// hash it looked up with and never needs it echoed back.
func (cs *CloudStore) FindPrincipalTokenByHash(ctx context.Context, tokenHash string) (PrincipalToken, Principal, error) {
	if cs == nil || cs.db == nil {
		return PrincipalToken{}, Principal{}, fmt.Errorf("cloudstore: not initialized")
	}
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return PrincipalToken{}, Principal{}, fmt.Errorf("cloudstore: token hash is required")
	}
	const q = `
		SELECT t.id::text, t.principal_id::text, t.token_prefix, '' AS token_hash, t.name,
		       COALESCE(t.created_by_principal_id::text, ''), t.created_at, t.last_used_at, t.revoked_at,
		       COALESCE(t.revoked_by_principal_id::text, ''), COALESCE(t.revocation_reason, ''),
		       p.id::text, p.kind, p.display_name, p.role, p.enabled, p.created_at, p.updated_at
		FROM cloud_principal_tokens t
		JOIN cloud_principals p ON p.id = t.principal_id
		WHERE t.token_hash = $1`
	var token PrincipalToken
	var principal Principal
	err := cs.db.QueryRowContext(ctx, q, tokenHash).Scan(
		&token.ID, &token.PrincipalID, &token.TokenPrefix, &token.TokenHash, &token.Name,
		&token.CreatedByPrincipalID, &token.CreatedAt, &token.LastUsedAt, &token.RevokedAt,
		&token.RevokedByPrincipalID, &token.RevocationReason,
		&principal.ID, &principal.Kind, &principal.DisplayName, &principal.Role, &principal.Enabled,
		&principal.CreatedAt, &principal.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PrincipalToken{}, Principal{}, ErrPrincipalTokenNotFound
	}
	if err != nil {
		return PrincipalToken{}, Principal{}, fmt.Errorf("cloudstore: find principal token by hash: %w", err)
	}
	return token, principal, nil
}

func (cs *CloudStore) RevokePrincipalToken(ctx context.Context, tokenID, revokedByPrincipalID, reason string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	res, err := cs.db.ExecContext(ctx, `
		UPDATE cloud_principal_tokens
		SET revoked_at = COALESCE(revoked_at, NOW()), revoked_by_principal_id = $2, revocation_reason = $3
		WHERE id = $1`, strings.TrimSpace(tokenID), nullableID(revokedByPrincipalID), nullableText(reason))
	if err != nil {
		return fmt.Errorf("cloudstore: revoke principal token: %w", err)
	}
	return requireAffected(res, "principal token")
}

func (cs *CloudStore) CreateProjectGrant(ctx context.Context, params CreateProjectGrantParams) (ProjectGrant, error) {
	if cs == nil || cs.db == nil {
		return ProjectGrant{}, fmt.Errorf("cloudstore: not initialized")
	}
	principalID := strings.TrimSpace(params.PrincipalID)
	if principalID == "" {
		return ProjectGrant{}, fmt.Errorf("cloudstore: principal id is required")
	}
	project := normalizeCloudProjectGrant(params.Project)
	if project == "" {
		return ProjectGrant{}, fmt.Errorf("cloudstore: project is required")
	}
	const q = `
		INSERT INTO cloud_project_grants (principal_id, project, granted_by_principal_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (principal_id, project) DO UPDATE SET project = EXCLUDED.project
		RETURNING principal_id::text, project, COALESCE(granted_by_principal_id::text, ''), created_at`
	return scanProjectGrant(cs.db.QueryRowContext(ctx, q, principalID, project, nullableID(params.GrantedByPrincipalID)))
}

func (cs *CloudStore) ListProjectGrants(ctx context.Context, principalID string) ([]ProjectGrant, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	rows, err := cs.db.QueryContext(ctx, `SELECT principal_id::text, project, COALESCE(granted_by_principal_id::text, ''), created_at FROM cloud_project_grants WHERE principal_id = $1 ORDER BY project ASC`, strings.TrimSpace(principalID))
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list project grants: %w", err)
	}
	defer rows.Close()
	grants := make([]ProjectGrant, 0)
	for rows.Next() {
		grant, err := scanProjectGrant(rows)
		if err != nil {
			return nil, fmt.Errorf("cloudstore: scan project grant: %w", err)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate project grants: %w", err)
	}
	return grants, nil
}

func (cs *CloudStore) RevokeProjectGrant(ctx context.Context, principalID, project string) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	normalized := normalizeCloudProjectGrant(project)
	res, err := cs.db.ExecContext(ctx, `DELETE FROM cloud_project_grants WHERE principal_id = $1 AND project = $2`, strings.TrimSpace(principalID), normalized)
	if err != nil {
		return fmt.Errorf("cloudstore: revoke project grant: %w", err)
	}
	_, _ = res.RowsAffected()
	return nil
}

func (cs *CloudStore) HasActiveAdmin(ctx context.Context) (bool, error) {
	if cs == nil || cs.db == nil {
		return false, fmt.Errorf("cloudstore: not initialized")
	}
	var exists bool
	if err := cs.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM cloud_principals WHERE role = $1 AND enabled = TRUE)`, PrincipalRoleAdmin).Scan(&exists); err != nil {
		return false, fmt.Errorf("cloudstore: has active admin: %w", err)
	}
	return exists, nil
}

func (cs *CloudStore) WouldRemoveLastActiveAdmin(ctx context.Context, principalID string) (bool, error) {
	if cs == nil || cs.db == nil {
		return false, fmt.Errorf("cloudstore: not initialized")
	}
	var role string
	var enabled bool
	err := cs.db.QueryRowContext(ctx, `SELECT role, enabled FROM cloud_principals WHERE id = $1`, strings.TrimSpace(principalID)).Scan(&role, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cloudstore: read admin candidate: %w", err)
	}
	if role != PrincipalRoleAdmin || !enabled {
		return false, nil
	}
	var count int
	if err := cs.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_principals WHERE role = $1 AND enabled = TRUE`, PrincipalRoleAdmin).Scan(&count); err != nil {
		return false, fmt.Errorf("cloudstore: count active admins: %w", err)
	}
	return count <= 1, nil
}

func (cs *CloudStore) InsertAuthAuditEvent(ctx context.Context, event AuthAuditEvent) error {
	if cs == nil || cs.db == nil {
		return fmt.Errorf("cloudstore: not initialized")
	}
	return insertAuthAuditEvent(ctx, cs.db, event)
}

type authAuditEventExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertAuthAuditEvent(ctx context.Context, exec authAuditEventExecutor, event AuthAuditEvent) error {
	actorSource := strings.TrimSpace(event.ActorSource)
	if actorSource == "" {
		return fmt.Errorf("cloudstore: actor source is required")
	}
	action := strings.TrimSpace(event.Action)
	if action == "" {
		return fmt.Errorf("cloudstore: action is required")
	}
	outcome := strings.TrimSpace(event.Outcome)
	if outcome == "" {
		return fmt.Errorf("cloudstore: outcome is required")
	}
	metadata, err := nullableJSON(event.Metadata)
	if err != nil {
		return err
	}
	_, err = exec.ExecContext(ctx, `
		INSERT INTO cloud_auth_audit_log (actor_principal_id, actor_source, target_principal_id, project, action, outcome, reason_code, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, nullableID(event.ActorPrincipalID), actorSource, nullableID(event.TargetPrincipalID), nullableText(event.Project), action, outcome, nullableText(event.ReasonCode), metadata)
	if err != nil {
		return fmt.Errorf("cloudstore: insert auth audit event: %w", err)
	}
	return nil
}

func (cs *CloudStore) ListAuthAuditEvents(ctx context.Context, query AuthAuditQuery) ([]AuthAuditEvent, error) {
	if cs == nil || cs.db == nil {
		return nil, fmt.Errorf("cloudstore: not initialized")
	}
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := cs.db.QueryContext(ctx, `
		SELECT id::text, occurred_at, COALESCE(actor_principal_id::text, ''), actor_source, COALESCE(target_principal_id::text, ''), COALESCE(project, ''), action, outcome, COALESCE(reason_code, ''), metadata
		FROM cloud_auth_audit_log
		ORDER BY occurred_at DESC, id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: list auth audit events: %w", err)
	}
	defer rows.Close()
	events := make([]AuthAuditEvent, 0)
	for rows.Next() {
		var event AuthAuditEvent
		var metadata []byte
		if err := rows.Scan(&event.ID, &event.OccurredAt, &event.ActorPrincipalID, &event.ActorSource, &event.TargetPrincipalID, &event.Project, &event.Action, &event.Outcome, &event.ReasonCode, &metadata); err != nil {
			return nil, fmt.Errorf("cloudstore: scan auth audit event: %w", err)
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &event.Metadata); err != nil {
				return nil, fmt.Errorf("cloudstore: decode auth audit metadata: %w", err)
			}
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cloudstore: iterate auth audit events: %w", err)
	}
	return events, nil
}

func scanPrincipal(scanner interface{ Scan(dest ...any) error }) (Principal, error) {
	var principal Principal
	if err := scanner.Scan(&principal.ID, &principal.Kind, &principal.DisplayName, &principal.Role, &principal.Enabled, &principal.CreatedAt, &principal.UpdatedAt); err != nil {
		return Principal{}, err
	}
	return principal, nil
}

func scanPrincipalToken(scanner interface{ Scan(dest ...any) error }) (PrincipalToken, error) {
	var token PrincipalToken
	if err := scanner.Scan(&token.ID, &token.PrincipalID, &token.TokenPrefix, &token.TokenHash, &token.Name, &token.CreatedByPrincipalID, &token.CreatedAt, &token.LastUsedAt, &token.RevokedAt, &token.RevokedByPrincipalID, &token.RevocationReason); err != nil {
		return PrincipalToken{}, err
	}
	return token, nil
}

func scanProjectGrant(scanner interface{ Scan(dest ...any) error }) (ProjectGrant, error) {
	var grant ProjectGrant
	if err := scanner.Scan(&grant.PrincipalID, &grant.Project, &grant.GrantedByPrincipalID, &grant.CreatedAt); err != nil {
		return ProjectGrant{}, err
	}
	return grant, nil
}

func requireAffected(res sql.Result, label string) error {
	rows, err := res.RowsAffected()
	if err != nil {
		return nil
	}
	if rows == 0 {
		return fmt.Errorf("cloudstore: %s not found", label)
	}
	return nil
}

func validPrincipalKind(kind string) bool {
	return kind == PrincipalKindHuman || kind == PrincipalKindServiceAccount
}

func validPrincipalRole(role string) bool {
	return role == PrincipalRoleAdmin || role == PrincipalRoleMember
}

func nullableID(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func nullableText(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func nullableJSON(metadata map[string]any) (any, error) {
	if len(metadata) == 0 {
		return nil, nil
	}
	if err := rejectSensitiveAuthAuditMetadata(metadata); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("cloudstore: encode auth audit metadata: %w", err)
	}
	return encoded, nil
}

// NormalizeProjectGrant exposes normalizeCloudProjectGrant's normalization
// rules to callers outside this package (specifically, the cmd/engram
// PrincipalProjectAuthorizer adapter wired into cloudserver at runtime) so a
// caller-presented project name can be compared against stored
// cloud_project_grants rows using the exact same normalization that
// CreateProjectGrant already applies when persisting a grant. Duplicating
// this normalization logic in another package instead of reusing it here
// would risk the two falling out of sync and silently breaking grant
// enforcement.
func NormalizeProjectGrant(project string) string {
	return normalizeCloudProjectGrant(project)
}

func normalizeCloudProjectGrant(project string) string {
	normalized, _ := store.NormalizeProject(project)
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return ""
	}
	var builder strings.Builder
	previousHyphen := false
	for _, r := range normalized {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_'
		if allowed {
			builder.WriteRune(r)
			previousHyphen = false
			continue
		}
		if !previousHyphen {
			builder.WriteByte('-')
			previousHyphen = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func guardLastActiveAdminTx(ctx context.Context, tx *sql.Tx, principalID string, nextRole string, nextEnabled bool) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('engram_cloud_active_admin_guard'))`); err != nil {
		return fmt.Errorf("cloudstore: lock active admin guard: %w", err)
	}
	var currentRole string
	var currentEnabled bool
	err := tx.QueryRowContext(ctx, `SELECT role, enabled FROM cloud_principals WHERE id = $1 FOR UPDATE`, principalID).Scan(&currentRole, &currentEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cloudstore: read admin guard candidate: %w", err)
	}
	if currentRole != PrincipalRoleAdmin || !currentEnabled {
		return nil
	}
	if nextRole == PrincipalRoleAdmin && nextEnabled {
		return nil
	}
	var activeAdmins int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_principals WHERE role = $1 AND enabled = TRUE`, PrincipalRoleAdmin).Scan(&activeAdmins); err != nil {
		return fmt.Errorf("cloudstore: count active admins: %w", err)
	}
	if activeAdmins <= 1 {
		return ErrLastActiveAdmin
	}
	return nil
}

func rejectSensitiveAuthAuditMetadata(metadata map[string]any) error {
	for key, value := range metadata {
		if sensitiveAuthAuditKey(key) {
			return fmt.Errorf("%w: %s", ErrSensitiveAuditMetadata, key)
		}
		if err := rejectSensitiveAuthAuditValue(value); err != nil {
			return err
		}
	}
	return nil
}

func rejectSensitiveAuthAuditValue(value any) error {
	if value == nil {
		return nil
	}
	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Pointer || reflected.Kind() == reflect.Interface {
		if reflected.IsNil() {
			return nil
		}
		reflected = reflected.Elem()
	}
	switch reflected.Kind() {
	case reflect.Map:
		if reflected.Type().Key().Kind() != reflect.String {
			return nil
		}
		for _, key := range reflected.MapKeys() {
			keyText := key.String()
			if sensitiveAuthAuditKey(keyText) {
				return fmt.Errorf("%w: %s", ErrSensitiveAuditMetadata, keyText)
			}
			if err := rejectSensitiveAuthAuditValue(reflected.MapIndex(key).Interface()); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < reflected.Len(); i++ {
			if err := rejectSensitiveAuthAuditValue(reflected.Index(i).Interface()); err != nil {
				return err
			}
		}
	}
	return nil
}

func sensitiveAuthAuditKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "token_prefix" {
		return false
	}
	for _, fragment := range []string{"token", "authorization", "cookie", "secret", "hash", "password", "bearer"} {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

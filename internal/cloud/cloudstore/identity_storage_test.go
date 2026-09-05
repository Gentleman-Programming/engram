package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func openIsolatedCloudStore(t *testing.T) *CloudStore {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	if dsn == "" {
		t.Skip("CLOUDSTORE_TEST_DSN not set — skipping integration test (requires Postgres)")
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		t.Skip("test requires URL-style CLOUDSTORE_TEST_DSN so a per-test search_path can be attached")
	}

	schema := fmt.Sprintf("cloudstore_identity_%d", time.Now().UnixNano())
	adminDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	t.Cleanup(func() { _ = adminDB.Close() })
	if _, err := adminDB.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`) })

	testDSN := dsn + "?search_path=" + schema
	if strings.Contains(dsn, "?") {
		testDSN = dsn + "&search_path=" + schema
	}
	cs, err := New(cloud.Config{DSN: testDSN})
	if err != nil {
		t.Fatalf("New isolated cloudstore: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestAuthFoundationMigrationsAreAdditive(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)

	for _, table := range []string{"cloud_principals", "cloud_human_users", "cloud_principal_tokens", "cloud_project_grants", "cloud_auth_audit_log"} {
		if !tableExists(t, cs.db, table) {
			t.Fatalf("expected migration to create %s", table)
		}
	}

	if _, err := cs.db.ExecContext(ctx, `INSERT INTO cloud_chunks (project_name, chunk_id, created_by, payload) VALUES ('identity-migration', 'chunk-1', 'tester', '{}'::jsonb)`); err != nil {
		t.Fatalf("insert existing sync chunk after migration: %v", err)
	}
	if _, err := cs.db.ExecContext(ctx, `INSERT INTO cloud_mutations (project, entity, entity_key, op, payload) VALUES ('identity-migration', 'session', 'session-1', 'upsert', '{}'::jsonb)`); err != nil {
		t.Fatalf("insert existing sync mutation after migration: %v", err)
	}
	if err := cs.migrate(ctx); err != nil {
		t.Fatalf("second migrate should be additive: %v", err)
	}

	var chunkCount, mutationCount int
	if err := cs.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_chunks WHERE project_name = 'identity-migration' AND chunk_id = 'chunk-1'`).Scan(&chunkCount); err != nil {
		t.Fatalf("count preserved chunk: %v", err)
	}
	if err := cs.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_mutations WHERE project = 'identity-migration' AND entity_key = 'session-1'`).Scan(&mutationCount); err != nil {
		t.Fatalf("count preserved mutation: %v", err)
	}
	if chunkCount != 1 || mutationCount != 1 {
		t.Fatalf("migration must preserve existing sync rows, chunks=%d mutations=%d", chunkCount, mutationCount)
	}
}

func TestCloudstorePrincipalHumanTokenGrantAndAuditLifecycle(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)

	principal, err := cs.CreatePrincipal(ctx, CreatePrincipalParams{Kind: PrincipalKindHuman, DisplayName: "Alice Admin", Role: PrincipalRoleAdmin})
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	gotPrincipal, err := cs.GetPrincipal(ctx, principal.ID)
	if err != nil {
		t.Fatalf("GetPrincipal: %v", err)
	}
	if gotPrincipal.ID != principal.ID || gotPrincipal.Kind != PrincipalKindHuman || gotPrincipal.Role != PrincipalRoleAdmin || !gotPrincipal.Enabled {
		t.Fatalf("stored principal mismatch: %+v", gotPrincipal)
	}

	// The last-active-admin guard (guardLastActiveAdminTx) correctly refuses
	// to demote/disable the only active admin, so a second active admin must
	// exist before this test can legally exercise UpdatePrincipal's
	// role/enabled mutation on `principal` below.
	if _, err := cs.CreatePrincipal(ctx, CreatePrincipalParams{Kind: PrincipalKindHuman, DisplayName: "Backup Admin", Role: PrincipalRoleAdmin}); err != nil {
		t.Fatalf("CreatePrincipal backup admin: %v", err)
	}
	if err := cs.UpdatePrincipal(ctx, principal.ID, UpdatePrincipalParams{Role: PrincipalRoleMember, Enabled: false}); err != nil {
		t.Fatalf("UpdatePrincipal: %v", err)
	}
	updated, err := cs.GetPrincipal(ctx, principal.ID)
	if err != nil {
		t.Fatalf("GetPrincipal updated: %v", err)
	}
	if updated.Role != PrincipalRoleMember || updated.Enabled {
		t.Fatalf("principal update did not persist role/enabled: %+v", updated)
	}

	human, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "alice", Email: "alice@example.test", DisplayName: "Alice Human", Role: PrincipalRoleAdmin})
	if err != nil {
		t.Fatalf("CreateHumanUser: %v", err)
	}
	users, err := cs.ListHumanUsers(ctx)
	if err != nil {
		t.Fatalf("ListHumanUsers: %v", err)
	}
	if len(users) != 1 || users[0].PrincipalID != human.PrincipalID || users[0].Username != "alice" || !users[0].Enabled || users[0].Role != PrincipalRoleAdmin {
		t.Fatalf("human user listing mismatch: %+v", users)
	}
	if _, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "backup", Email: "backup@example.test", DisplayName: "Backup", Role: PrincipalRoleAdmin}); err != nil {
		t.Fatalf("CreateHumanUser backup admin: %v", err)
	}
	if err := cs.SetHumanUserEnabled(ctx, human.PrincipalID, false); err != nil {
		t.Fatalf("SetHumanUserEnabled(false): %v", err)
	}
	users, err = cs.ListHumanUsers(ctx)
	if err != nil {
		t.Fatalf("ListHumanUsers after disable: %v", err)
	}
	if users[0].Enabled {
		t.Fatalf("disabled human user should list as disabled: %+v", users[0])
	}
	if err := cs.SetHumanUserEnabled(ctx, human.PrincipalID, true); err != nil {
		t.Fatalf("SetHumanUserEnabled(true): %v", err)
	}

	token, err := cs.CreatePrincipalToken(ctx, CreatePrincipalTokenParams{PrincipalID: human.PrincipalID, TokenPrefix: "egc_live_ab12cd34", TokenHash: "hmac-sha256:v1:hash-only", Name: "laptop", CreatedByPrincipalID: principal.ID})
	if err != nil {
		t.Fatalf("CreatePrincipalToken: %v", err)
	}
	tokens, err := cs.ListPrincipalTokens(ctx, human.PrincipalID)
	if err != nil {
		t.Fatalf("ListPrincipalTokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].ID != token.ID || tokens[0].TokenPrefix != "egc_live_ab12cd34" || tokens[0].TokenHash != "" || tokens[0].RevokedAt != nil {
		t.Fatalf("token metadata listing must expose prefix only and no hash/raw token: %+v", tokens)
	}
	storedHash, err := rawStoredTokenHash(ctx, cs.db, token.ID)
	if err != nil {
		t.Fatalf("raw stored token hash: %v", err)
	}
	if storedHash != "hmac-sha256:v1:hash-only" || strings.Contains(storedHash, "raw") {
		t.Fatalf("database must persist hash-only verifier, got %q", storedHash)
	}
	if err := cs.RevokePrincipalToken(ctx, token.ID, principal.ID, "rotated"); err != nil {
		t.Fatalf("RevokePrincipalToken: %v", err)
	}
	tokens, err = cs.ListPrincipalTokens(ctx, human.PrincipalID)
	if err != nil {
		t.Fatalf("ListPrincipalTokens after revoke: %v", err)
	}
	if tokens[0].RevokedAt == nil || tokens[0].RevokedByPrincipalID != principal.ID || tokens[0].RevocationReason != "rotated" {
		t.Fatalf("token revocation metadata mismatch: %+v", tokens[0])
	}

	grant, err := cs.CreateProjectGrant(ctx, CreateProjectGrantParams{PrincipalID: human.PrincipalID, Project: "Alpha Project", GrantedByPrincipalID: principal.ID})
	if err != nil {
		t.Fatalf("CreateProjectGrant: %v", err)
	}
	if _, err := cs.CreateProjectGrant(ctx, CreateProjectGrantParams{PrincipalID: human.PrincipalID, Project: "alpha-project", GrantedByPrincipalID: principal.ID}); err != nil {
		t.Fatalf("duplicate CreateProjectGrant should be idempotent: %v", err)
	}
	grants, err := cs.ListProjectGrants(ctx, human.PrincipalID)
	if err != nil {
		t.Fatalf("ListProjectGrants: %v", err)
	}
	if len(grants) != 1 || grants[0].Project != "alpha-project" || grant.Project != "alpha-project" {
		t.Fatalf("expected one normalized project grant after duplicate insert, got initial=%+v list=%+v", grant, grants)
	}
	if err := cs.RevokeProjectGrant(ctx, human.PrincipalID, "alpha-project"); err != nil {
		t.Fatalf("RevokeProjectGrant: %v", err)
	}
	grants, err = cs.ListProjectGrants(ctx, human.PrincipalID)
	if err != nil {
		t.Fatalf("ListProjectGrants after revoke: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("revoked grant should not list, got %+v", grants)
	}

	if err := cs.InsertAuthAuditEvent(ctx, AuthAuditEvent{ActorPrincipalID: principal.ID, ActorSource: "managed", TargetPrincipalID: human.PrincipalID, Project: "alpha-project", Action: "grant.revoke", Outcome: "success", ReasonCode: "operator_request", Metadata: map[string]any{"token_prefix": "egc_live_ab12cd34"}}); err != nil {
		t.Fatalf("InsertAuthAuditEvent: %v", err)
	}
	events, err := cs.ListAuthAuditEvents(ctx, AuthAuditQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuthAuditEvents: %v", err)
	}
	if len(events) != 1 || events[0].Action != "grant.revoke" || events[0].Metadata["token_prefix"] != "egc_live_ab12cd34" {
		t.Fatalf("auth audit event mismatch: %+v", events)
	}
}

func TestCreatePrincipalTokenRejectsDisabledPrincipalWithoutPersisting(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)

	user, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "disabled-token-target", Email: "disabled-token-target@example.test", DisplayName: "Disabled Target", Role: PrincipalRoleMember})
	if err != nil {
		t.Fatalf("CreateHumanUser: %v", err)
	}
	if err := cs.SetHumanUserEnabled(ctx, user.PrincipalID, false); err != nil {
		t.Fatalf("SetHumanUserEnabled(false): %v", err)
	}

	_, err = cs.CreatePrincipalToken(ctx, CreatePrincipalTokenParams{PrincipalID: user.PrincipalID, TokenPrefix: "egc_live_disabled", TokenHash: "hmac-sha256:v1:disabled-hash", Name: "blocked"})
	if !errors.Is(err, ErrPrincipalDisabled) {
		t.Fatalf("expected ErrPrincipalDisabled for disabled principal token create, got %v", err)
	}
	tokens, err := cs.ListPrincipalTokens(ctx, user.PrincipalID)
	if err != nil {
		t.Fatalf("ListPrincipalTokens after disabled create attempt: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("disabled principal token create must not persist metadata, got %+v", tokens)
	}
}

func TestCreatePrincipalTokenWithAuditRollsBackTokenWhenAuditValidationFails(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)

	actor, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "audit-actor", Email: "audit-actor@example.test", DisplayName: "Audit Actor", Role: PrincipalRoleAdmin})
	if err != nil {
		t.Fatalf("CreateHumanUser actor: %v", err)
	}
	target, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "audit-target", Email: "audit-target@example.test", DisplayName: "Audit Target", Role: PrincipalRoleMember})
	if err != nil {
		t.Fatalf("CreateHumanUser target: %v", err)
	}

	_, err = cs.CreatePrincipalTokenWithAudit(ctx,
		CreatePrincipalTokenParams{PrincipalID: target.PrincipalID, TokenPrefix: "egc_live_atomic", TokenHash: "hmac-sha256:v1:atomic-rollback", Name: "laptop", CreatedByPrincipalID: actor.PrincipalID},
		AuthAuditEvent{ActorPrincipalID: actor.PrincipalID, ActorSource: "managed", TargetPrincipalID: target.PrincipalID, Action: "token.create", Outcome: "success", Metadata: map[string]any{"raw_token": "egc_live_secret"}},
	)
	if !errors.Is(err, ErrSensitiveAuditMetadata) {
		t.Fatalf("expected sensitive audit metadata validation error, got %v", err)
	}
	tokens, err := cs.ListPrincipalTokens(ctx, target.PrincipalID)
	if err != nil {
		t.Fatalf("ListPrincipalTokens after failed atomic create: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("atomic audit failure must roll back token insert, got %+v", tokens)
	}
	events, err := cs.ListAuthAuditEvents(ctx, AuthAuditQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuthAuditEvents after failed atomic create: %v", err)
	}
	for _, event := range events {
		if event.Action == "token.create" {
			t.Fatalf("failed atomic create must not persist token.create audit, got %+v", event)
		}
	}
}

// TestBootstrapCompletionAuditsPersistBooleanIssuedToken proves the real
// CloudStore audit boundary accepts and round-trips the completion metadata
// emitted by token and grant-only CLI bootstrap paths.
func TestBootstrapCompletionAuditsPersistBooleanIssuedToken(t *testing.T) {
	cases := []struct {
		name        string
		issuedToken bool
	}{
		{name: "token bootstrap completion", issuedToken: true},
		{name: "grant-only bootstrap completion", issuedToken: false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cs := openIsolatedCloudStore(t)
			admin, err := cs.CreateFirstAdminHumanUser(ctx, CreateHumanUserParams{Username: "bootstrap-completion", DisplayName: "Bootstrap Completion"})
			if err != nil {
				t.Fatalf("CreateFirstAdminHumanUser: %v", err)
			}

			metadata := map[string]any{
				"created_admin": true,
				"username":      admin.Username,
				"issued_token":  tt.issuedToken,
			}
			event := AuthAuditEvent{
				ActorSource:       "bootstrap_cli",
				TargetPrincipalID: admin.PrincipalID,
				Action:            "bootstrap.cli",
				Outcome:           "success",
				ReasonCode:        "bootstrap_completed",
				Metadata:          metadata,
			}
			if tt.issuedToken {
				_, err = cs.CreatePrincipalTokenWithAudit(ctx, CreatePrincipalTokenParams{
					PrincipalID:          admin.PrincipalID,
					TokenPrefix:          "egc_live_bootstrap",
					TokenHash:            "hmac-sha256:v1:bootstrap",
					Name:                 "bootstrap",
					CreatedByPrincipalID: admin.PrincipalID,
				}, event)
			} else {
				_, err = cs.CreateProjectGrant(ctx, CreateProjectGrantParams{
					PrincipalID:          admin.PrincipalID,
					Project:              "bootstrap-completion",
					GrantedByPrincipalID: admin.PrincipalID,
				})
				if err == nil {
					err = cs.InsertAuthAuditEvent(ctx, event)
				}
			}
			if err != nil {
				t.Fatalf("persist bootstrap completion audit: %v", err)
			}

			events, err := cs.ListAuthAuditEvents(ctx, AuthAuditQuery{Limit: 10})
			if err != nil {
				t.Fatalf("ListAuthAuditEvents: %v", err)
			}
			if len(events) != 1 || events[0].ReasonCode != "bootstrap_completed" || events[0].Metadata["issued_token"] != tt.issuedToken {
				t.Fatalf("expected persisted completion audit with issued_token=%t, got %+v", tt.issuedToken, events)
			}
		})
	}
}

func TestRecoverStrandedAdminTokenWithAuditPreservesGrants(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)

	admin, err := cs.CreateFirstAdminHumanUser(ctx, CreateHumanUserParams{Username: "stranded-admin", DisplayName: "Stranded Admin"})
	if err != nil {
		t.Fatalf("CreateFirstAdminHumanUser: %v", err)
	}
	if _, err := cs.CreateProjectGrant(ctx, CreateProjectGrantParams{PrincipalID: admin.PrincipalID, Project: "recovery-project", GrantedByPrincipalID: admin.PrincipalID}); err != nil {
		t.Fatalf("CreateProjectGrant: %v", err)
	}

	token, err := cs.RecoverStrandedAdminTokenWithAudit(ctx, RecoverStrandedAdminTokenParams{TokenPrefix: "egc_live_recovery", TokenHash: "hmac-sha256:v1:recovery", Name: "recovered"}, AuthAuditEvent{
		ActorSource: "bootstrap_cli",
		Action:      "bootstrap.cli",
		Outcome:     "success",
		ReasonCode:  "stranded_admin_token_recovered",
		Metadata:    map[string]any{"recovered": true, "token_prefix": "egc_live_recovery"},
	})
	if err != nil {
		t.Fatalf("RecoverStrandedAdminTokenWithAudit: %v", err)
	}
	if token.PrincipalID != admin.PrincipalID || token.CreatedByPrincipalID != admin.PrincipalID {
		t.Fatalf("recovery token must belong to and be attributed to the stranded admin, got %+v", token)
	}
	grants, err := cs.ListProjectGrants(ctx, admin.PrincipalID)
	if err != nil {
		t.Fatalf("ListProjectGrants: %v", err)
	}
	assertProjects(t, grants, []string{"recovery-project"})
	events, err := cs.ListAuthAuditEvents(ctx, AuthAuditQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuthAuditEvents: %v", err)
	}
	if len(events) != 1 || events[0].TargetPrincipalID != admin.PrincipalID || events[0].ReasonCode != "stranded_admin_token_recovered" || events[0].Metadata["recovered"] != true {
		t.Fatalf("expected one recovery audit attributed to the recovered admin, got %+v", events)
	}
}

func TestRecoverStrandedAdminTokenWithAuditRejectsUnsafeOrRepeatedState(t *testing.T) {
	cases := []struct {
		name           string
		setup          func(t *testing.T, cs *CloudStore, admin HumanUser)
		params         RecoverStrandedAdminTokenParams
		wantErr        string
		wantTokenCount int
	}{
		{
			name: "multiple enabled human admins",
			setup: func(t *testing.T, cs *CloudStore, _ HumanUser) {
				t.Helper()
				if _, err := cs.CreateHumanUser(context.Background(), CreateHumanUserParams{Username: "second-admin", DisplayName: "Second Admin", Role: PrincipalRoleAdmin}); err != nil {
					t.Fatalf("CreateHumanUser second admin: %v", err)
				}
			},
			wantErr: ErrStrandedAdminRecoveryIneligible.Error(),
		},
		{
			name: "zero enabled human admins",
			setup: func(t *testing.T, cs *CloudStore, admin HumanUser) {
				t.Helper()
				if _, err := cs.CreatePrincipal(context.Background(), CreatePrincipalParams{Kind: PrincipalKindServiceAccount, DisplayName: "Recovery Backup", Role: PrincipalRoleAdmin}); err != nil {
					t.Fatalf("CreatePrincipal backup admin: %v", err)
				}
				if err := cs.SetHumanUserEnabled(context.Background(), admin.PrincipalID, false); err != nil {
					t.Fatalf("SetHumanUserEnabled stranded admin false: %v", err)
				}
			},
			wantErr: ErrStrandedAdminRecoveryIneligible.Error(),
		},
		{
			name: "existing token anywhere",
			setup: func(t *testing.T, cs *CloudStore, admin HumanUser) {
				t.Helper()
				if _, err := cs.CreatePrincipalToken(context.Background(), CreatePrincipalTokenParams{PrincipalID: admin.PrincipalID, TokenPrefix: "egc_live_existing", TokenHash: "hmac-sha256:v1:existing", Name: "existing", CreatedByPrincipalID: admin.PrincipalID}); err != nil {
					t.Fatalf("CreatePrincipalToken setup: %v", err)
				}
			},
			wantErr:        ErrStrandedAdminRecoveryIneligible.Error(),
			wantTokenCount: 1,
		},
		{
			name: "revoked token row",
			setup: func(t *testing.T, cs *CloudStore, admin HumanUser) {
				t.Helper()
				token, err := cs.CreatePrincipalToken(context.Background(), CreatePrincipalTokenParams{PrincipalID: admin.PrincipalID, TokenPrefix: "egc_live_revoked", TokenHash: "hmac-sha256:v1:revoked", Name: "revoked", CreatedByPrincipalID: admin.PrincipalID})
				if err != nil {
					t.Fatalf("CreatePrincipalToken setup: %v", err)
				}
				if err := cs.RevokePrincipalToken(context.Background(), token.ID, admin.PrincipalID, "setup"); err != nil {
					t.Fatalf("RevokePrincipalToken setup: %v", err)
				}
			},
			wantErr:        ErrStrandedAdminRecoveryIneligible.Error(),
			wantTokenCount: 1,
		},
		{
			name:    "empty token prefix",
			params:  RecoverStrandedAdminTokenParams{TokenHash: "hmac-sha256:v1:rejected"},
			wantErr: "cloudstore: token prefix is required",
		},
		{
			name:    "empty token hash",
			params:  RecoverStrandedAdminTokenParams{TokenPrefix: "egc_live_rejected"},
			wantErr: "cloudstore: token hash is required",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cs := openIsolatedCloudStore(t)
			admin, err := cs.CreateFirstAdminHumanUser(ctx, CreateHumanUserParams{Username: "recovery-target", DisplayName: "Recovery Target"})
			if err != nil {
				t.Fatalf("CreateFirstAdminHumanUser: %v", err)
			}
			if tt.setup != nil {
				tt.setup(t, cs, admin)
			}

			params := tt.params
			if params.TokenPrefix == "" && params.TokenHash == "" {
				params = RecoverStrandedAdminTokenParams{TokenPrefix: "egc_live_rejected", TokenHash: "hmac-sha256:v1:rejected"}
			}
			_, err = cs.RecoverStrandedAdminTokenWithAudit(ctx, params, AuthAuditEvent{ActorSource: "bootstrap_cli", Action: "bootstrap.cli", Outcome: "success", Metadata: map[string]any{"recovered": true}})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected recovery rejection containing %q, got %v", tt.wantErr, err)
			}
			tokens, err := cs.ListPrincipalTokens(ctx, admin.PrincipalID)
			if err != nil {
				t.Fatalf("ListPrincipalTokens after rejected recovery: %v", err)
			}
			if len(tokens) != tt.wantTokenCount {
				t.Fatalf("rejected recovery must not change token rows, got %+v want %d tokens", tokens, tt.wantTokenCount)
			}
			events, err := cs.ListAuthAuditEvents(ctx, AuthAuditQuery{Limit: 10})
			if err != nil {
				t.Fatalf("ListAuthAuditEvents: %v", err)
			}
			if len(events) != 0 {
				t.Fatalf("ineligible recovery must not write an audit event, got %+v", events)
			}
		})
	}
}

func TestRecoverStrandedAdminTokenWithAuditRejectsTokenOwnedByNonAdmin(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)
	admin, err := cs.CreateFirstAdminHumanUser(ctx, CreateHumanUserParams{Username: "recovery-target", DisplayName: "Recovery Target"})
	if err != nil {
		t.Fatalf("CreateFirstAdminHumanUser: %v", err)
	}
	other, err := cs.CreatePrincipal(ctx, CreatePrincipalParams{Kind: PrincipalKindServiceAccount, DisplayName: "Recovery Member", Role: PrincipalRoleMember})
	if err != nil {
		t.Fatalf("CreatePrincipal non-admin token owner: %v", err)
	}
	existing, err := cs.CreatePrincipalToken(ctx, CreatePrincipalTokenParams{PrincipalID: other.ID, TokenPrefix: "egc_live_other", TokenHash: "hmac-sha256:v1:other", Name: "other", CreatedByPrincipalID: admin.PrincipalID})
	if err != nil {
		t.Fatalf("CreatePrincipalToken non-admin token owner: %v", err)
	}

	_, err = cs.RecoverStrandedAdminTokenWithAudit(ctx, RecoverStrandedAdminTokenParams{TokenPrefix: "egc_live_rejected", TokenHash: "hmac-sha256:v1:rejected"}, AuthAuditEvent{ActorSource: "bootstrap_cli", Action: "bootstrap.cli", Outcome: "success", Metadata: map[string]any{"recovered": true}})
	if !errors.Is(err, ErrStrandedAdminRecoveryIneligible) {
		t.Fatalf("expected ErrStrandedAdminRecoveryIneligible, got %v", err)
	}
	targetTokens, err := cs.ListPrincipalTokens(ctx, admin.PrincipalID)
	if err != nil {
		t.Fatalf("ListPrincipalTokens recovery target: %v", err)
	}
	if len(targetTokens) != 0 {
		t.Fatalf("ineligible recovery must not create a target token, got %+v", targetTokens)
	}
	otherTokens, err := cs.ListPrincipalTokens(ctx, other.ID)
	if err != nil {
		t.Fatalf("ListPrincipalTokens non-admin token owner: %v", err)
	}
	if len(otherTokens) != 1 || otherTokens[0].ID != existing.ID || otherTokens[0].PrincipalID != other.ID || otherTokens[0].TokenPrefix != existing.TokenPrefix || otherTokens[0].Name != existing.Name || otherTokens[0].CreatedByPrincipalID != admin.PrincipalID || otherTokens[0].RevokedAt != nil {
		t.Fatalf("ineligible recovery must preserve the non-admin token, got %+v want %+v", otherTokens, existing)
	}
	events, err := cs.ListAuthAuditEvents(ctx, AuthAuditQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuthAuditEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("ineligible recovery must not write an audit event, got %+v", events)
	}
}

func TestRecoverStrandedAdminTokenWithAuditRollsBackTokenWhenAuditFails(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)
	admin, err := cs.CreateFirstAdminHumanUser(ctx, CreateHumanUserParams{Username: "audit-recovery", DisplayName: "Audit Recovery"})
	if err != nil {
		t.Fatalf("CreateFirstAdminHumanUser: %v", err)
	}
	if _, err := cs.CreateProjectGrant(ctx, CreateProjectGrantParams{PrincipalID: admin.PrincipalID, Project: "preserved-project", GrantedByPrincipalID: admin.PrincipalID}); err != nil {
		t.Fatalf("CreateProjectGrant: %v", err)
	}

	_, err = cs.RecoverStrandedAdminTokenWithAudit(ctx, RecoverStrandedAdminTokenParams{TokenPrefix: "egc_live_audit", TokenHash: "hmac-sha256:v1:audit"}, AuthAuditEvent{ActorSource: "bootstrap_cli", Action: "bootstrap.cli", Outcome: "success", Metadata: map[string]any{"raw_token": "must-not-persist"}})
	if !errors.Is(err, ErrSensitiveAuditMetadata) {
		t.Fatalf("expected sensitive audit metadata error, got %v", err)
	}
	tokens, err := cs.ListPrincipalTokens(ctx, admin.PrincipalID)
	if err != nil {
		t.Fatalf("ListPrincipalTokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("failed recovery audit must roll back token creation, got %+v", tokens)
	}
	grants, err := cs.ListProjectGrants(ctx, admin.PrincipalID)
	if err != nil {
		t.Fatalf("ListProjectGrants: %v", err)
	}
	assertProjects(t, grants, []string{"preserved-project"})
}

func TestPrincipalTokenIssuanceSerializesWithStrandedRecoveryEligibility(t *testing.T) {
	cases := []struct {
		name  string
		issue func(context.Context, *CloudStore, HumanUser) error
	}{
		{
			name: "without audit",
			issue: func(ctx context.Context, cs *CloudStore, admin HumanUser) error {
				_, err := cs.CreatePrincipalToken(ctx, CreatePrincipalTokenParams{PrincipalID: admin.PrincipalID, TokenPrefix: "egc_live_normal", TokenHash: "hmac-sha256:v1:normal", Name: "normal", CreatedByPrincipalID: admin.PrincipalID})
				return err
			},
		},
		{
			name: "with audit",
			issue: func(ctx context.Context, cs *CloudStore, admin HumanUser) error {
				_, err := cs.CreatePrincipalTokenWithAudit(ctx, CreatePrincipalTokenParams{PrincipalID: admin.PrincipalID, TokenPrefix: "egc_live_audited", TokenHash: "hmac-sha256:v1:audited", Name: "audited", CreatedByPrincipalID: admin.PrincipalID}, AuthAuditEvent{ActorPrincipalID: admin.PrincipalID, ActorSource: "managed", TargetPrincipalID: admin.PrincipalID, Action: "token.create", Outcome: "success", Metadata: map[string]any{"token_prefix": "egc_live_audited"}})
				return err
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cs := openIsolatedCloudStore(t)
			admin, err := cs.CreateFirstAdminHumanUser(ctx, CreateHumanUserParams{Username: "serialized-recovery", DisplayName: "Serialized Recovery"})
			if err != nil {
				t.Fatalf("CreateFirstAdminHumanUser: %v", err)
			}

			blocker, err := cs.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin advisory-lock blocker: %v", err)
			}
			defer func() { _ = blocker.Rollback() }()
			if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('engram_cloud_active_admin_guard'))`); err != nil {
				t.Fatalf("acquire advisory-lock blocker: %v", err)
			}

			issueErrs := make(chan error, 1)
			go func() { issueErrs <- tt.issue(ctx, cs, admin) }()
			waitForActiveAdminGuardWaiter(t, cs.db)

			if err := blocker.Commit(); err != nil {
				t.Fatalf("release advisory-lock blocker: %v", err)
			}
			if err := <-issueErrs; err != nil {
				t.Fatalf("normal token issuance after lock release: %v", err)
			}

			_, err = cs.RecoverStrandedAdminTokenWithAudit(ctx, RecoverStrandedAdminTokenParams{TokenPrefix: "egc_live_recovery-race", TokenHash: "hmac-sha256:v1:recovery-race"}, AuthAuditEvent{ActorSource: "bootstrap_cli", Action: "bootstrap.cli", Outcome: "success", Metadata: map[string]any{"recovered": true}})
			if !errors.Is(err, ErrStrandedAdminRecoveryIneligible) {
				t.Fatalf("recovery must see the serialized normal issuance and refuse, got %v", err)
			}
		})
	}
}

func TestTokenIssuanceAndLastAdminGuardLockTargetBeforeAdvisoryGuard(t *testing.T) {
	cases := []struct {
		name  string
		issue func(context.Context, *CloudStore, HumanUser) error
	}{
		{
			name: "without audit",
			issue: func(ctx context.Context, cs *CloudStore, admin HumanUser) error {
				_, err := cs.CreatePrincipalToken(ctx, CreatePrincipalTokenParams{PrincipalID: admin.PrincipalID, TokenPrefix: "egc_live_lock-order", TokenHash: "hmac-sha256:v1:lock-order", Name: "lock-order", CreatedByPrincipalID: admin.PrincipalID})
				return err
			},
		},
		{
			name: "with audit",
			issue: func(ctx context.Context, cs *CloudStore, admin HumanUser) error {
				_, err := cs.CreatePrincipalTokenWithAudit(ctx, CreatePrincipalTokenParams{PrincipalID: admin.PrincipalID, TokenPrefix: "egc_live_lock-order-audit", TokenHash: "hmac-sha256:v1:lock-order-audit", Name: "lock-order-audit", CreatedByPrincipalID: admin.PrincipalID}, AuthAuditEvent{ActorPrincipalID: admin.PrincipalID, ActorSource: "managed", TargetPrincipalID: admin.PrincipalID, Action: "token.create", Outcome: "success", Metadata: map[string]any{"token_prefix": "egc_live_lock-order-audit"}})
				return err
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cs := openIsolatedCloudStore(t)
			admin, err := cs.CreateFirstAdminHumanUser(ctx, CreateHumanUserParams{Username: "lock-order-admin", DisplayName: "Lock Order Admin"})
			if err != nil {
				t.Fatalf("CreateFirstAdminHumanUser: %v", err)
			}

			blocker, err := cs.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin principal-row blocker: %v", err)
			}
			defer func() { _ = blocker.Rollback() }()
			var lockedPrincipalID string
			if err := blocker.QueryRowContext(ctx, `SELECT id::text FROM cloud_principals WHERE id = $1 FOR UPDATE`, admin.PrincipalID).Scan(&lockedPrincipalID); err != nil {
				t.Fatalf("acquire principal-row blocker: %v", err)
			}

			operationCtx, cancel := context.WithTimeout(ctx, lockOrderOperationTimeout)
			defer cancel()
			errs := make(chan error, 2)
			go func() { errs <- tt.issue(operationCtx, cs, admin) }()
			go func() {
				errs <- cs.UpdatePrincipal(operationCtx, admin.PrincipalID, UpdatePrincipalParams{Role: PrincipalRoleAdmin, Enabled: true})
			}()
			waitForDatabaseLockWaiters(t, cs.db, 2)
			assertNoActiveAdminGuardHeld(t, cs.db)

			if err := blocker.Commit(); err != nil {
				t.Fatalf("release principal-row blocker: %v", err)
			}
			for range 2 {
				select {
				case err := <-errs:
					if err != nil {
						t.Fatalf("concurrent operation must complete without a lock-order error: %v", err)
					}
				case <-time.After(lockOrderOperationTimeout):
					t.Fatal("concurrent token issuance and admin update did not complete after the principal-row blocker released")
				}
			}
		})
	}
}

const (
	lockOrderOperationTimeout = 5 * time.Second
	activeAdminGuardLockWhere = `
		locktype = 'advisory'
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		AND classid::bigint = ((hashtext('engram_cloud_active_admin_guard')::bigint >> 32) & 4294967295)
		AND objid::bigint = (hashtext('engram_cloud_active_admin_guard')::bigint & 4294967295)
		AND objsubid = 1`
)

func waitForActiveAdminGuardWaiter(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var waiters int
		if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pg_locks WHERE `+activeAdminGuardLockWhere+` AND granted = FALSE`).Scan(&waiters); err != nil {
			t.Fatalf("count active-admin guard waiters: %v", err)
		}
		if waiters > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("normal token issuance did not wait for engram_cloud_active_admin_guard")
}

func waitForDatabaseLockWaiters(t *testing.T, db *sql.DB, wantAtLeast int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var waiters int
		if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiters); err != nil {
			t.Fatalf("count database lock waiters: %v", err)
		}
		if waiters >= wantAtLeast {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected at least %d database lock waiters", wantAtLeast)
}

func assertNoActiveAdminGuardHeld(t *testing.T, db *sql.DB) {
	t.Helper()
	var held int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pg_locks WHERE `+activeAdminGuardLockWhere+` AND granted = TRUE`).Scan(&held); err != nil {
		t.Fatalf("count held advisory locks: %v", err)
	}
	if held != 0 {
		t.Fatalf("token issuance and last-admin guard must wait on the principal row before acquiring the advisory guard, found %d held advisory locks", held)
	}
}

func TestActiveAdminGuardLockQueriesIgnoreUnrelatedAdvisoryLocks(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin unrelated advisory-lock tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(42::bigint)`); err != nil {
		t.Fatalf("acquire unrelated advisory lock: %v", err)
	}
	assertNoActiveAdminGuardHeld(t, cs.db)
}

// TestFindPrincipalTokenByHashResolvesActiveTokenAndRejectsUnknownOrRevoked
// is the RED-first proof for the runtime managed-token wiring slice: before
// FindPrincipalTokenByHash existed, this test failed to compile (undefined
// method). It now proves the storage-only lookup that
// cloudauth.ManagedTokenLookup depends on at runtime: an active token's hash
// resolves to both the token metadata and its owning principal, an unknown
// hash returns ErrPrincipalTokenNotFound (not a raw sql.ErrNoRows), and a
// revoked token still resolves (RevokedAt non-nil) so the caller — not this
// storage method — makes the revoked/enabled decision, matching how
// cloudauth.PrincipalResolver.ResolveBearerToken already expects to receive
// the record and decide.
func TestFindPrincipalTokenByHashResolvesActiveTokenAndRejectsUnknownOrRevoked(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)

	human, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "runtime-bob", Email: "runtime-bob@example.test", DisplayName: "Runtime Bob", Role: PrincipalRoleMember})
	if err != nil {
		t.Fatalf("CreateHumanUser: %v", err)
	}
	const tokenHash = "hmac-sha256:v1:runtime-wiring-hash"
	created, err := cs.CreatePrincipalToken(ctx, CreatePrincipalTokenParams{PrincipalID: human.PrincipalID, TokenPrefix: "egc_live_rt001122", TokenHash: tokenHash, Name: "ci-runner"})
	if err != nil {
		t.Fatalf("CreatePrincipalToken: %v", err)
	}

	gotToken, gotPrincipal, err := cs.FindPrincipalTokenByHash(ctx, tokenHash)
	if err != nil {
		t.Fatalf("FindPrincipalTokenByHash active token: %v", err)
	}
	if gotToken.ID != created.ID || gotToken.PrincipalID != human.PrincipalID || gotToken.TokenHash != "" || gotToken.RevokedAt != nil {
		t.Fatalf("unexpected token record: %+v", gotToken)
	}
	if gotPrincipal.ID != human.PrincipalID || gotPrincipal.Kind != PrincipalKindHuman || gotPrincipal.Role != PrincipalRoleMember || !gotPrincipal.Enabled {
		t.Fatalf("unexpected principal record: %+v", gotPrincipal)
	}

	if _, _, err := cs.FindPrincipalTokenByHash(ctx, "hmac-sha256:v1:never-issued"); !errors.Is(err, ErrPrincipalTokenNotFound) {
		t.Fatalf("expected ErrPrincipalTokenNotFound for unknown hash, got %v", err)
	}

	if err := cs.RevokePrincipalToken(ctx, created.ID, human.PrincipalID, "rotated"); err != nil {
		t.Fatalf("RevokePrincipalToken: %v", err)
	}
	revokedToken, _, err := cs.FindPrincipalTokenByHash(ctx, tokenHash)
	if err != nil {
		t.Fatalf("FindPrincipalTokenByHash revoked token: %v", err)
	}
	if revokedToken.RevokedAt == nil {
		t.Fatal("expected revoked token lookup to still resolve with a non-nil RevokedAt so the caller can reject it")
	}
}

// TestNormalizeProjectGrantMatchesInternalNormalization is a pure (non-DSN)
// RED-first test proving the exported NormalizeProjectGrant helper — added
// so the cmd/engram runtime PrincipalProjectAuthorizer adapter can normalize
// a request project exactly like CreateProjectGrant does before storing —
// stays byte-for-byte identical to the package's own internal normalization.
func TestNormalizeProjectGrantMatchesInternalNormalization(t *testing.T) {
	cases := []string{"Alpha Project", "alpha-project", "  Weird!!Chars__here  ", ""}
	for _, input := range cases {
		if got, want := NormalizeProjectGrant(input), normalizeCloudProjectGrant(input); got != want {
			t.Fatalf("NormalizeProjectGrant(%q) = %q, want %q (must match normalizeCloudProjectGrant)", input, got, want)
		}
	}
}

func TestCloudstoreIdentityGuardsAndErrorPaths(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)

	if _, err := cs.CreatePrincipal(ctx, CreatePrincipalParams{Kind: "robot", DisplayName: "Bad", Role: PrincipalRoleMember}); err == nil {
		t.Fatal("invalid principal kind must be rejected")
	}
	if _, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "owner", DisplayName: "Owner", Role: "owner"}); err == nil {
		t.Fatal("invalid human role must be rejected")
	}

	admin, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "admin", Email: "admin@example.test", DisplayName: "Admin", Role: PrincipalRoleAdmin})
	if err != nil {
		t.Fatalf("CreateHumanUser admin: %v", err)
	}
	if _, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "admin", Email: "other@example.test", DisplayName: "Duplicate Username", Role: PrincipalRoleMember}); err == nil {
		t.Fatal("duplicate username must be rejected")
	}
	if _, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "admin2", Email: "admin@example.test", DisplayName: "Duplicate Email", Role: PrincipalRoleMember}); err == nil {
		t.Fatal("duplicate email must be rejected")
	}

	hasAdmin, err := cs.HasActiveAdmin(ctx)
	if err != nil {
		t.Fatalf("HasActiveAdmin: %v", err)
	}
	if !hasAdmin {
		t.Fatal("active admin should exist after creating enabled admin human")
	}
	wouldRemove, err := cs.WouldRemoveLastActiveAdmin(ctx, admin.PrincipalID)
	if err != nil {
		t.Fatalf("WouldRemoveLastActiveAdmin: %v", err)
	}
	if !wouldRemove {
		t.Fatal("single enabled admin should trigger last-admin guard")
	}
	if err := cs.SetHumanUserEnabled(ctx, admin.PrincipalID, false); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("disabling last active admin must fail with ErrLastActiveAdmin, got %v", err)
	}
	if err := cs.UpdatePrincipal(ctx, admin.PrincipalID, UpdatePrincipalParams{Role: PrincipalRoleMember, Enabled: true}); !errors.Is(err, ErrLastActiveAdmin) {
		t.Fatalf("demoting last active admin must fail with ErrLastActiveAdmin, got %v", err)
	}
	if _, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "backup", Email: "backup@example.test", DisplayName: "Backup", Role: PrincipalRoleAdmin}); err != nil {
		t.Fatalf("CreateHumanUser backup admin: %v", err)
	}
	wouldRemove, err = cs.WouldRemoveLastActiveAdmin(ctx, admin.PrincipalID)
	if err != nil {
		t.Fatalf("WouldRemoveLastActiveAdmin with backup: %v", err)
	}
	if wouldRemove {
		t.Fatal("two enabled admins should not trigger last-admin guard for one admin")
	}

	if _, err := cs.CreatePrincipalToken(ctx, CreatePrincipalTokenParams{PrincipalID: admin.PrincipalID, TokenPrefix: "egc_live_dup", TokenHash: "hmac-sha256:v1:dup", Name: "one"}); err != nil {
		t.Fatalf("CreatePrincipalToken one: %v", err)
	}
	if _, err := cs.CreatePrincipalToken(ctx, CreatePrincipalTokenParams{PrincipalID: admin.PrincipalID, TokenPrefix: "egc_live_dup2", TokenHash: "hmac-sha256:v1:dup", Name: "two"}); err == nil {
		t.Fatal("duplicate token hash must be rejected")
	}

	if _, err := cs.CreateProjectGrant(ctx, CreateProjectGrantParams{PrincipalID: admin.PrincipalID, Project: ""}); err == nil {
		t.Fatal("empty project grant must be rejected")
	}
	if err := cs.InsertAuthAuditEvent(ctx, AuthAuditEvent{ActorSource: "managed", Action: "token.create", Outcome: "success", Metadata: map[string]any{"raw_token": "egc_live_secret"}}); !errors.Is(err, ErrSensitiveAuditMetadata) {
		t.Fatalf("raw token audit metadata must be rejected, got %v", err)
	}
	if err := cs.InsertAuthAuditEvent(ctx, AuthAuditEvent{ActorSource: "managed", Action: "token.create", Outcome: "success", Metadata: map[string]any{"token_hash": "hmac-sha256:v1:secret"}}); !errors.Is(err, ErrSensitiveAuditMetadata) {
		t.Fatalf("token hash audit metadata must be rejected, got %v", err)
	}
	if err := cs.InsertAuthAuditEvent(ctx, AuthAuditEvent{ActorSource: "managed", Action: "token.create", Outcome: "success", Metadata: map[string]any{"events": []any{map[string]any{"raw_token": "secret"}}}}); !errors.Is(err, ErrSensitiveAuditMetadata) {
		t.Fatalf("nested array token audit metadata must be rejected, got %v", err)
	}
}

func TestCloudstoreLastActiveAdminGuardSerializesConcurrentRemoval(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)

	first, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "first", Email: "first@example.test", DisplayName: "First", Role: PrincipalRoleAdmin})
	if err != nil {
		t.Fatalf("CreateHumanUser first: %v", err)
	}
	second, err := cs.CreateHumanUser(ctx, CreateHumanUserParams{Username: "second", Email: "second@example.test", DisplayName: "Second", Role: PrincipalRoleAdmin})
	if err != nil {
		t.Fatalf("CreateHumanUser second: %v", err)
	}

	errs := make(chan error, 2)
	go func() { errs <- cs.SetHumanUserEnabled(ctx, first.PrincipalID, false) }()
	go func() { errs <- cs.SetHumanUserEnabled(ctx, second.PrincipalID, false) }()

	firstErr := <-errs
	secondErr := <-errs
	if firstErr == nil && secondErr == nil {
		t.Fatal("concurrent disable of both admins must not both succeed")
	}
	if firstErr != nil && secondErr != nil {
		t.Fatalf("exactly one concurrent disable should succeed, got %v and %v", firstErr, secondErr)
	}
	hasAdmin, err := cs.HasActiveAdmin(ctx)
	if err != nil {
		t.Fatalf("HasActiveAdmin after concurrent disable: %v", err)
	}
	if !hasAdmin {
		t.Fatal("last-admin guard must leave at least one active admin")
	}
}

// TestCreateFirstAdminHumanUserSerializesConcurrentBootstrap proves
// CreateFirstAdminHumanUser closes the CLI/dashboard first-admin bootstrap
// TOCTOU race at the real Postgres layer: N concurrent callers racing to
// bootstrap the first admin against the same database must result in
// exactly one created admin and every other attempt receiving
// ErrAdminAlreadyExists, never two (or more) admins created.
func TestCreateFirstAdminHumanUserSerializesConcurrentBootstrap(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)

	const attempts = 5
	type result struct {
		user HumanUser
		err  error
	}
	results := make(chan result, attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			user, err := cs.CreateFirstAdminHumanUser(ctx, CreateHumanUserParams{
				Username: fmt.Sprintf("racer-%d", i),
				Email:    fmt.Sprintf("racer-%d@example.test", i),
			})
			results <- result{user: user, err: err}
		}(i)
	}

	var successes, duplicates int
	for i := 0; i < attempts; i++ {
		r := <-results
		switch {
		case r.err == nil:
			successes++
		case errors.Is(r.err, ErrAdminAlreadyExists):
			duplicates++
		default:
			t.Fatalf("unexpected error from concurrent first-admin bootstrap: %v", r.err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one concurrent bootstrap attempt to succeed, got %d successes and %d duplicates", successes, duplicates)
	}
	if duplicates != attempts-1 {
		t.Fatalf("expected the other %d attempts to be refused as duplicates, got %d", attempts-1, duplicates)
	}

	var adminCount int
	if err := cs.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_principals WHERE role = $1 AND enabled = TRUE`, PrincipalRoleAdmin).Scan(&adminCount); err != nil {
		t.Fatalf("count active admins: %v", err)
	}
	if adminCount != 1 {
		t.Fatalf("expected exactly 1 durably persisted admin despite concurrent bootstrap attempts, got %d", adminCount)
	}
}

func TestCreateFirstAdminHumanUserRequiresInitializedStore(t *testing.T) {
	cs := &CloudStore{}
	if _, err := cs.CreateFirstAdminHumanUser(context.Background(), CreateHumanUserParams{Username: "alice"}); err == nil {
		t.Fatal("expected an error from an uninitialized store, got nil")
	}
}

func TestCloudstoreIdentityPureHelpers(t *testing.T) {
	cases := map[string]string{
		"Alpha Project":     "alpha-project",
		" alpha   project ": "alpha-project",
		"alpha_project":     "alpha_project",
		"alpha.project":     "alpha.project",
		"!!!":               "",
	}
	for input, want := range cases {
		if got := normalizeCloudProjectGrant(input); got != want {
			t.Fatalf("normalizeCloudProjectGrant(%q) = %q, want %q", input, got, want)
		}
	}
	if sensitiveAuthAuditKey("token_prefix") {
		t.Fatal("token_prefix is safe metadata and should not be rejected")
	}
	for _, key := range []string{"raw_token", "authorization_header", "session_cookie", "token_hash", "password"} {
		if !sensitiveAuthAuditKey(key) {
			t.Fatalf("%s should be classified as sensitive audit metadata", key)
		}
	}
	if err := rejectSensitiveAuthAuditMetadata(map[string]any{"nested": map[string]any{"raw_token": "secret"}}); !errors.Is(err, ErrSensitiveAuditMetadata) {
		t.Fatalf("nested sensitive audit metadata must be rejected, got %v", err)
	}
	if err := rejectSensitiveAuthAuditMetadata(map[string]any{"nested": map[string]string{"raw_token": "secret"}}); !errors.Is(err, ErrSensitiveAuditMetadata) {
		t.Fatalf("typed nested map sensitive audit metadata must be rejected, got %v", err)
	}
	if err := rejectSensitiveAuthAuditMetadata(map[string]any{"events": []map[string]any{{"raw_token": "secret"}}}); !errors.Is(err, ErrSensitiveAuditMetadata) {
		t.Fatalf("typed nested slice sensitive audit metadata must be rejected, got %v", err)
	}
	// Regression: the bootstrap completion audit metadata (created by
	// cloudBootstrapCompletionMetadata) carries the boolean flag "issued_token".
	// It is not a secret and must be accepted, otherwise
	// `engram cloud bootstrap admin --issue-token` fails atomically and no admin
	// token is ever minted.
	if err := rejectSensitiveAuthAuditMetadata(map[string]any{"issued_token": true, "username": "admin", "created_admin": true}); err != nil {
		t.Fatalf("bootstrap completion metadata must be accepted, got %v", err)
	}
	// issued_token is false for grant-only bootstraps (no --issue-token); both
	// boolean values must be accepted.
	if err := rejectSensitiveAuthAuditMetadata(map[string]any{"issued_token": false}); err != nil {
		t.Fatalf("false issued_token must be accepted, got %v", err)
	}
	// issued_token is exempt ONLY as a boolean flag. A non-boolean value (e.g. a
	// string that could carry secret material) must NOT bypass the filter, at
	// the top level or nested.
	if err := rejectSensitiveAuthAuditMetadata(map[string]any{"issued_token": "egc_live_would_be_leaked"}); !errors.Is(err, ErrSensitiveAuditMetadata) {
		t.Fatalf("non-boolean issued_token must be rejected, got %v", err)
	}
	if err := rejectSensitiveAuthAuditMetadata(map[string]any{"outer": map[string]any{"issued_token": "egc_live_would_be_leaked"}}); !errors.Is(err, ErrSensitiveAuditMetadata) {
		t.Fatalf("nested non-boolean issued_token must be rejected, got %v", err)
	}
	if err := rejectSensitiveAuthAuditMetadata(map[string]any{"outer": map[string]any{"issued_token": true}}); err != nil {
		t.Fatalf("nested boolean issued_token must be accepted, got %v", err)
	}
}

func tableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1)`, table).Scan(&exists); err != nil {
		t.Fatalf("table exists %s: %v", table, err)
	}
	return exists
}

func rawStoredTokenHash(ctx context.Context, db *sql.DB, tokenID string) (string, error) {
	var hash string
	if err := db.QueryRowContext(ctx, `SELECT token_hash FROM cloud_principal_tokens WHERE id::text = $1`, tokenID).Scan(&hash); err != nil {
		return "", err
	}
	return hash, nil
}

func assertProjects(t *testing.T, got []ProjectGrant, want []string) {
	t.Helper()
	projects := make([]string, 0, len(got))
	for _, grant := range got {
		projects = append(projects, grant.Project)
	}
	if !slices.Equal(projects, want) {
		t.Fatalf("projects mismatch: got=%v want=%v", projects, want)
	}
}

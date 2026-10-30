package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// Both operations queue behind a real principal-row lock. Observe PostgreSQL's
// wait graph before releasing it; elapsed time is never ordering evidence.
func TestTokenUsageMarkRecoveryOrdering(t *testing.T) {
	for _, markFirst := range []bool{true, false} {
		name := "recovery first"
		if markFirst {
			name = "mark first"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cs := openIsolatedCloudStore(t)
			admin, err := cs.CreateFirstAdminHumanUser(ctx, CreateHumanUserParams{Username: "usage-admin", DisplayName: "Usage Admin"})
			if err != nil {
				t.Fatal(err)
			}
			token, err := cs.CreatePrincipalToken(ctx, CreatePrincipalTokenParams{PrincipalID: admin.PrincipalID, TokenPrefix: "original", TokenHash: "original-hash"})
			if err != nil {
				t.Fatal(err)
			}
			blocker, err := cs.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback() }()
			var blockerPID int
			if err := blocker.QueryRowContext(ctx, `SELECT pg_backend_pid() FROM cloud_principals WHERE id=$1 FOR UPDATE`, admin.PrincipalID).Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			markResult, recoveryResult := make(chan error, 1), make(chan error, 1)
			mark := func() {
				go func() { markResult <- cs.MarkPrincipalTokenUsed(ctx, token.ID) }()
			}
			recover := func() {
				go func() {
					_, err := cs.RecoverStrandedAdminTokenWithAudit(ctx, RecoverStrandedAdminTokenParams{TokenPrefix: "replacement", TokenHash: "replacement-hash", RevokeExisting: true}, AuthAuditEvent{})
					recoveryResult <- err
				}()
			}
			waitFor := func(pattern string, firstPID int) int {
				t.Helper()
				for {
					var pid int
					err := cs.db.QueryRowContext(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND query LIKE $1 AND ($2=ANY(pg_blocking_pids(pid)) OR $3=ANY(pg_blocking_pids(pid))) LIMIT 1`, pattern, blockerPID, firstPID).Scan(&pid)
					if err == nil {
						return pid
					}
					if ctx.Err() != nil {
						t.Fatalf("lock wait graph not observed for %s: %v", pattern, err)
					}
					if !errors.Is(err, sql.ErrNoRows) {
						t.Fatal(err)
					}
					time.Sleep(time.Millisecond)
				}
			}
			markPattern := "%SELECT enabled FROM cloud_principals WHERE id = $1 FOR NO KEY UPDATE%"
			recoveryPattern := "%JOIN cloud_human_users h ON h.principal_id = p.id%"
			if markFirst {
				mark()
				pid := waitFor(markPattern, blockerPID)
				recover()
				waitFor(recoveryPattern, pid)
			} else {
				recover()
				pid := waitFor(recoveryPattern, blockerPID)
				mark()
				waitFor(markPattern, pid)
			}
			if err := blocker.Commit(); err != nil {
				t.Fatal(err)
			}
			markErr, recoveryErr := <-markResult, <-recoveryResult
			if markFirst {
				if markErr != nil || !errors.Is(recoveryErr, ErrStrandedAdminRecoveryIneligible) {
					t.Fatalf("mark=%v recovery=%v", markErr, recoveryErr)
				}
			} else {
				if !errors.Is(markErr, ErrPrincipalTokenRevoked) || recoveryErr != nil {
					t.Fatalf("mark=%v recovery=%v", markErr, recoveryErr)
				}
			}
			tokens, err := cs.ListPrincipalTokens(ctx, admin.PrincipalID)
			if err != nil {
				t.Fatal(err)
			}
			for _, got := range tokens {
				if got.ID == token.ID && ((got.LastUsedAt != nil) != markFirst || (got.RevokedAt == nil) != markFirst) {
					t.Fatalf("original state: %+v", got)
				}
			}
		})
	}
}

// Revocation owns the token row before its foreign-key check takes KEY SHARE
// on the principal. Usage must not introduce the opposite lock dependency.
func TestTokenUsageConcurrentRevocationForeignKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cs := openIsolatedCloudStore(t)
	p, err := cs.CreatePrincipal(ctx, CreatePrincipalParams{Kind: PrincipalKindServiceAccount, Role: PrincipalRoleMember, DisplayName: "Revocation"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := cs.CreatePrincipalToken(ctx, CreatePrincipalTokenParams{PrincipalID: p.ID, TokenPrefix: "revoke", TokenHash: "revoke-hash"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var pid int
	if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid() FROM cloud_principal_tokens WHERE id=$1 FOR UPDATE`, token.ID).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- cs.MarkPrincipalTokenUsed(ctx, token.ID) }()
	for {
		var waiting bool
		err := cs.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%SELECT revoked_at FROM cloud_principal_tokens%')`, pid).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("usage token-lock wait was not observed")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cloud_principal_tokens SET revoked_at=clock_timestamp(), revoked_by_principal_id=$2 WHERE id=$1`, token.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrPrincipalTokenRevoked) {
		t.Fatalf("revocation must win: %v", err)
	}
}

func TestTokenUsagePersistence(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)
	p, err := cs.CreatePrincipal(ctx, CreatePrincipalParams{Kind: PrincipalKindServiceAccount, Role: PrincipalRoleMember, DisplayName: "Usage"})
	if err != nil {
		t.Fatal(err)
	}
	token, err := cs.CreatePrincipalToken(ctx, CreatePrincipalTokenParams{PrincipalID: p.ID, TokenPrefix: "usage", TokenHash: "usage-hash"})
	if err != nil {
		t.Fatal(err)
	}
	read := func() time.Time {
		t.Helper()
		tokens, err := cs.ListPrincipalTokens(ctx, p.ID)
		if err != nil || len(tokens) != 1 || tokens[0].LastUsedAt == nil {
			t.Fatalf("usage missing: %v %v", tokens, err)
		}
		return *tokens[0].LastUsedAt
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := cs.MarkPrincipalTokenUsed(canceled, token.ID); err == nil || errors.Is(err, ErrPrincipalTokenNotFound) || errors.Is(err, ErrPrincipalTokenRevoked) || errors.Is(err, ErrPrincipalDisabled) {
		t.Fatalf("storage failure must remain distinct from rejected state: %v", err)
	}
	unused, err := cs.ListPrincipalTokens(ctx, p.ID)
	if err != nil || len(unused) != 1 || unused[0].LastUsedAt != nil {
		t.Fatalf("failed storage operation marked token used: %+v %v", unused, err)
	}
	before := time.Now().Add(-time.Second)
	if err := cs.MarkPrincipalTokenUsed(ctx, token.ID); err != nil {
		t.Fatal(err)
	}
	first := read()
	if first.Before(before) || first.After(time.Now().Add(time.Second)) {
		t.Fatalf("not current database time: %v", first)
	}
	if err := cs.MarkPrincipalTokenUsed(ctx, token.ID); err != nil {
		t.Fatal(err)
	}
	if !read().After(first) {
		t.Fatal("repeat usage did not advance timestamp")
	}
	future := first.Add(time.Hour)
	if _, err := cs.db.ExecContext(ctx, `UPDATE cloud_principal_tokens SET last_used_at=$2 WHERE id=$1`, token.ID, future); err != nil {
		t.Fatal(err)
	}
	if err := cs.MarkPrincipalTokenUsed(ctx, token.ID); err != nil {
		t.Fatal(err)
	}
	if read().Before(future) {
		t.Fatal("usage moved backwards from future timestamp")
	}
	if err := cs.MarkPrincipalTokenUsed(ctx, "9223372036854775807"); !errors.Is(err, ErrPrincipalTokenNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := cs.UpdatePrincipal(ctx, p.ID, UpdatePrincipalParams{Role: PrincipalRoleMember, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := cs.MarkPrincipalTokenUsed(ctx, token.ID); !errors.Is(err, ErrPrincipalDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if err := cs.UpdatePrincipal(ctx, p.ID, UpdatePrincipalParams{Role: PrincipalRoleMember, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := cs.RevokePrincipalToken(ctx, token.ID, "", "test"); err != nil {
		t.Fatal(err)
	}
	if err := cs.MarkPrincipalTokenUsed(ctx, token.ID); !errors.Is(err, ErrPrincipalTokenRevoked) {
		t.Fatalf("revoked: %v", err)
	}
	if !read().Equal(future) {
		t.Fatal("rejected use changed timestamp")
	}
}

package cloudstore

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestPromptPairClaimRequiresRegistration(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	if _, err := cs.db.ExecContext(ctx, `INSERT INTO cloud_project_sessions (project_name, session_id) VALUES ('alpha', 'unverified')`); err != nil {
		t.Fatal(err)
	}
	if err := cs.ClaimPromptPair(ctx, "unverified", "inbox", "sync", "beta", "actor"); !errors.Is(err, ErrSessionAuthorityNotFound) {
		t.Fatalf("chunk index authorized claim: %v", err)
	}
	claim, err := cs.GetPromptPairClaim(ctx, "unverified", "inbox")
	if err != nil || claim != nil {
		t.Fatalf("unverified claim persisted: %+v %v", claim, err)
	}
}

func TestPromptPairClaimCrossProjectReplayAndConflicts(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	for _, registration := range [][2]string{{"alpha-session", "alpha/foo"}, {"other-session", "alpha-foo"}} {
		if err := cs.RegisterSessionAuthority(ctx, registration[0], registration[1], "registrar"); err != nil {
			t.Fatal(err)
		}
	}
	if err := cs.ClaimPromptPair(ctx, "alpha-session", "inbox", "sync", "beta", "first"); err != nil {
		t.Fatal(err)
	}
	original, err := cs.GetPromptPairClaim(ctx, "alpha-session", "inbox")
	if err != nil || original == nil || original.PromptProject != "beta" || original.ClaimedBy != "first" || original.ClaimedAt.IsZero() {
		t.Fatalf("claim: %+v %v", original, err)
	}
	if err := cs.ClaimPromptPair(ctx, "alpha-session", "inbox", "sync", "beta", "second"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][4]string{
		{"alpha-session", "inbox", "other-sync", "beta"},
		{"alpha-session", "inbox", "sync", "gamma"},
		{"alpha-session", "other-inbox", "sync", "beta"},
		{"other-session", "inbox", "sync", "beta"},
	} {
		if err := cs.ClaimPromptPair(ctx, args[0], args[1], args[2], args[3], "competitor"); !errors.Is(err, ErrPromptPairClaimConflict) {
			t.Fatalf("%v: %v", args, err)
		}
	}
	after, err := cs.GetPromptPairClaim(ctx, "alpha-session", "inbox")
	if err != nil || *after != *original {
		t.Fatalf("audit changed: %+v %v", after, err)
	}
}

func TestPromptPairClaimConcurrentWriters(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	if err := cs.RegisterSessionAuthority(ctx, "session", "alpha", "registrar"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			syncID := "same"
			if i%2 == 1 {
				syncID = "different"
			}
			results <- cs.ClaimPromptPair(ctx, "session", "inbox", syncID, "beta", "actor")
		}(i)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrPromptPairClaimConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 10 || conflicts != 10 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func TestPromptPairClaimConcurrentSyncReuseAcrossPairs(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	ctx := context.Background()
	if err := cs.RegisterSessionAuthority(ctx, "session", "alpha", "registrar"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			inbox := "first"
			if i%2 == 1 {
				inbox = "second"
			}
			results <- cs.ClaimPromptPair(ctx, "session", inbox, "shared-sync", "beta", "actor")
		}(i)
	}
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrPromptPairClaimConflict):
			conflicts++
		default:
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if successes != 10 || conflicts != 10 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestPromptPairClaimRejectsBlankInputs(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	for i := 0; i < 5; i++ {
		args := [5]string{"session", "inbox", "sync", "beta", "actor"}
		args[i] = "  "
		if err := cs.ClaimPromptPair(context.Background(), args[0], args[1], args[2], args[3], args[4]); err == nil {
			t.Fatalf("accepted blank field %d", i)
		}
	}
	for _, args := range [][2]string{{" ", "inbox"}, {"session", " "}} {
		if _, err := cs.GetPromptPairClaim(context.Background(), args[0], args[1]); err == nil {
			t.Fatalf("accepted blank lookup: %v", args)
		}
	}
}

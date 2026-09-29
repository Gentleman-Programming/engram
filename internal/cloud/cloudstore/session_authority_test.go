package cloudstore

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/cloud"
)

func TestSessionAuthorityExplicitRegistration(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)
	if _, err := cs.db.ExecContext(ctx, `INSERT INTO cloud_project_sessions (project_name, session_id) VALUES ('imported', 'session-1')`); err != nil {
		t.Fatal(err)
	}
	if authority, err := cs.GetSessionAuthority(ctx, "session-1"); err != nil || authority != nil {
		t.Fatalf("index must not confer authority: %+v, %v", authority, err)
	}
	if err := cs.RegisterSessionAuthority(ctx, " session-1 ", " alpha ", " actor-1 "); err != nil {
		t.Fatal(err)
	}
	if err := cs.RegisterSessionAuthority(ctx, "session-1", "alpha", "actor-2"); err != nil {
		t.Fatalf("same owner replay: %v", err)
	}
	authority, err := cs.GetSessionAuthority(ctx, "session-1")
	if err != nil || authority == nil || authority.OwnerProject != "alpha" || authority.RegisteredBy != "actor-1" || authority.RegisteredAt.IsZero() {
		t.Fatalf("original registration: %+v, %v", authority, err)
	}
	if err := cs.RegisterSessionAuthority(ctx, "session-1", "beta", "actor-3"); !errors.Is(err, ErrSessionAuthorityConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	var schema string
	if err := cs.db.QueryRowContext(ctx, `SHOW search_path`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	dsn := os.Getenv("CLOUDSTORE_TEST_DSN")
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	reopened, err := New(cloud.Config{DSN: dsn + sep + "search_path=" + schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, err := reopened.GetSessionAuthority(ctx, "session-1")
	if err != nil || persisted == nil || *persisted != *authority {
		t.Fatalf("reopened authority: %+v, %v", persisted, err)
	}
}

func TestSessionAuthorityConcurrentOwners(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			project := "alpha"
			if i%2 == 1 {
				project = "beta"
			}
			results <- cs.RegisterSessionAuthority(ctx, "raced", project, "actor")
		}(i)
	}
	wg.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrSessionAuthorityConflict):
			conflict++
		default:
			t.Fatalf("unexpected registration error: %v", err)
		}
	}
	if success != 8 || conflict != 8 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestSessionAuthorityRejectsBlankInputs(t *testing.T) {
	cs := openIsolatedCloudStore(t)
	for _, args := range [][3]string{{" ", "alpha", "actor"}, {"session", " ", "actor"}, {"session", "alpha", " "}} {
		if err := cs.RegisterSessionAuthority(context.Background(), args[0], args[1], args[2]); err == nil {
			t.Fatalf("accepted blank registration: %q", args)
		}
	}
	if _, err := cs.GetSessionAuthority(context.Background(), " "); err == nil {
		t.Fatal("accepted blank lookup")
	}
}

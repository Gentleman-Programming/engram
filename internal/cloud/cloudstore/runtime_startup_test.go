package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"slices"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
)

func TestRuntimeStartupReceiptBoundaryPostgres(t *testing.T) {
	for _, test := range []struct {
		name     string
		verified bool
		proofErr error
		cancel   bool
	}{
		{name: "proved", verified: true},
		{name: "declined"},
		{name: "proof error cannot skip", verified: true, proofErr: errors.New("authority unavailable")},
		{name: "canceled proof cannot open runtime", verified: true, cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fixture := openIsolatedCloudStore(t)
			var schema string
			if err := fixture.db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.db.ExecContext(ctx, `DROP TABLE cloud_sync_audit_log;
				INSERT INTO cloud_chunks(project_name,chunk_id,created_by,payload)
				VALUES ('second','fixture','fixture','{"sessions":[{"id":"session"}]}')`); err != nil {
				t.Fatal(err)
			}
			wantCovered := test.verified && test.proofErr == nil && !test.cancel
			if wantCovered {
				if _, err := fixture.db.ExecContext(ctx, `INSERT INTO cloud_project_sessions(project_name,session_id)
					VALUES ('second','session')`); err != nil {
					t.Fatal(err)
				}
			}
			dsn, err := url.Parse(os.Getenv("CLOUDSTORE_TEST_DSN"))
			if err != nil {
				t.Fatal(err)
			}
			options := dsn.Query()
			options.Set("search_path", schema)
			dsn.RawQuery = options.Encode()
			projects := []string{"second", "first"}
			calls := 0
			verifier := &StartupReceiptVerifier{verify: func(ctx context.Context, db *sql.DB, scope []string) (bool, error) {
				calls++
				if !slices.Equal(scope, projects) {
					t.Fatalf("startup verifier scope = %v, want %v", scope, projects)
				}
				var migrated, indexed bool
				if err := db.QueryRowContext(ctx, `SELECT to_regclass('cloud_sync_audit_log') IS NOT NULL,
					EXISTS(SELECT FROM cloud_project_sessions WHERE project_name='second' AND session_id='session')`).Scan(&migrated, &indexed); err != nil {
					t.Fatal(err)
				}
				if !migrated || indexed != wantCovered {
					t.Fatalf("verifier must run after schema migration and before any index fallback: migrated=%t indexed=%t", migrated, indexed)
				}
				scope[0] = "mutated fixture scope"
				if test.cancel {
					cancel()
				}
				return test.verified, test.proofErr
			}}
			cs, covered, err := NewRuntimeContext(ctx, cloud.Config{DSN: dsn.String()}, projects, verifier)
			if !slices.Equal(projects, []string{"second", "first"}) {
				t.Fatalf("verifier modified fallback project order: %v", projects)
			}
			if test.cancel {
				if cs != nil || covered || !errors.Is(err, context.Canceled) || calls != 1 {
					t.Fatalf("canceled runtime startup = (%v,%t,%v), %d verifier calls", cs, covered, err, calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cs.Close() })
			if covered != wantCovered || calls != 1 {
				t.Fatalf("covered=%t calls=%d, want %t and 1", covered, calls, wantCovered)
			}
			known, err := cs.KnownSessionIDs(ctx, "second")
			if _, ok := known["session"]; err != nil || !ok {
				t.Fatalf("runtime returned without its exact session index: %v, %v", known, err)
			}
		})
	}
}

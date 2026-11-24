//go:build engram_lab_protected_receipt

package cloudstore

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/v3/internal/cloud"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed startup_receipt/schema.sql
var protectedFeedDDL string

var labUnsupportedReceiptSources = []struct {
	name, change, restore string
}{
	{"forced row security", `ALTER TABLE public.cloud_mutations ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY`, `ALTER TABLE public.cloud_mutations DISABLE ROW LEVEL SECURITY, NO FORCE ROW LEVEL SECURITY`},
	{"inheritance", `CREATE TABLE public.fixture_inherited () INHERITS (public.cloud_mutations)`, `DROP TABLE public.fixture_inherited`},
	// Changing persistence rewrites the relation, so even SET LOGGED would
	// leave this fixture ineligible for the subsequent verified append tests.
	{"unlogged source", `ALTER TABLE public.cloud_chunks SET UNLOGGED`, ""},
}

func sameLabProtectedReceipt(left, right protectedReceipt) bool {
	return left.snapshot == right.snapshot && left.identity == right.identity && left.witness == right.witness && slices.Equal(left.projects, right.projects)
}

func TestLabProtectedScopeReceipt(t *testing.T) {
	projects := []string{"gamma", "alpha", "beta", "alpha"}
	sorted := protectedProjects(projects)
	if !slices.Equal(sorted, []string{"alpha", "beta", "gamma"}) || projects[0] != "gamma" {
		t.Fatalf("canonical receipt changed fallback input: receipt %v, fallback %v", sorted, projects)
	}
	if len(protectedProjects([]string{"alpha", "*"})) != 0 || len(protectedProjects([]string{"alpha", " Gamma "})) != 0 {
		t.Fatal("wildcard or raw identities cannot authorize the shortcut")
	}
	dir := t.TempDir()
	root := openReceiptTestRoot(t, dir)
	const name = "receipt.json"
	valid := protectedReceipt{sorted, "800:800:", "fixture-enrollment", "fixture-witness"}
	if err := writeProtectedDiskReceipt(root, name, valid); err != nil {
		t.Fatal(err)
	}
	if got, err := readProtectedDiskReceipt(root, name); err != nil || !sameLabProtectedReceipt(got, valid) {
		t.Fatalf("sorted receipt round trip: %+v, %v", got, err)
	}
	for _, unsupported := range []string{
		`{"protected_receipt_version":1,"project":"alpha","snapshot":"800:800:","identity":"fixture-enrollment"}`,
		`{"protected_receipt_version":2,"projects":["gamma","alpha"],"snapshot":"800:800:","identity":"fixture-enrollment"}`,
		`{"protected_receipt_version":2,"projects":["alpha","alpha"],"snapshot":"800:800:","identity":"fixture-enrollment"}`,
		`{"protected_receipt_version":3,"projects":["alpha"],"snapshot":"800:800:","identity":"fixture-enrollment"}`,
		`{"protected_receipt_version":3,"projects":["gamma","alpha"],"snapshot":"800:800:","identity":"fixture-enrollment","witness":"fixture-witness"}`,
	} {
		if err := root.WriteFile(name, []byte(unsupported), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readProtectedDiskReceipt(root, name); !errors.Is(err, errReceiptAppendUnproven) {
			t.Fatalf("old or noncanonical receipt %q: %v", unsupported, err)
		}
	}
}

func openProtectedAppendFixture(t *testing.T, ctx context.Context) (*CloudStore, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("CLOUDSTORE_TEST_PROTECTED_DELTA_DSN")
	if dsn == "" {
		t.Skip("requires the owned PostgreSQL protected append runner")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgres" || u.Hostname() != "127.0.0.1" || u.User == nil || u.User.Username() != "fixture2397" {
		t.Fatal("expected an isolated loopback fixture")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	var directory, version string
	var initialized bool
	if err := admin.QueryRowContext(ctx, `SELECT current_setting('data_directory'), current_setting('server_version_num'),
		to_regclass('public.cloud_chunks') IS NOT NULL`).Scan(&directory, &version, &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized || !strings.HasPrefix(version, "18") ||
		filepath.Clean(directory) != filepath.Clean(os.Getenv("CLOUDSTORE_TEST_PROTECTED_DELTA_DIR")) ||
		!strings.HasPrefix(filepath.Base(filepath.Dir(directory)), "engram-2397-go-delta-") {
		t.Fatal("expected a fresh owned PostgreSQL 18 data directory")
	}
	if _, err := admin.ExecContext(ctx, `CREATE ROLE cloud_delta_owner LOGIN;
		CREATE ROLE cloud_delta_retention_operator LOGIN;
		GRANT CREATE, USAGE ON SCHEMA public TO cloud_delta_owner`); err != nil {
		t.Fatal(err)
	}
	u.User = url.User("cloud_delta_owner")
	cs, err := NewContext(ctx, cloud.Config{DSN: u.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	install := func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT
			set_config('engram.startup_receipt_runtime_role', 'cloud_delta_owner', true),
			set_config('engram.startup_receipt_retention_role', 'cloud_delta_retention_operator', true)`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, protectedFeedDDL)
		return err
	}
	for _, source := range labUnsupportedReceiptSources {
		t.Run("reject enrollment with "+source.name, func(t *testing.T) {
			tx, err := admin.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err := tx.ExecContext(ctx, source.change); err != nil {
				t.Fatal(err)
			}
			var pgErr *pgconn.PgError
			if err := install(tx); !errors.As(err, &pgErr) || pgErr.Message != "startup receipt requires ordinary permanent source tables without row security or inheritance" {
				t.Fatalf("expected source eligibility error, got %v", err)
			}
		})
	}
	t.Run("reject overlapping retention role", func(t *testing.T) {
		tx, err := admin.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, `GRANT cloud_delta_retention_operator TO cloud_delta_owner WITH INHERIT FALSE, SET TRUE`); err != nil {
			t.Fatal(err)
		}
		var pgErr *pgconn.PgError
		if err := install(tx); !errors.As(err, &pgErr) || pgErr.Message != "startup receipt needs separate existing Cloud runtime and retention roles" {
			t.Fatalf("expected overlapping role rejection, got %v", err)
		}
	})
	tx, err := admin.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := install(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, disallowed := range []string{
		`ALTER EVENT TRIGGER receipt_catalog_ddl DISABLE`,
		`SET event_triggers=false`,
		`DELETE FROM receipt_audit.events`,
		`SELECT receipt_audit.reset_log()`,
	} {
		_, err := cs.db.ExecContext(ctx, disallowed)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("ordinary source role must be denied %q, got %v", disallowed, err)
		}
	}
	return cs, admin
}

func TestLabProtectedAppendFeedPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cs, admin := openProtectedAppendFixture(t, ctx)
	t.Run("reject a different client encoding", func(t *testing.T) {
		cs.db.SetMaxOpenConns(1)
		t.Cleanup(func() {
			if _, err := cs.db.ExecContext(ctx, `RESET client_encoding`); err != nil {
				t.Error(err)
			}
			cs.db.SetMaxOpenConns(0)
		})
		if _, err := cs.db.ExecContext(ctx, `SET client_encoding TO LATIN1`); err != nil {
			t.Fatal(err)
		}
		if _, err := captureProtectedReceipt(ctx, cs.db, []string{"alpha"}); err == nil {
			t.Fatal("receipt prepared with an unaccounted client encoding")
		}
	})
	t.Run("reject shadowed project equality", func(t *testing.T) {
		// Keep the custom search path on the same isolated database connection.
		cs.db.SetMaxOpenConns(1)
		t.Cleanup(func() {
			if _, err := cs.db.ExecContext(ctx, `RESET search_path; DROP OPERATOR public.= (text,text); DROP FUNCTION public.fixture_equal(text,text)`); err != nil {
				t.Error(err)
			}
			cs.db.SetMaxOpenConns(0)
		})
		if _, err := cs.db.ExecContext(ctx, `
			CREATE FUNCTION public.fixture_equal(text,text) RETURNS boolean LANGUAGE sql IMMUTABLE
			AS 'SELECT pg_catalog.lower($1) OPERATOR(pg_catalog.=) pg_catalog.lower($2)';
			CREATE OPERATOR public.= (FUNCTION=public.fixture_equal, LEFTARG=text, RIGHTARG=text);
			SET search_path TO public, pg_catalog`); err != nil {
			t.Fatal(err)
		}
		var aliasesEqual bool
		if err := cs.db.QueryRowContext(ctx, `SELECT 'alpha'::text = 'ALPHA'::text`).Scan(&aliasesEqual); err != nil || !aliasesEqual {
			t.Fatalf("expected ordinary project equality to use the custom operator: %v (%v)", aliasesEqual, err)
		}
		if _, err := captureProtectedReceipt(ctx, cs.db, []string{"alpha"}); err == nil {
			t.Fatal("receipt prepared with different project equality")
		}
	})
	baseline := func(t *testing.T, project string) protectedReceipt {
		t.Helper()
		receipt, err := captureProtectedReceipt(ctx, cs.db, []string{project})
		if err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	for _, source := range labUnsupportedReceiptSources {
		if source.restore == "" {
			continue
		}
		t.Run("reject reuse with "+source.name, func(t *testing.T) {
			prior := baseline(t, "source-eligibility")
			if _, err := cs.db.ExecContext(ctx, source.change); err != nil {
				t.Fatal(err)
			}
			if _, err := captureProtectedReceipt(ctx, cs.db, prior.projects); !errors.Is(err, errReceiptAppendUnproven) {
				t.Errorf("receipt captured with unsupported source: %v", err)
			}
			if next, _, err := tryProtectedDelta(ctx, cs.db, prior); !errors.Is(err, errReceiptAppendUnproven) || !sameLabProtectedReceipt(prior, next) {
				t.Errorf("receipt advanced with unsupported source: %v", err)
			}
			if _, err := cs.db.ExecContext(ctx, source.restore); err != nil {
				t.Fatal(err)
			}
			if next, _, err := tryProtectedDelta(ctx, cs.db, prior); !errors.Is(err, errReceiptAppendUnproven) || !sameLabProtectedReceipt(prior, next) {
				t.Errorf("receipt advanced after source shape was restored: %v", err)
			}
		})
	}
	appendBatch := func(project, entity, id string, payload []byte) int64 {
		t.Helper()
		seq, err := cs.InsertMutationBatch(ctx, []MutationEntry{{Project: project, Entity: entity, EntityKey: id, Op: store.SyncOpUpsert, Payload: payload}})
		if err != nil || len(seq) != 1 {
			t.Fatalf("real writer: sequences=%v, %v", seq, err)
		}
		return seq[0]
	}
	const insertUncovered = `INSERT INTO cloud_mutations(project, entity, entity_key, op, payload) VALUES ($1,'observation',$2,'upsert',$3) RETURNING seq`
	t.Run("replica mode records a destructive event", func(t *testing.T) {
		const project = "replica-audit"
		prior := baseline(t, project)
		tx, err := admin.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, `SET LOCAL session_replication_role TO replica`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, insertUncovered, project, "replica-audit-row", receiptAppendObservation("replica-audit-row", "unmaterialized")); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		next, events, err := tryProtectedDelta(ctx, cs.db, prior)
		if !errors.Is(err, errReceiptAppendUnproven) || !sameLabProtectedReceipt(prior, next) ||
			!slices.ContainsFunc(events, func(e receiptAppendEvent) bool { return e.kind == "destructive" }) {
			t.Fatalf("replica write must mark previous proof unproven: %+v (%v)", events, err)
		}
		report, err := cs.BackfillMutationChunks(ctx, project, true)
		if err != nil || report.ChunksInserted != 1 {
			t.Fatalf("ordinary repair must insert the missing chunk: %+v (%v)", report, err)
		}
	})
	insertMissing := func(db *sql.DB, project, id string) int64 {
		t.Helper()
		var seq int64
		if err := db.QueryRowContext(ctx, insertUncovered, project, id, receiptAppendObservation(id, "unmaterialized")).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		return seq
	}
	assertLegacyFallback := func(prior protectedReceipt, kind string, seq int64) {
		t.Helper()
		advanced, events, err := tryProtectedDelta(ctx, cs.db, prior)
		if !errors.Is(err, errReceiptAppendUnproven) || !sameLabProtectedReceipt(prior, advanced) || len(events) != 1 || events[0].kind != kind || events[0].seq != seq {
			t.Fatalf("%s fallback %q changed receipt or events: %+v (%v)", kind, prior.projects[0], events, err)
		}
		report, err := cs.BackfillMutationChunks(ctx, prior.projects[0], true)
		if err != nil || report.ChunksPlanned != 1 || report.ChunksInserted != 1 {
			t.Fatalf("ordinary fallback for %q failed: %+v, %v", prior.projects[0], report, err)
		}
		repaired := baseline(t, prior.projects[0])
		if _, events, err := tryProtectedDelta(ctx, cs.db, repaired); err != nil || len(events) != 0 {
			t.Fatalf("post-repair receipt must verify with zero events: %+v (%v)", events, err)
		}
		t.Logf("%s event %s -> legacy planned and inserted one chunk -> zero-event recovery", prior.projects[0], kind)
	}

	healthy := baseline(t, "healthy")
	// Rerun the actual schema initialization to check its idempotent DDL and
	// zero-row updates leave the protected warm receipt usable.
	if err := cs.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, events, err := tryProtectedDelta(ctx, cs.db, healthy); err != nil || len(events) != 0 {
		t.Fatalf("idempotent initialization invalidated receipt: %+v (%v)", events, err)
	}
	const opaque = " session\u00a0 "
	appendBatch("healthy", store.SyncEntitySession, opaque, []byte(`{"id":" session\u00a0 ","directory":"/fixture","project":"old"}`))
	advanced, events, err := tryProtectedDelta(ctx, cs.db, healthy)
	if err != nil || sameLabProtectedReceipt(healthy, advanced) || len(events) != 3 {
		t.Fatalf("healthy real PG event append must advance: %+v (%v)", events, err)
	}
	if next, events, err := tryProtectedDelta(ctx, cs.db, advanced); err != nil || len(events) != 0 || !sameLabProtectedReceipt(next, advanced) {
		t.Fatalf("advanced receipt must accept zero events: %+v (%v)", events, err)
	}
	testLabProtectedWitness(t, ctx, cs.db, healthy, advanced)
	t.Log("healthy append accepted three actual events, then zero after receipt advance")

	missing := baseline(t, "missing")
	assertLegacyFallback(missing, "journal", insertMissing(cs.db, "missing", "missing-observation"))

	// Keep the lower journal INSERT uncommitted until a later normal writer is
	// committed and observed in a baseline. No sequence-maximum check sees it.
	lower, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lower.Rollback() }()
	var lowSeq, maxSeq int64
	if err := lower.QueryRowContext(ctx, insertUncovered, "late", "lower-observation", receiptAppendObservation("lower-observation", "late")).Scan(&lowSeq); err != nil {
		t.Fatal(err)
	}
	highSeq := appendBatch("late", store.SyncEntityObservation, "higher-observation", receiptAppendObservation("higher-observation", "visible"))
	late := baseline(t, "late")
	if _, events, err := tryProtectedDelta(ctx, cs.db, late); err != nil || len(events) != 0 {
		t.Fatalf("current still-invisible pending transaction: events=%+v (%v)", events, err)
	}
	if err := lower.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := cs.db.QueryRowContext(ctx, `SELECT max(seq) FROM cloud_mutations WHERE project='late'`).Scan(&maxSeq); err != nil || lowSeq >= highSeq || maxSeq != highSeq {
		t.Fatalf("late sequence boundary: low=%d high=%d max=%d, %v", lowSeq, highSeq, maxSeq, err)
	}
	var lowXID, highXID string
	var lowVisible, highVisible bool
	if err := cs.db.QueryRowContext(ctx, `SELECT low.xid::text, high.xid::text,
			pg_visible_in_snapshot(low.xid,$3::pg_snapshot), pg_visible_in_snapshot(high.xid,$3::pg_snapshot)
		FROM receipt_audit.events low CROSS JOIN receipt_audit.events high
		WHERE low.kind='journal' AND low.seq=$1 AND high.kind='journal' AND high.seq=$2`, lowSeq, highSeq, late.snapshot).
		Scan(&lowXID, &highXID, &lowVisible, &highVisible); err != nil || lowVisible || !highVisible {
		t.Fatalf("late transaction visibility: low=%v high=%v, %v", lowVisible, highVisible, err)
	}
	t.Logf("snapshot %s received sequence %d (XID %s) after visible maximum %d (XID %s)", late.snapshot, lowSeq, lowXID, highSeq, highXID)
	assertLegacyFallback(late, "journal", lowSeq)

	appendBatch("ddl", store.SyncEntityObservation, "legacy-raw", receiptAppendObservation("legacy-raw", "original"))
	drift := baseline(t, "ddl")
	if _, err := cs.db.ExecContext(ctx, `BEGIN;
		ALTER TABLE cloud_mutations DISABLE TRIGGER receipt_update;
		UPDATE cloud_mutations SET payload = jsonb_set(payload,'{content}','"changed during DDL"') WHERE project='ddl';
		ALTER TABLE cloud_mutations ENABLE ALWAYS TRIGGER receipt_update;
		COMMIT;`); err != nil {
		t.Fatal(err)
	}
	// Catalog equality has already been rechecked when we observe a concrete
	// catalog-dirty event (the row UPDATE trigger did not fire).
	assertLegacyFallback(drift, "catalog-dirty", 0)

	capReceipt := baseline(t, "overflow")
	if _, err := cs.db.ExecContext(ctx, `INSERT INTO cloud_mutations(project,entity,entity_key,op,payload)
		SELECT 'overflow','ignored',i::text,'upsert','{}'::jsonb FROM generate_series(1,$1) i`, receiptAppendEventCap+5); err != nil {
		t.Fatal(err)
	}
	if next, events, err := tryProtectedDelta(ctx, cs.db, capReceipt); !sameLabProtectedReceipt(next, capReceipt) || !errors.Is(err, errReceiptAppendUnproven) || len(events) != receiptAppendEventCap+1 {
		t.Fatalf("select at most cap+1 before fallback: events=%d (%v)", len(events), err)
	}
	future := baseline(t, "currently-invisible")
	active, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = active.Rollback() }()
	var activeSeq int64
	if err := active.QueryRowContext(ctx, insertUncovered, future.projects[0], "uncommitted", receiptAppendObservation("uncommitted", "pending")).Scan(&activeSeq); err != nil {
		t.Fatal(err)
	}
	if _, events, err := tryProtectedDelta(ctx, cs.db, future); err != nil || len(events) != 0 {
		t.Fatalf("xmax-tail lookup must exclude currently invisible uncommitted events: %+v (%v)", events, err)
	}
	if err := active.Rollback(); err != nil {
		t.Fatal(err)
	}

	retained := baseline(t, "retained-query")
	prunedSeq := insertMissing(cs.db, "retained-query", "pruned-observation")
	testLabConcurrentRetentionQuery(t, ctx, cs, retained, prunedSeq)

	outside := baseline(t, "healthy")
	if _, err := cs.db.ExecContext(ctx, `INSERT INTO cloud_chunks(project_name,chunk_id,created_by,payload)
		VALUES ('outside-repair','new','fixture','{"sessions":[{"id":" opaque outside "}]}')`); err != nil {
		t.Fatal(err)
	}
	advanced, events, err = tryProtectedDelta(ctx, cs.db, outside)
	if !errors.Is(err, errReceiptAppendUnproven) || !sameLabProtectedReceipt(outside, advanced) || len(events) != 1 || events[0].kind != "cloud_chunks" {
		t.Fatalf("global index must reject an unindexed append outside repair scope: %+v (%v)", events, err)
	}
	if err := cs.backfillProjectSessionsFromChunks(ctx); !errors.Is(err, ErrSessionIndexHistoricalResolutionRequired) {
		t.Fatalf("ordinary session-index guard must still run: %v", err)
	}
	t.Log("out-of-scope chunk event rejected, and ordinary exact opaque-index guard applies")
}

func testLabProtectedWitness(t *testing.T, ctx context.Context, db *sql.DB, old, current protectedReceipt) {
	t.Helper()
	for _, command := range []string{
		`DELETE FROM receipt_audit.witnesses WHERE false`,
		`UPDATE receipt_audit.witnesses SET snapshot='1:1:' WHERE false`,
		`INSERT INTO receipt_audit.witnesses SELECT * FROM receipt_audit.witnesses WHERE false`,
	} {
		_, err := db.ExecContext(ctx, command)
		var denied *pgconn.PgError
		if !errors.As(err, &denied) || denied.Code != "42501" {
			t.Fatalf("runtime could bypass the witness function: %q: %v", command, err)
		}
	}
	tx, err := startupSnapshot(ctx, db, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var token string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_audit.save_witness($1)::text`, current.scopeKey()).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		witness string
		scope   []string
	}{
		{"rolled-back witness", token, current.projects},
		{"witness from another snapshot", old.witness, current.projects},
		{"missing witness", "00000000-0000-0000-0000-000000000000", current.projects},
		{"witness from another project scope", current.witness, []string{"another-project"}},
	} {
		invalid := current
		invalid.witness = test.witness
		invalid.projects = test.scope
		next, events, err := tryProtectedDelta(ctx, db, invalid)
		if !errors.Is(err, errReceiptAppendUnproven) || !sameLabProtectedReceipt(next, invalid) || len(events) != 0 {
			t.Fatalf("%s admitted a receipt: events=%+v (%v)", test.name, events, err)
		}
	}
}

func testLabConcurrentRetentionQuery(t *testing.T, ctx context.Context, cs *CloudStore, prior protectedReceipt, seq int64) {
	t.Helper()
	dsn, err := url.Parse(os.Getenv("CLOUDSTORE_TEST_PROTECTED_DELTA_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	dsn.User = url.User("cloud_delta_retention_operator")
	operator, err := sql.Open("pgx", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = operator.Close() }()
	for _, command := range []string{
		`DELETE FROM receipt_audit.events`,
		`TRUNCATE receipt_audit.events`,
		`UPDATE receipt_audit.enrollment SET retained_from='0'`,
		`DELETE FROM receipt_audit.witnesses WHERE false`,
		`SELECT receipt_audit.save_witness(repeat('0',64))`,
	} {
		_, err := operator.ExecContext(ctx, command)
		var denied *pgconn.PgError
		if !errors.As(err, &denied) || denied.Code != "42501" {
			t.Fatalf("maintenance operator bypassed guarded API: %q: %v", command, err)
		}
	}
	if _, err := operator.ExecContext(ctx, `SELECT receipt_audit.prune_prefix('18446744073709551615')`); err == nil {
		t.Fatal("maintenance operator could prune beyond current xmin")
	}
	tx, err := startupSnapshot(ctx, cs.db, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, identity, err := protectedCatalogSnapshot(ctx, tx, prior.snapshot); err != nil || identity != prior.identity {
		t.Fatalf("in-flight proof must start with valid old floor: %v", err)
	}
	var removed int64
	if err := operator.QueryRowContext(ctx, `SELECT receipt_audit.prune_prefix(pg_snapshot_xmin(pg_current_snapshot()))`).Scan(&removed); err != nil || removed == 0 {
		t.Fatalf("prune must progress while older proof is in flight: removed=%d, %v", removed, err)
	}
	if _, err := operator.ExecContext(ctx, `SELECT receipt_audit.prune_prefix('0')`); err == nil {
		t.Fatal("maintenance operator could lower the protected floor")
	}
	events, err := readProtectedAppendEvents(ctx, tx, prior.snapshot)
	if err != nil || len(events) != 1 || events[0].seq != seq {
		t.Fatalf("old transaction must see the pruned row through MVCC: %+v (%v)", events, err)
	}
	if err := verifyAppendMembership(ctx, tx, prior.projects, events); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("old transaction must reject the incomplete append: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if next, events, err := tryProtectedDelta(ctx, cs.db, prior); !sameLabProtectedReceipt(next, prior) || len(events) != 0 || !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("fresh transaction must refuse stale floor before loading events: %+v (%v)", events, err)
	}
	report, err := cs.BackfillMutationChunks(ctx, prior.projects[0], true)
	if err != nil || report.ChunksInserted != 1 {
		t.Fatalf("ordinary repair after stale floor: %+v (%v)", report, err)
	}
	t.Logf("prefix retention removed %d events; in-flight proof saw its old event, fresh proof refused stale floor, legacy repaired one", removed)
}

func receiptAppendObservation(id, content string) []byte {
	payload, _ := json.Marshal(map[string]string{
		"sync_id": id, "session_id": "observation-session", "type": "decision", "title": "Fixture", "content": content, "scope": "project",
	})
	return payload
}

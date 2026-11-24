//go:build engram_lab_protected_receipt

package cloudstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestLabAppendMembershipPostgres(t *testing.T) {
	ctx := context.Background()
	cs := openIsolatedCloudStore(t)
	const project = "configured"
	const opaqueID = " session\u00a0 "
	batch := []MutationEntry{{
		Project: project, Entity: store.SyncEntitySession, EntityKey: opaqueID, Op: store.SyncOpUpsert,
		Payload: []byte(`{"id":" session\u00a0 ","directory":"/fixture","project":"old"}`),
	}}
	chunk, err := materializedMutationBatchChunks(batch)
	if err != nil || len(chunk) != 1 {
		t.Fatalf("materialize fixture: %v", err)
	}
	seq, err := cs.InsertMutationBatch(ctx, batch)
	if err != nil || len(seq) != 1 {
		t.Fatalf("insert materialized fixture: %v", err)
	}
	valid := []receiptAppendEvent{
		{kind: "journal", project: project, seq: seq[0]},
		{kind: "cloud_chunks", project: project, id: chunk[0].id},
		{kind: "cloud_project_sessions", project: project, id: opaqueID},
	}
	verify := func(t *testing.T, events []receiptAppendEvent, alsoAllowed ...string) error {
		t.Helper()
		tx, err := cs.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, `LOCK TABLE cloud_chunks, cloud_mutations, cloud_project_sessions IN ACCESS SHARE MODE`); err != nil {
			t.Fatal(err)
		}
		if err := verifyAppendMembership(ctx, tx, append([]string{project}, alsoAllowed...), events); err != nil {
			return err
		}
		return tx.Commit()
	}
	exec := func(t *testing.T, statement string, args ...any) {
		t.Helper()
		if _, err := cs.db.ExecContext(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := verify(t, valid); err != nil {
		t.Fatalf("real writer append must pass: %v", err)
	}
	if err := verify(t, nil); err != nil {
		t.Fatalf("empty delta must pass: %v", err)
	}
	if err := verify(t, valid[:1]); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("journal without a new covering chunk must fall back: %v", err)
	}
	if err := verify(t, append(append([]receiptAppendEvent{}, valid...), valid[0])); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("duplicate event must fall back: %v", err)
	}
	if err := verify(t, []receiptAppendEvent{{kind: "catalog-dirty"}}); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("catalog event must fall back: %v", err)
	}
	if err := verify(t, make([]receiptAppendEvent, receiptAppendEventCap+1)); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("over-cap events must fall back: %v", err)
	}

	// Reusing the chunk ID cannot cover different published bytes. The session
	// membership remains healthy, so only the exact mutation signature decides.
	exec(t, `UPDATE cloud_chunks SET payload = jsonb_set(payload, '{mutations,0,payload}', to_jsonb($1::text)) WHERE project_name=$2`, `{"id":" session\u00a0 ","directory":"/other","project":"configured"}`, project)
	if err := verify(t, valid); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("same-ID replaced payload must fall back: %v", err)
	}
	exec(t, `INSERT INTO cloud_chunks (project_name,chunk_id,created_by,payload) VALUES ('other-configured',$1,'fixture',$2)`, chunk[0].id, chunk[0].payload)
	exec(t, `INSERT INTO cloud_project_sessions (project_name,session_id) VALUES ('other-configured',$1)`, opaqueID)
	otherProject := []receiptAppendEvent{valid[0], {kind: "cloud_chunks", project: "other-configured", id: chunk[0].id}}
	if err := verify(t, otherProject, "other-configured"); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("embedded signature in a different physical project's chunk must fall back: %v", err)
	}

	// All appended chunks affect startup session indexing, even outside the
	// configured repair projects. Trimmed keys cannot stand in for opaque IDs.
	exec(t, `INSERT INTO cloud_chunks (project_name,chunk_id,created_by,payload) VALUES ('other','new','fixture','{"sessions":[{"id":" padded "}]}')`)
	exec(t, `INSERT INTO cloud_project_sessions (project_name,session_id) VALUES ('other','padded')`)
	outside := []receiptAppendEvent{{kind: "cloud_chunks", project: "other", id: "new"}}
	if err := verify(t, outside); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("missing exact global session index key must fall back: %v", err)
	}
	exec(t, `INSERT INTO cloud_project_sessions (project_name,session_id) VALUES ('other',' padded ')`)
	if err := verify(t, outside); err != nil {
		t.Fatalf("global scope checks exact existing opaque session key: %v", err)
	}

	exec(t, `INSERT INTO cloud_chunks (project_name,chunk_id,created_by,payload) VALUES ('other','bad','fixture','{"sessions":[{"id":"s","ownership_mode":"project_owned","directory":null}]}')`)
	if err := verify(t, []receiptAppendEvent{{kind: "cloud_chunks", project: "other", id: "bad"}}); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("guarded decoder must reject malformed project-owned chunk: %v", err)
	}
	if err := verify(t, []receiptAppendEvent{{kind: "cloud_chunks", project: "other", id: "absent"}}); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("missing exact event row must fall back: %v", err)
	}
	exec(t, `INSERT INTO cloud_chunks (project_name,chunk_id,created_by,payload)
		VALUES ('other','oversize','fixture',jsonb_build_object('ignored',repeat('x',$1)))`, receiptAppendRowBytes)
	if err := verify(t, []receiptAppendEvent{{kind: "cloud_chunks", project: "other", id: "oversize"}}); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("over-cap raw JSON bytes must fall back: %v", err)
	}

	exec(t, `INSERT INTO cloud_chunks (project_name,chunk_id,created_by,payload)
		SELECT 'other','bounded-' || i,'fixture',jsonb_build_object('ignored',repeat('x',3500000))
		FROM generate_series(1,5) AS i`)
	var bounded []receiptAppendEvent
	for _, id := range []string{"bounded-1", "bounded-2", "bounded-3", "bounded-4", "bounded-5"} {
		bounded = append(bounded, receiptAppendEvent{kind: "cloud_chunks", project: "other", id: id})
	}
	if err := verify(t, bounded[:4]); err != nil {
		t.Fatalf("under the shared payload budget: %v", err)
	}
	if err := verify(t, bounded); !errors.Is(err, errReceiptAppendUnproven) {
		t.Fatalf("over the shared payload budget must fall back: %v", err)
	}

	// The legacy project predicate is SQL equality. A nondeterministic column
	// collation could match another project that the opaque Go map would omit.
	t.Run("nondeterministic-project-scope", func(t *testing.T) {
		var icu bool
		if err := cs.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT FROM pg_catalog.pg_collation WHERE collprovider='i')`).Scan(&icu); err != nil {
			t.Fatal(err)
		}
		if !icu {
			t.Skip("fixture PostgreSQL has no ICU collation provider")
		}
		exec(t, `CREATE COLLATION casefold_fixture (provider=icu, locale='und-u-ks-level2', deterministic=false);
			ALTER TABLE cloud_mutations ALTER COLUMN project TYPE text COLLATE casefold_fixture`)
		if err := verify(t, outside); !errors.Is(err, errReceiptAppendUnproven) {
			t.Fatalf("inexact SQL project equality must fall back: %v", err)
		}
	})
	t.Run("case-insensitive-project-type", func(t *testing.T) {
		owned := os.Getenv("CLOUDSTORE_TEST_PROTECTED_DELTA_DIR")
		if owned == "" {
			t.Skip("requires the owned PostgreSQL protected append runner")
		}
		var directory string
		if err := cs.db.QueryRowContext(ctx, `SHOW data_directory`).Scan(&directory); err != nil || filepath.Clean(directory) != filepath.Clean(owned) {
			t.Fatalf("expected the owned PostgreSQL data directory, got %q (%v)", directory, err)
		}
		// Use fresh connections after changing the column type, as on restart.
		cs.db.SetMaxIdleConns(0)
		exec(t, `CREATE EXTENSION citext;
			ALTER TABLE cloud_mutations ALTER COLUMN project TYPE citext COLLATE "C"`)
		var seq int64
		if err := cs.db.QueryRowContext(ctx, `
			INSERT INTO cloud_mutations(project, entity, entity_key, op, payload)
			VALUES ('ALIAS-PROJECT', 'observation', 'new-alias', 'upsert', $1) RETURNING seq`,
			receiptAppendObservation("new-alias", "unmaterialized")).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		report, err := cs.BackfillMutationChunks(ctx, "alias-project", false)
		if err != nil || report.ChunksPlanned != 1 {
			t.Fatalf("ordinary repair must find the uppercase project: %+v (%v)", report, err)
		}
		if err := verify(t, []receiptAppendEvent{{kind: "journal", project: "ALIAS-PROJECT", seq: seq}}, "alias-project"); !errors.Is(err, errReceiptAppendUnproven) {
			t.Fatalf("case-insensitive project type must fall back: %v", err)
		}
	})
}

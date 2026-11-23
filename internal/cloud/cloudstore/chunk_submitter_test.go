package cloudstore

import (
	"database/sql"
	"testing"
)

func TestMigratePreservesLegacyChunkCreditWithoutSubmitter(t *testing.T) {
	s := openIsolatedCloudStore(t)
	ctx := t.Context()
	if _, err := s.db.ExecContext(ctx, `
		ALTER TABLE cloud_chunks DROP COLUMN submitted_by_principal_id;
		INSERT INTO cloud_chunks (project_name,chunk_id,created_by,payload)
		VALUES ('history','historic-chunk','credited author','{}');
	`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var credited string
	var submitted sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT created_by,submitted_by_principal_id
		FROM cloud_chunks WHERE project_name='history' AND chunk_id='historic-chunk'`).Scan(&credited, &submitted); err != nil {
		t.Fatal(err)
	}
	if credited != "credited author" || submitted.Valid {
		t.Fatal("migration changed historical attribution")
	}
}

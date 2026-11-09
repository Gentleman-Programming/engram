package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestChunkDeleteEvidencePersistenceAndIsolation(t *testing.T) {
	s := newTestStore(t)
	evidence := ChunkDeleteEvidence{Scope: "https://synthetic.invalid/audit", ManifestVersion: 2, DeletedIDs: []string{"deleted"}}
	if err := s.ApplyPulledChunkWithDeleteEvidence("cloud:audit", "chunk", nil, evidence); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSyncedChunkForTarget("cloud:audit", "empty"); err != nil {
		t.Fatal(err)
	}
	empty := evidence
	empty.DeletedIDs = nil
	if err := s.RecordChunkDeleteEvidence("cloud:audit", "empty", empty); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordChunkDeleteEvidence("cloud:audit", "unknown", empty); err == nil {
		t.Fatal("recorded evidence for an untracked chunk")
	}
	cfg := s.cfg
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	for _, tc := range []struct {
		target, scope string
		version       int
		want          int
	}{
		{"cloud:audit", evidence.Scope, 2, 2},
		{"cloud:other", evidence.Scope, 2, 0},
		{"cloud:audit", "https://other.invalid/audit", 2, 0},
		{"cloud:audit", evidence.Scope, 1, 0},
	} {
		got, err := reopened.GetChunkDeleteEvidence(tc.target, tc.scope, tc.version)
		if err != nil || len(got) != tc.want {
			t.Fatalf("%+v: summaries=%v err=%v", tc, got, err)
		}
		if tc.want > 0 && (!reflect.DeepEqual(got["chunk"], []string{"deleted"}) || got["empty"] == nil) {
			t.Fatalf("lost complete empty/nonempty summaries: %v", got)
		}
	}
}

func TestChunkDeleteEvidencePreIndexCopy(t *testing.T) {
	for _, nearMiss := range []bool{false, true} {
		t.Run(fmt.Sprint(nearMiss), func(t *testing.T) {
			source := copyFixture(t)
			db, err := sql.Open("sqlite", filepath.Join(source, "engram.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP TABLE sync_chunk_delete_evidence`); err != nil {
				t.Fatal(err)
			}
			if nearMiss {
				if _, err := db.Exec(`CREATE TABLE unexpected_object(id TEXT)`); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := copySnapshot(t, source)
			_, err = MigrateCopy(source, filepath.Join(copyTempDir(t), "destination"), true)
			if (err != nil) != nearMiss {
				t.Fatalf("pre-index copy nearMiss=%t: %v", nearMiss, err)
			}
			if !reflect.DeepEqual(before, copySnapshot(t, source)) {
				t.Fatal("copy mutated original pre-index store")
			}
		})
	}
}

func TestChunkDeleteEvidenceAtomicRollback(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_evidence BEFORE INSERT ON sync_chunk_delete_evidence
		BEGIN SELECT RAISE(ABORT, 'synthetic evidence failure'); END`); err != nil {
		t.Fatal(err)
	}
	evidence := ChunkDeleteEvidence{Scope: "synthetic", ManifestVersion: 2}
	mutations := []SyncMutation{{Entity: SyncEntitySession, EntityKey: "atomic-session", Op: SyncOpUpsert,
		Payload: `{"id":"atomic-session","project":"audit","directory":"/synthetic/audit"}`}}
	if err := s.ApplyPulledChunkWithDeleteEvidence("cloud:audit", "chunk", mutations, evidence); err == nil {
		t.Fatal("evidence failure did not abort import")
	}
	known, err := s.GetSyncedChunksForTarget("cloud:audit")
	if err != nil || known["chunk"] {
		t.Fatalf("tracking survived rollback: %v %v", known, err)
	}
	if _, err := s.GetSession("atomic-session"); err == nil {
		t.Fatal("session survived evidence rollback")
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_evidence`); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPulledChunkWithDeleteEvidence("cloud:audit", "chunk", mutations, evidence); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSession("atomic-session"); err != nil {
		t.Fatal(err)
	}
}

func TestChunkDeleteEvidenceDamagedRowIsMiss(t *testing.T) {
	s := newTestStore(t)
	evidence := ChunkDeleteEvidence{Scope: "synthetic", ManifestVersion: 2}
	if err := s.ApplyPulledChunkWithDeleteEvidence("cloud:audit", "chunk", nil, evidence); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"not-json", "null", `{"not":"array"}`} {
		if _, err := s.db.Exec(`UPDATE sync_chunk_delete_evidence SET deleted_ids = ?`, raw); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetChunkDeleteEvidence("cloud:audit", "synthetic", 2)
		if err != nil || len(got) != 0 {
			t.Fatalf("damaged evidence became authoritative: %v %v", got, err)
		}
	}
	if err := s.RecordChunkDeleteEvidence("cloud:audit", "chunk", evidence); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetChunkDeleteEvidence("cloud:audit", "synthetic", 2)
	if err != nil || got["chunk"] == nil {
		t.Fatalf("backfill did not repair damaged evidence: %v %v", got, err)
	}
	if _, err := s.db.Exec(`DELETE FROM sync_chunks WHERE target_key = 'cloud:audit'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM sync_chunk_delete_evidence`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan evidence count=%d err=%v", count, err)
	}
}

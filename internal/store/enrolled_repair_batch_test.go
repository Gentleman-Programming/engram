package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func batchRepairFixture(t *testing.T) (*Store, map[string]string) {
	t.Helper()
	s := newTestStoreRaw(t)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.DB().Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sync_enrolled_projects(project) VALUES ('batch-project')`)
	for i := 0; i < 65; i++ {
		key := fmt.Sprintf("%03d", i)
		// Keep opening a second Store from classifying the fixture's ownership.
		exec(`INSERT INTO sessions(id,project,directory,ownership_mode) VALUES (?, 'batch-project','/synthetic','shared')`, "s"+key)
		for _, prefix := range []string{"live", "deleted"} {
			exec(`INSERT INTO observations(session_id,type,title,content,project,scope,normalized_hash,sync_id,deleted_at)
				VALUES ('s000','decision',?,'synthetic','batch-project','project',?,?,?)`,
				prefix+key, prefix+key, prefix+key, map[string]any{"live": nil, "deleted": "2026-01-01 00:00:00"}[prefix])
		}
		exec(`INSERT INTO user_prompts(sync_id,session_id,content,project) VALUES (?,'s000','synthetic','batch-project')`, "p"+key)
		exec(`INSERT INTO prompt_tombstones(sync_id,session_id,project) VALUES (?,'s000','batch-project')`, "pd"+key)
		for _, entity := range []string{SyncEntityObservation, SyncEntitySession} {
			exec(`INSERT INTO sync_delete_tombstones(entity,entity_key,session_id,project) VALUES (?,?,'s000','batch-project')`, entity, entity+key)
		}
		exec(`INSERT INTO memory_relations(sync_id,source_id,target_id,relation,judgment_status,marked_by_actor,marked_by_kind)
			VALUES (?,'live000','live001','related','judged','synthetic','manual')`, "r"+key)
	}
	// Capture the existing atomic consumer's public output as a payload oracle,
	// then restore missing-journal state. No production database is touched.
	if err := s.withTx(func(tx *sql.Tx) error { return s.backfillProjectSyncMutationsTx(tx, "batch-project") }); err != nil {
		t.Fatal(err)
	}
	want := batchRepairJournal(t, s)
	if len(want) != 520 {
		t.Fatalf("fixture produced %d mutations, want 520", len(want))
	}
	exec(`DELETE FROM sync_mutations`)
	return s, want
}

func batchRepairJournal(t *testing.T, s *Store) map[string]string {
	t.Helper()
	rows, err := s.ListPendingProjectMutations("batch-project")
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, row := range rows {
		key := row.Entity + "/" + row.EntityKey + "/" + row.Op + "/" + row.Source
		if _, exists := result[key]; exists {
			t.Fatalf("duplicate mutation %s", key)
		}
		result[key] = row.Payload
	}
	return result
}

func TestEnrolledRepairBatches(t *testing.T) {
	for _, mode := range []string{"rollback", "retry", "cancel", "writer", "atomic consumer"} {
		t.Run(mode, func(t *testing.T) {
			s, want := batchRepairFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			commits, writes, maxWrites := 0, 0, 0
			injected := errors.New("synthetic batch failure")
			writerProgress := false
			var writer *Store
			if mode == "writer" {
				var err error
				writer, err = New(s.cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := writer.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
				if strings.Contains(query, "INSERT INTO sync_mutations") {
					writes++
					if (mode == "rollback" || mode == "atomic consumer") && writes == 2 {
						return nil, injected
					}
				}
				return db.Exec(query, args...)
			}
			s.hooks.commit = func(tx *sql.Tx) error {
				if mode == "retry" && commits == 1 {
					return injected
				}
				if err := tx.Commit(); err != nil {
					return err
				}
				commits++
				if writes > maxWrites {
					maxWrites = writes
				}
				writes = 0
				if commits == 1 && mode == "cancel" {
					cancel()
				}
				if commits == 1 && mode == "writer" {
					// The real commit has released SQLite before this observation.
					writerProgress = len(batchRepairJournal(t, writer)) < len(want)
					return writer.CreateSession("independent-writer", "other-project", "/synthetic")
				}
				return nil
			}
			var err error
			if mode == "atomic consumer" {
				err = s.withTx(func(tx *sql.Tx) error { return s.backfillProjectSyncMutationsTx(tx, "batch-project") })
			} else {
				err = s.EnsureEnrolledProjectSyncMutations(ctx)
			}
			got := batchRepairJournal(t, s)
			switch mode {
			case "rollback", "atomic consumer":
				if !errors.Is(err, injected) || len(got) != 0 {
					t.Fatalf("rollback err=%v mutations=%d", err, len(got))
				}
			case "retry", "cancel":
				wantErr := injected
				if mode == "cancel" {
					wantErr = context.Canceled
				}
				if !errors.Is(err, wantErr) || len(got) != 64 {
					t.Fatalf("partial repair err=%v mutations=%d/%d", err, len(got), len(want))
				}
			case "writer":
				if err != nil || !writerProgress || maxWrites > 64 {
					t.Fatalf("writer window=%t err=%v largest transaction=%d", writerProgress, err, maxWrites)
				}
				if session, err := writer.GetSession("independent-writer"); err != nil || session == nil {
					t.Fatalf("writer session=%+v err=%v", session, err)
				}
			}
			s.hooks = defaultStoreHooks()
			if err := s.EnsureEnrolledProjectSyncMutations(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := batchRepairJournal(t, s); !reflect.DeepEqual(got, want) {
				t.Fatalf("retry changed payloads or identities: got=%d want=%d", len(got), len(want))
			}
			before, err := s.MaxPendingSyncMutationSeq(DefaultSyncTargetKey)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.EnsureEnrolledProjectSyncMutations(context.Background()); err != nil {
				t.Fatal(err)
			}
			if after, err := s.MaxPendingSyncMutationSeq(DefaultSyncTargetKey); err != nil || after != before {
				t.Fatalf("memoized repair changed public sequence: %d -> %d, %v", before, after, err)
			}
			if mode == "writer" {
				fresh, err := New(s.cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := fresh.Close(); err != nil {
						t.Error(err)
					}
				})
				if err := fresh.EnsureEnrolledProjectSyncMutations(context.Background()); err != nil {
					t.Fatal(err)
				}
				if after, err := fresh.MaxPendingSyncMutationSeq(DefaultSyncTargetKey); err != nil || after != before {
					t.Fatalf("fresh control changed sequence: %d -> %d, %v", before, after, err)
				}
			}
		})
	}
}

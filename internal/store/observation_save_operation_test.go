package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestAddObservationWithOperationIDIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("session-1", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	params := AddObservationParams{
		SessionID:   "session-1",
		Type:        "manual",
		Title:       "Replay-safe title",
		Content:     "Replay-safe content.",
		Project:     "test-project",
		Scope:       "project",
		TopicKey:    "",
		OperationID: "op-replay-1",
	}

	firstID, err := s.AddObservation(params)
	if err != nil {
		t.Fatalf("first AddObservation: %v", err)
	}
	if firstID == 0 {
		t.Fatalf("first AddObservation returned zero id")
	}

	secondID, err := s.AddObservation(params)
	if err != nil {
		t.Fatalf("second AddObservation: %v", err)
	}
	if secondID != firstID {
		t.Fatalf("replay returned id %d, want %d", secondID, firstID)
	}

	count, err := countObservationSaveOperations(s)
	if err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if count != 1 {
		t.Fatalf("ledger has %d rows, want 1", count)
	}
}

func TestAddObservationWithOperationIDDetectsPayloadConflict(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("session-1", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	base := AddObservationParams{
		SessionID:   "session-1",
		Type:        "manual",
		Title:       "Replay-safe title",
		Content:     "Replay-safe content.",
		Project:     "test-project",
		Scope:       "project",
		OperationID: "op-conflict-1",
	}

	if _, err := s.AddObservation(base); err != nil {
		t.Fatalf("first AddObservation: %v", err)
	}

	conflict := base
	conflict.Content = "Different content under the same operation id."
	_, err := s.AddObservation(conflict)
	if !errors.Is(err, ErrObservationOperationConflict) {
		t.Fatalf("conflict error = %v, want ErrObservationOperationConflict", err)
	}
}

func TestAddObservationWithoutOperationIDWritesNoLedgerRow(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("session-1", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	params := AddObservationParams{
		SessionID: "session-1",
		Type:      "manual",
		Title:     "Regular title",
		Content:   "Regular content.",
		Project:   "test-project",
		Scope:     "project",
	}

	if _, err := s.AddObservation(params); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	count, err := countObservationSaveOperations(s)
	if err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if count != 0 {
		t.Fatalf("ledger has %d rows, want 0", count)
	}
}

func TestAddObservationWithOperationIDReturnsExpiredAfterObservationDeletion(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("session-1", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	params := AddObservationParams{
		SessionID:   "session-1",
		Type:        "manual",
		Title:       "Ephemeral",
		Content:     "Will be deleted.",
		Project:     "test-project",
		Scope:       "project",
		OperationID: "op-expired-1",
	}

	id, err := s.AddObservation(params)
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if err := s.DeleteObservation(id, true); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}

	_, err = s.AddObservation(params)
	if !errors.Is(err, ErrObservationOperationExpired) {
		t.Fatalf("replay after deletion error = %v, want ErrObservationOperationExpired", err)
	}

	var committedID sql.NullInt64
	err = s.db.QueryRow(`SELECT observation_id FROM observation_save_operations WHERE operation_id = ?`, params.OperationID).Scan(&committedID)
	if err != nil {
		t.Fatalf("read tombstoned operation: %v", err)
	}
	if committedID.Valid {
		t.Fatalf("ledger observation_id = %d, want NULL for tombstoned operation", committedID.Int64)
	}
}

func TestObservationOperationNULBoundaryConflict(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("nul", "test-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	p := AddObservationParams{SessionID: "nul", Project: "test-project", Type: "manual", Title: "A\x00B", Content: "C", OperationID: "nul"}
	if _, err := s.AddObservation(p); err != nil {
		t.Fatal(err)
	}
	p.Title, p.Content = "A", "B\x00C"
	if _, err := s.AddObservation(p); !errors.Is(err, ErrObservationOperationConflict) {
		t.Fatalf("NUL boundary replay: %v, want conflict", err)
	}
}

func TestObservationOperationUnsupportedFingerprint(t *testing.T) {
	for _, expression := range []string{"replace(fingerprint, 'v1:', '')", "'v2:unsupported'"} {
		t.Run(expression, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("legacy", "test-project", "/tmp"); err != nil {
				t.Fatal(err)
			}
			p := AddObservationParams{SessionID: "legacy", Project: "test-project", Type: "manual", Title: "Legacy", Content: "Content", OperationID: "legacy"}
			if _, err := s.AddObservation(p); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE observation_save_operations SET fingerprint = ` + expression); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddObservation(p); !errors.Is(err, ErrObservationOperationExpired) {
				t.Fatalf("unsupported fingerprint: %v, want expired", err)
			}
		})
	}
}

func TestObservationOperationReplayBeforeOwnershipResolution(t *testing.T) {
	for _, project := range []string{" TEST-PROJECT ", ""} {
		t.Run(project, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.CreateSession("replay", "test-project", "/tmp"); err != nil {
				t.Fatal(err)
			}
			if err := s.EnrollProject("test-project"); err != nil {
				t.Fatal(err)
			}
			p := AddObservationParams{SessionID: "replay", Project: project, Type: "manual", Title: "Replay", Content: "Content", OperationID: "replay"}
			id, err := s.AddObservation(p)
			if err != nil {
				t.Fatal(err)
			}
			changed := p
			if project == "" {
				changed.Project = "test-project"
			} else {
				changed.Project = ""
			}
			if _, err := s.AddObservation(changed); !errors.Is(err, ErrObservationOperationConflict) {
				t.Errorf("changed request selector: %v, want conflict", err)
			}
			// Simulate legacy unowned state after commit; acknowledged sync cannot mask adoption.
			if _, err := s.db.Exec(`UPDATE sessions SET project = '', ownership_mode = NULL WHERE id = 'replay'; UPDATE sync_mutations SET acked_at = datetime('now')`); err != nil {
				t.Fatal(err)
			}
			var before, after int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if got, err := s.AddObservation(p); err != nil || got != id {
				t.Errorf("replay = %d, %v; want %d, nil", got, err, id)
			}
			var owner string
			if err := s.db.QueryRow(`SELECT project, (SELECT COUNT(*) FROM sync_mutations) FROM sessions WHERE id = 'replay'`).Scan(&owner, &after); err != nil {
				t.Fatal(err)
			}
			if owner != "" || after != before {
				t.Fatalf("replay mutated ownership/sync: owner=%q, mutations=%d -> %d", owner, before, after)
			}
		})
	}
}

func TestObservationOperationDuplicateLedgerInsertFails(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("duplicate", "test-project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	p := AddObservationParams{SessionID: "duplicate", Project: "test-project", Type: "manual", Title: "Duplicate", Content: "Content", OperationID: "duplicate"}
	id, err := s.AddObservation(p)
	if err != nil {
		t.Fatal(err)
	}
	err = s.withTx(func(tx *sql.Tx) error {
		obs, err := s.getObservationTx(tx, id)
		if err != nil {
			return err
		}
		return s.recordObservationSaveOperationTx(tx, p.OperationID, "v1:replacement", id, obs)
	})
	if err == nil {
		t.Fatal("duplicate ledger insert succeeded")
	}
}

func TestGetObservationSaveResult(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("result", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if got, err := s.GetObservationSaveResult(""); err != nil || got != 0 {
		t.Fatalf("empty operation_id: got %d, %v; want 0, nil", got, err)
	}
	if got, err := s.GetObservationSaveResult("op-unknown"); err != nil || got != 0 {
		t.Fatalf("unknown operation_id: got %d, %v; want 0, nil", got, err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID:   "result",
		Type:        "manual",
		Title:       "Result",
		Content:     "Content.",
		Project:     "test-project",
		Scope:       "project",
		OperationID: "op-result",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	got, err := s.GetObservationSaveResult("op-result")
	if err != nil || got != id {
		t.Fatalf("GetObservationSaveResult = %d, %v; want %d, nil", got, err, id)
	}

	if err := s.DeleteObservation(id, true); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}
	if got, err := s.GetObservationSaveResult("op-result"); err != nil || got != 0 {
		t.Fatalf("tombstoned operation_id: got %d, %v; want 0, nil", got, err)
	}
}

func TestObservationOperationRecorded(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("recorded", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if got, err := s.ObservationOperationRecorded(""); err != nil || got {
		t.Fatalf("empty operation_id: got %v, %v; want false, nil", got, err)
	}
	if got, err := s.ObservationOperationRecorded("op-unknown"); err != nil || got {
		t.Fatalf("unknown operation_id: got %v, %v; want false, nil", got, err)
	}

	id, err := s.AddObservation(AddObservationParams{
		SessionID:   "recorded",
		Type:        "manual",
		Title:       "Recorded",
		Content:     "Content.",
		Project:     "test-project",
		Scope:       "project",
		OperationID: "op-recorded",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	got, err := s.ObservationOperationRecorded("op-recorded")
	if err != nil || !got {
		t.Fatalf("committed operation recorded: got %v, %v; want true, nil", got, err)
	}

	if err := s.DeleteObservation(id, true); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}
	got, err = s.ObservationOperationRecorded("op-recorded")
	if err != nil || !got {
		t.Fatalf("tombstoned operation recorded: got %v, %v; want true, nil", got, err)
	}
}

func countObservationSaveOperations(s *Store) (int, error) {
	var count int
	row := s.db.QueryRow(`SELECT COUNT(*) FROM observation_save_operations`)
	err := row.Scan(&count)
	return count, err
}

func countObservationSaveOperationsFor(s *Store, operationID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM observation_save_operations WHERE operation_id = ?`, operationID).Scan(&count)
	return count, err
}

func countPendingSyncMutations(s *Store) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE target_key = ? AND acked_at IS NULL AND disposition = ?`, DefaultSyncTargetKey, SyncMutationDispositionPending).Scan(&count)
	return count, err
}

func countObservationVersions(s *Store, observationID int64) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM observation_versions WHERE observation_id = ?`, observationID).Scan(&count)
	return count, err
}

func readObservationCounters(s *Store, observationID int64) (revision, duplicate int, created, updated, lastSeen string, err error) {
	var lastSeenPtr sql.NullString
	err = s.db.QueryRow(`SELECT revision_count, duplicate_count, created_at, updated_at, last_seen_at FROM observations WHERE id = ?`, observationID).Scan(&revision, &duplicate, &created, &updated, &lastSeenPtr)
	if lastSeenPtr.Valid {
		lastSeen = lastSeenPtr.String
	}
	return
}

func newStoreAt(t *testing.T, dir string) *Store {
	t.Helper()
	cfg := mustDefaultConfig(t)
	cfg.DataDir = dir
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New(%q): %v", dir, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestObservationSaveOperationConcurrentSameOperationReplay verifies that two
// independent Store instances sharing one database directory can replay the
// same operation id concurrently and produce exactly one logical effect: one
// observation, one ledger row, one stable id, and no duplicate/topic/sync side
// effects.
func TestObservationSaveOperationConcurrentSameOperationReplay(t *testing.T) {
	dataDir := t.TempDir()

	initStore := newStoreAt(t, dataDir)
	if err := initStore.CreateSession("session-1", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := initStore.EnrollProject("test-project"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	_ = initStore.Close()

	s1 := newStoreAt(t, dataDir)
	s2 := newStoreAt(t, dataDir)

	params := AddObservationParams{
		SessionID:   "session-1",
		Type:        "manual",
		Title:       "Concurrent replay title",
		Content:     "Concurrent replay content.",
		Project:     "test-project",
		Scope:       "project",
		TopicKey:    "topic-concurrent-replay",
		OperationID: "op-concurrent-same",
	}

	before, err := countPendingSyncMutations(s1)
	if err != nil {
		t.Fatalf("count pending mutations before: %v", err)
	}

	start := make(chan struct{})
	type result struct {
		id  int64
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, s := range []*Store{s1, s2} {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			<-start
			id, err := store.AddObservation(params)
			results <- result{id, err}
		}(s)
	}
	close(start)
	wg.Wait()
	close(results)

	var ids []int64
	for r := range results {
		if r.err != nil {
			t.Fatalf("concurrent AddObservation: %v", r.err)
		}
		ids = append(ids, r.id)
	}
	if len(ids) != 2 || ids[0] != ids[1] || ids[0] == 0 {
		t.Fatalf("expected two identical non-zero ids, got %v", ids)
	}
	observationID := ids[0]

	var obsCount int
	if err := s1.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE id = ?`, observationID).Scan(&obsCount); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if obsCount != 1 {
		t.Fatalf("observation count = %d, want 1", obsCount)
	}

	ledgerCount, err := countObservationSaveOperationsFor(s1, params.OperationID)
	if err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if ledgerCount != 1 {
		t.Fatalf("ledger rows for operation = %d, want 1", ledgerCount)
	}

	rev, dup, _, _, _, err := readObservationCounters(s1, observationID)
	if err != nil {
		t.Fatalf("read observation counters: %v", err)
	}
	if rev != 1 {
		t.Fatalf("revision_count = %d, want 1", rev)
	}
	if dup != 1 {
		t.Fatalf("duplicate_count = %d, want 1", dup)
	}

	versions, err := countObservationVersions(s1, observationID)
	if err != nil {
		t.Fatalf("count observation versions: %v", err)
	}
	if versions != 0 {
		t.Fatalf("observation versions = %d, want 0", versions)
	}

	after, err := countPendingSyncMutations(s1)
	if err != nil {
		t.Fatalf("count pending mutations after: %v", err)
	}
	// Exactly one observation upsert was enqueued by the winning commit; the
	// replayed commit returned the committed id without enqueueing a second
	// mutation. The +1 accounts for the session backfill mutation created when
	// the project was enrolled.
	if after != before+1 {
		t.Fatalf("pending sync mutations = %d, want %d (before=%d)", after, before+1, before)
	}

	lookupID, err := s1.GetObservationSaveResult(params.OperationID)
	if err != nil {
		t.Fatalf("GetObservationSaveResult: %v", err)
	}
	if lookupID != observationID {
		t.Fatalf("lookup returned %d, want %d", lookupID, observationID)
	}
}

// TestObservationSaveOperationDistinctKeyedSaves verifies that keyed logical
// saves remain distinct operations and observations. Using different topic_keys
// prevents accidental merging by the deduplication window.
func TestObservationSaveOperationDistinctKeyedSaves(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("session-1", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.EnrollProject("test-project"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	const n = 10
	var ids []int64
	for i := 0; i < n; i++ {
		id, err := s.AddObservation(AddObservationParams{
			SessionID:   "session-1",
			Type:        "manual",
			Title:       fmt.Sprintf("Keyed title %d", i),
			Content:     fmt.Sprintf("Keyed content %d.", i),
			Project:     "test-project",
			Scope:       "project",
			TopicKey:    fmt.Sprintf("topic-%d", i),
			OperationID: fmt.Sprintf("op-keyed-%d", i),
		})
		if err != nil {
			t.Fatalf("AddObservation %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	var obsCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE session_id = ?`, "session-1").Scan(&obsCount); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if obsCount != n {
		t.Fatalf("observation count = %d, want %d", obsCount, n)
	}

	var distinctTopicKeys int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT topic_key) FROM observations WHERE session_id = ?`, "session-1").Scan(&distinctTopicKeys); err != nil {
		t.Fatalf("count distinct topic keys: %v", err)
	}
	if distinctTopicKeys != n {
		t.Fatalf("distinct topic keys = %d, want %d", distinctTopicKeys, n)
	}

	ledgerCount, err := countObservationSaveOperations(s)
	if err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if ledgerCount != n {
		t.Fatalf("ledger rows = %d, want %d", ledgerCount, n)
	}

	for i, id := range ids {
		lookup, err := s.GetObservationSaveResult(fmt.Sprintf("op-keyed-%d", i))
		if err != nil {
			t.Fatalf("GetObservationSaveResult %d: %v", i, err)
		}
		if lookup != id {
			t.Fatalf("lookup %d = %d, want %d", i, lookup, id)
		}
	}
}

// TestObservationSaveOperationRestartRecovery verifies that a committed
// operation's lookup result survives Store.Close and a fresh Store.New on the
// same directory, and that unsupported/tombstoned states remain detectable
// after restart.
func TestObservationSaveOperationRestartRecovery(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("session-1", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.EnrollProject("test-project"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	params := AddObservationParams{
		SessionID:   "session-1",
		Type:        "manual",
		Title:       "Restart title",
		Content:     "Restart content.",
		Project:     "test-project",
		Scope:       "project",
		TopicKey:    "topic-restart",
		OperationID: "op-restart",
	}
	id, err := s.AddObservation(params)
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s = newStoreAt(t, s.DataDir())

	lookupID, err := s.GetObservationSaveResult(params.OperationID)
	if err != nil {
		t.Fatalf("GetObservationSaveResult after restart: %v", err)
	}
	if lookupID != id {
		t.Fatalf("lookup after restart = %d, want %d", lookupID, id)
	}

	obs, err := s.GetObservation(id)
	if err != nil {
		t.Fatalf("GetObservation after restart: %v", err)
	}
	if obs == nil || obs.Title != params.Title {
		t.Fatalf("observation after restart = %#v, want title %q", obs, params.Title)
	}

	recorded, err := s.ObservationOperationRecorded(params.OperationID)
	if err != nil || !recorded {
		t.Fatalf("ObservationOperationRecorded after restart: got %v, %v; want true, nil", recorded, err)
	}

	if err := s.DeleteObservation(id, true); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s = newStoreAt(t, s.DataDir())
	if _, err := s.AddObservation(params); !errors.Is(err, ErrObservationOperationExpired) {
		t.Fatalf("replay after deletion: %v, want ErrObservationOperationExpired", err)
	}
	var tombstoneID sql.NullInt64
	if err := s.db.QueryRow(`SELECT observation_id FROM observation_save_operations WHERE operation_id = ?`, params.OperationID).Scan(&tombstoneID); err != nil {
		t.Fatalf("read tombstone: %v", err)
	}
	if tombstoneID.Valid {
		t.Fatalf("tombstoned observation_id = %d, want NULL", tombstoneID.Int64)
	}
	recorded, err = s.ObservationOperationRecorded(params.OperationID)
	if err != nil || !recorded {
		t.Fatalf("tombstone recorded after restart: got %v, %v; want true, nil", recorded, err)
	}
}

// TestObservationSaveOperationUnsupportedFingerprintAfterRestart verifies that
// an operation whose fingerprint becomes unsupported is still detectable as
// expired after the store is reopened.
func TestObservationSaveOperationUnsupportedFingerprintAfterRestart(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("session-1", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	params := AddObservationParams{
		SessionID:   "session-1",
		Type:        "manual",
		Title:       "Unsupported fingerprint title",
		Content:     "Unsupported fingerprint content.",
		Project:     "test-project",
		Scope:       "project",
		OperationID: "op-unsupported-restart",
	}
	if _, err := s.AddObservation(params); err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE observation_save_operations SET fingerprint = 'v2:unsupported'`); err != nil {
		t.Fatalf("update fingerprint: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s = newStoreAt(t, s.DataDir())
	if _, err := s.AddObservation(params); !errors.Is(err, ErrObservationOperationExpired) {
		t.Fatalf("replay with unsupported fingerprint after restart: %v, want ErrObservationOperationExpired", err)
	}
	recorded, err := s.ObservationOperationRecorded(params.OperationID)
	if err != nil || !recorded {
		t.Fatalf("unsupported operation recorded after restart: got %v, %v; want true, nil", recorded, err)
	}
}

// TestObservationSaveOperationReplaySideEffectInvariance verifies that an
// exact replay does not change revision_count, duplicate_count, timestamps,
// observation version history, or the pending sync-mutation count relative to
// the first save.
func TestObservationSaveOperationReplaySideEffectInvariance(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("session-1", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.EnrollProject("test-project"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}

	params := AddObservationParams{
		SessionID:   "session-1",
		Type:        "manual",
		Title:       "Invariant title",
		Content:     "Invariant content.",
		Project:     "test-project",
		Scope:       "project",
		TopicKey:    "topic-invariant",
		OperationID: "op-invariant",
	}
	id, err := s.AddObservation(params)
	if err != nil {
		t.Fatalf("first AddObservation: %v", err)
	}

	beforeMutations, err := countPendingSyncMutations(s)
	if err != nil {
		t.Fatalf("count pending mutations before replay: %v", err)
	}
	revBefore, dupBefore, createdBefore, updatedBefore, lastSeenBefore, err := readObservationCounters(s, id)
	if err != nil {
		t.Fatalf("read observation counters before replay: %v", err)
	}
	versionsBefore, err := countObservationVersions(s, id)
	if err != nil {
		t.Fatalf("count observation versions before replay: %v", err)
	}

	replayID, err := s.AddObservation(params)
	if err != nil {
		t.Fatalf("replay AddObservation: %v", err)
	}
	if replayID != id {
		t.Fatalf("replay id = %d, want %d", replayID, id)
	}

	afterMutations, err := countPendingSyncMutations(s)
	if err != nil {
		t.Fatalf("count pending mutations after replay: %v", err)
	}
	if afterMutations != beforeMutations {
		t.Fatalf("pending sync mutations changed: before=%d after=%d", beforeMutations, afterMutations)
	}

	revAfter, dupAfter, createdAfter, updatedAfter, lastSeenAfter, err := readObservationCounters(s, id)
	if err != nil {
		t.Fatalf("read observation counters after replay: %v", err)
	}
	if revAfter != revBefore {
		t.Fatalf("revision_count changed: %d -> %d", revBefore, revAfter)
	}
	if dupAfter != dupBefore {
		t.Fatalf("duplicate_count changed: %d -> %d", dupBefore, dupAfter)
	}
	if createdAfter != createdBefore {
		t.Fatalf("created_at changed: %q -> %q", createdBefore, createdAfter)
	}
	if updatedAfter != updatedBefore {
		t.Fatalf("updated_at changed: %q -> %q", updatedBefore, updatedAfter)
	}
	if lastSeenAfter != lastSeenBefore {
		t.Fatalf("last_seen_at changed: %q -> %q", lastSeenBefore, lastSeenAfter)
	}

	versionsAfter, err := countObservationVersions(s, id)
	if err != nil {
		t.Fatalf("count observation versions after replay: %v", err)
	}
	if versionsAfter != versionsBefore {
		t.Fatalf("observation versions changed: %d -> %d", versionsBefore, versionsAfter)
	}
}

// TestObservationSaveOperationConcurrentConflict verifies that two independent
// Stores using the same operation_id with different payloads produce exactly
// one commit and one conflict, with no partial state.
func TestObservationSaveOperationConcurrentConflict(t *testing.T) {
	dataDir := t.TempDir()

	initStore := newStoreAt(t, dataDir)
	if err := initStore.CreateSession("session-1", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_ = initStore.Close()

	s1 := newStoreAt(t, dataDir)
	s2 := newStoreAt(t, dataDir)

	base := AddObservationParams{
		SessionID:   "session-1",
		Type:        "manual",
		Title:       "Conflict title",
		Content:     "Conflict content A.",
		Project:     "test-project",
		Scope:       "project",
		OperationID: "op-concurrent-conflict",
	}
	opposing := base
	opposing.Content = "Conflict content B."

	start := make(chan struct{})
	type result struct {
		id  int64
		err error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup

	calls := []struct {
		store  *Store
		params AddObservationParams
	}{
		{s1, base},
		{s2, opposing},
	}
	for _, c := range calls {
		wg.Add(1)
		go func(store *Store, params AddObservationParams) {
			defer wg.Done()
			<-start
			id, err := store.AddObservation(params)
			results <- result{id, err}
		}(c.store, c.params)
	}
	close(start)
	wg.Wait()
	close(results)

	var successes, conflicts int
	var winnerID int64
	var winnerContent string
	for r := range results {
		if r.err == nil {
			successes++
			winnerID = r.id
			continue
		}
		if errors.Is(r.err, ErrObservationOperationConflict) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent error: %v", r.err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want 1 and 1", successes, conflicts)
	}

	var obsCount int
	if err := s1.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE session_id = ?`, "session-1").Scan(&obsCount); err != nil {
		t.Fatalf("count observations: %v", err)
	}
	if obsCount != 1 {
		t.Fatalf("observation count = %d, want 1", obsCount)
	}

	ledgerCount, err := countObservationSaveOperationsFor(s1, base.OperationID)
	if err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if ledgerCount != 1 {
		t.Fatalf("ledger rows for operation = %d, want 1", ledgerCount)
	}

	obs, err := s1.GetObservation(winnerID)
	if err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	if obs == nil {
		t.Fatal("GetObservation returned nil")
	}
	switch obs.Content {
	case base.Content:
		winnerContent = base.Content
	case opposing.Content:
		winnerContent = opposing.Content
	default:
		t.Fatalf("unexpected persisted content %q", obs.Content)
	}
	if winnerContent == "" {
		t.Fatal("winner content not matched")
	}
}

// TestObservationSaveOperationRollbackLeavesNoState verifies that a failure
// inside the AddObservation transaction rolls back both the observation and
// the ledger row. It uses the existing production storeHooks.exec seam to
// inject a deterministic sync-enqueue failure and a deterministic commit
// failure; the hook is restored after each subtest so production behavior is
// unchanged.
func TestObservationSaveOperationRollbackLeavesNoState(t *testing.T) {
	t.Run("enqueue_failure", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("session-1", "test-project", "/tmp"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if err := s.EnrollProject("test-project"); err != nil {
			t.Fatalf("EnrollProject: %v", err)
		}

		originalExec := s.hooks.exec
		t.Cleanup(func() { s.hooks.exec = originalExec })
		var calls int
		s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
			// The ledger insert is executed through tx.Exec directly and cannot be
			// intercepted here; failing the following sync-mutation insert still
			// happens after the observation and ledger row are written in the same
			// transaction, so the rollback removes all three.
			if strings.Contains(query, "INSERT INTO sync_mutations") && calls == 0 {
				calls++
				return nil, errors.New("forced sync mutation insert failure")
			}
			return originalExec(db, query, args...)
		}

		params := AddObservationParams{
			SessionID:   "session-1",
			Type:        "manual",
			Title:       "Rollback title",
			Content:     "Rollback content.",
			Project:     "test-project",
			Scope:       "project",
			OperationID: "op-rollback-statement",
		}
		if _, err := s.AddObservation(params); err == nil {
			t.Fatal("expected AddObservation to fail")
		}

		var obsCount int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE session_id = ? AND title = ?`, "session-1", params.Title).Scan(&obsCount); err != nil {
			t.Fatalf("count observations: %v", err)
		}
		if obsCount != 0 {
			t.Fatalf("observation count = %d, want 0", obsCount)
		}

		ledgerCount, err := countObservationSaveOperationsFor(s, params.OperationID)
		if err != nil {
			t.Fatalf("count ledger rows: %v", err)
		}
		if ledgerCount != 0 {
			t.Fatalf("ledger rows = %d, want 0", ledgerCount)
		}

		var obsMutations int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ?`, SyncEntityObservation).Scan(&obsMutations); err != nil {
			t.Fatalf("count observation sync mutations: %v", err)
		}
		if obsMutations != 0 {
			t.Fatalf("observation sync mutations = %d, want 0", obsMutations)
		}
	})

	t.Run("commit_failure", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.CreateSession("session-1", "test-project", "/tmp"); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if err := s.EnrollProject("test-project"); err != nil {
			t.Fatalf("EnrollProject: %v", err)
		}

		originalCommit := s.hooks.commit
		t.Cleanup(func() { s.hooks.commit = originalCommit })
		s.hooks.commit = func(tx *sql.Tx) error { return errors.New("forced commit failure") }

		params := AddObservationParams{
			SessionID:   "session-1",
			Type:        "manual",
			Title:       "Commit failure title",
			Content:     "Commit failure content.",
			Project:     "test-project",
			Scope:       "project",
			OperationID: "op-rollback-commit",
		}
		if _, err := s.AddObservation(params); err == nil {
			t.Fatal("expected AddObservation to fail")
		}

		var obsCount int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM observations WHERE session_id = ? AND title = ?`, "session-1", params.Title).Scan(&obsCount); err != nil {
			t.Fatalf("count observations: %v", err)
		}
		if obsCount != 0 {
			t.Fatalf("observation count = %d, want 0", obsCount)
		}

		ledgerCount, err := countObservationSaveOperationsFor(s, params.OperationID)
		if err != nil {
			t.Fatalf("count ledger rows: %v", err)
		}
		if ledgerCount != 0 {
			t.Fatalf("ledger rows = %d, want 0", ledgerCount)
		}
	})
}

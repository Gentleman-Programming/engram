package store

import (
	"database/sql"
	"errors"
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

func TestGetObservationSaveResultReturnsCommittedID(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateSession("session-1", "test-project", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	params := AddObservationParams{
		SessionID:   "session-1",
		Type:        "manual",
		Title:       "Lookup title",
		Content:     "Lookup content.",
		Project:     "test-project",
		Scope:       "project",
		OperationID: "op-lookup-1",
	}

	committedID, err := s.AddObservation(params)
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	got, err := s.GetObservationSaveResult("op-lookup-1")
	if err != nil {
		t.Fatalf("GetObservationSaveResult: %v", err)
	}
	if got != committedID {
		t.Fatalf("GetObservationSaveResult returned %d, want %d", got, committedID)
	}
}

func TestGetObservationSaveResultReturnsZeroForUnknownID(t *testing.T) {
	s := newTestStore(t)

	got, err := s.GetObservationSaveResult("op-does-not-exist")
	if err != nil {
		t.Fatalf("GetObservationSaveResult: %v", err)
	}
	if got != 0 {
		t.Fatalf("GetObservationSaveResult returned %d, want 0", got)
	}
}

func TestGetObservationSaveResultIgnoresEmptyID(t *testing.T) {
	s := newTestStore(t)

	got, err := s.GetObservationSaveResult("")
	if err != nil {
		t.Fatalf("GetObservationSaveResult: %v", err)
	}
	if got != 0 {
		t.Fatalf("GetObservationSaveResult returned %d, want 0", got)
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

	got, err := s.GetObservationSaveResult("op-expired-1")
	if err != nil {
		t.Fatalf("GetObservationSaveResult: %v", err)
	}
	if got != 0 {
		t.Fatalf("GetObservationSaveResult returned %d, want 0 for tombstoned operation", got)
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

func countObservationSaveOperations(s *Store) (int, error) {
	var count int
	row := s.db.QueryRow(`SELECT COUNT(*) FROM observation_save_operations`)
	err := row.Scan(&count)
	return count, err
}

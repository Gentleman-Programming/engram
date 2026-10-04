package store

import (
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

func countObservationSaveOperations(s *Store) (int, error) {
	var count int
	row := s.db.QueryRow(`SELECT COUNT(*) FROM observation_save_operations`)
	err := row.Scan(&count)
	return count, err
}

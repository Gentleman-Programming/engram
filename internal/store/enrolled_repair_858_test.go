package store

import "testing"

func TestRepairEnrolledProjectSyncMutationsBackfillsDeletedObservationDelete(t *testing.T) {
	s := newTestStoreRaw(t)
	if err := s.CreateSession("repair-858-obs-session", "repair-858-obs", "/tmp/repair-858"); err != nil {
		t.Fatal(err)
	}
	_, syncID := addTestObsSession(t, s, "repair-858-obs-session", "deleted", "decision", "repair-858-obs", "project")
	if err := s.EnrollProject("repair-858-obs"); err != nil {
		t.Fatal(err)
	}
	var observationID int64
	if err := s.db.QueryRow(`SELECT id FROM observations WHERE sync_id = ?`, syncID).Scan(&observationID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObservation(observationID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM sync_mutations WHERE entity = ? AND entity_key = ? AND op = ?`, SyncEntityObservation, syncID, SyncOpDelete); err != nil {
		t.Fatal(err)
	}

	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatal(err)
	}
	var upserts, deletes int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND op = ?`, SyncEntityObservation, syncID, SyncOpUpsert).Scan(&upserts); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND op = ?`, SyncEntityObservation, syncID, SyncOpDelete).Scan(&deletes); err != nil {
		t.Fatal(err)
	}
	if upserts != 1 || deletes != 1 {
		t.Fatalf("expected existing upsert and repaired delete, got upserts=%d deletes=%d", upserts, deletes)
	}
}

func TestRepairEnrolledProjectSyncMutationsBackfillsPromptTombstoneDelete(t *testing.T) {
	s := newTestStoreRaw(t)
	if err := s.CreateSession("repair-858-prompt-session", "repair-858-prompt", "/tmp/repair-858"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("repair-858-prompt"); err != nil {
		t.Fatal(err)
	}
	promptID, err := s.AddPrompt(AddPromptParams{SessionID: "repair-858-prompt-session", Content: "deleted", Project: "repair-858-prompt"})
	if err != nil {
		t.Fatal(err)
	}
	var promptSyncID string
	if err := s.db.QueryRow(`SELECT sync_id FROM user_prompts WHERE id = ?`, promptID).Scan(&promptSyncID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePrompt(promptID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM sync_mutations WHERE entity = ? AND entity_key = ? AND op = ?`, SyncEntityPrompt, promptSyncID, SyncOpDelete); err != nil {
		t.Fatal(err)
	}

	if err := s.repairEnrolledProjectSyncMutations(); err != nil {
		t.Fatal(err)
	}
	var upserts, deletes int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND op = ?`, SyncEntityPrompt, promptSyncID, SyncOpUpsert).Scan(&upserts); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations WHERE entity = ? AND entity_key = ? AND op = ?`, SyncEntityPrompt, promptSyncID, SyncOpDelete).Scan(&deletes); err != nil {
		t.Fatal(err)
	}
	if upserts != 1 || deletes != 1 {
		t.Fatalf("expected existing upsert and repaired delete, got upserts=%d deletes=%d", upserts, deletes)
	}
}

func TestRepairEnrolledProjectSyncMutationsPropagatesDetectorErrorWithoutWriting(t *testing.T) {
	s := newTestStoreRaw(t)
	if _, err := s.db.Exec(`INSERT INTO sync_enrolled_projects (project) VALUES (?)`, "repair-858-error"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO sessions (id, project, directory) VALUES (?, ?, ?)`, "repair-858-error-session", "repair-858-error", "/tmp/repair-858"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE sync_enrolled_projects`); err != nil {
		t.Fatal(err)
	}

	if err := s.repairEnrolledProjectSyncMutations(); err == nil {
		t.Fatal("expected enrolled-project detector error")
	}
	var mutations int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sync_mutations`).Scan(&mutations); err != nil {
		t.Fatal(err)
	}
	if mutations != 0 {
		t.Fatalf("expected no mutation writes after detector failure, got %d", mutations)
	}
}

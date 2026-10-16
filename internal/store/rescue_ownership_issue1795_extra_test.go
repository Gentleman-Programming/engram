package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRescueBlankManualProjectJournalsOwnershipAndRejectsForeignRegistration(t *testing.T) {
	type legacySession struct{ id, project string }
	const id = "manual-save-target"
	s := newTestStoreWithNullableLegacySessions(t, legacySession{id, ""})
	enrollTestProject(t, s, "target")
	enrollTestProject(t, s, "other")
	params := ProjectRescueParams{TargetProject: "target", SessionIDs: []string{id}}
	result, err := s.RescueNullProjectOwnership(params)
	if err != nil || result == nil || result.RescuedSessions != 1 || !result.Journaled || !result.Complete {
		t.Fatalf("rescue = %#v, err=%v, want one complete journaled session", result, err)
	}
	before, err := s.GetSession(id)
	if err != nil || before.Project != "target" || before.OwnershipMode != SessionOwnershipProjectOwned {
		t.Fatalf("rescued session = %#v, err=%v", before, err)
	}
	journal, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil || len(journal) != 1 {
		t.Fatalf("journal = %#v, err=%v, want one mutation", journal, err)
	}
	var payload syncSessionPayload
	if err := json.Unmarshal([]byte(journal[0].Payload), &payload); err != nil {
		t.Fatalf("decode journal payload: %v", err)
	}
	if journal[0].Entity != SyncEntitySession || journal[0].EntityKey != id || journal[0].Project != "target" || payload.ID != id || payload.Project != "target" || payload.OwnershipMode != SessionOwnershipProjectOwned {
		t.Fatalf("journal = %#v, payload = %#v, want target-owned manual session", journal, payload)
	}
	for _, mode := range []string{SessionOwnershipShared, SessionOwnershipProjectOwned} {
		err := s.CreateSessionWithOwnershipMode(id, "other", "/foreign", mode)
		if !errors.Is(err, ErrSessionOwnershipMismatch) {
			t.Fatalf("foreign %s registration = %v, want ownership mismatch", mode, err)
		}
		if mode == SessionOwnershipProjectOwned {
			var conflict *SessionProjectConflictError
			if !errors.As(err, &conflict) || conflict.SessionID != id || conflict.OwnerProject != "target" || conflict.RequestedProject != "other" {
				t.Fatalf("foreign strict conflict = %v", err)
			}
		} else if err.Error() != `session ownership does not match write project: session "manual-save-target" belongs to "target", not "other"` {
			t.Fatalf("foreign shared error = %q", err.Error())
		}
		after, err := s.GetSession(id)
		if err != nil || !reflect.DeepEqual(after, before) {
			t.Fatalf("foreign registration changed session: %#v, err=%v", after, err)
		}
		afterJournal, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
		if err != nil || !reflect.DeepEqual(afterJournal, journal) {
			t.Fatalf("foreign registration changed journal: %#v, err=%v", afterJournal, err)
		}
	}
	again, err := s.RescueNullProjectOwnership(params)
	if err != nil || again.RescuedSessions != 0 || again.SkippedRecords != 1 {
		t.Fatalf("repeat rescue = %#v, err=%v", again, err)
	}
	afterJournal, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil || !reflect.DeepEqual(afterJournal, journal) {
		t.Fatalf("repeat rescue changed journal: %#v, err=%v", afterJournal, err)
	}
}

func TestRescueBlankProjectRollsBackModeProjectAndJournalAfterJournalFailure(t *testing.T) {
	type legacySession struct{ id, project string }
	const id = "manual-save-target"
	s := newTestStoreWithNullableLegacySessions(t, legacySession{id, ""})
	enrollTestProject(t, s, "target")
	before, err := s.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	journalBefore, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatal(err)
	}
	originalExec := s.hooks.exec
	t.Cleanup(func() { s.hooks.exec = originalExec })
	injected := errors.New("injected failure after rescue journal insert")
	stampExecuted, journalExecuted := false, false
	s.hooks.exec = func(db execer, query string, args ...any) (sql.Result, error) {
		res, err := originalExec(db, query, args...)
		if err != nil {
			return res, err
		}
		if query == rescueSessionQuery.stampOwnershipMode {
			stampExecuted = true
		}
		if strings.Contains(query, "INSERT INTO sync_mutations") {
			journalExecuted = true
			return res, injected
		}
		return res, nil
	}
	result, err := s.RescueNullProjectOwnership(ProjectRescueParams{TargetProject: "target", SessionIDs: []string{id}})
	if !errors.Is(err, injected) || result != nil {
		t.Fatalf("failed rescue = %#v, err=%v, want nil result and injected failure", result, err)
	}
	if !stampExecuted || !journalExecuted {
		t.Fatalf("fault must follow both writes: stamp=%t journal=%t", stampExecuted, journalExecuted)
	}
	after, err := s.GetSession(id)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("rollback changed project/mode: before=%#v after=%#v err=%v", before, after, err)
	}
	journalAfter, err := s.ListPendingSyncMutations(DefaultSyncTargetKey, 100)
	if err != nil || !reflect.DeepEqual(journalAfter, journalBefore) {
		t.Fatalf("rollback changed journal: before=%#v after=%#v err=%v", journalBefore, journalAfter, err)
	}
}

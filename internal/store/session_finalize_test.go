package store

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func finalizeFixture(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	if err := s.CreateSession("finalize", "project", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("project"); err != nil {
		t.Fatal(err)
	}
	return s
}

type finalizeState struct {
	Session *Session
	Counts  map[string]int
	Seq     int64
}

func captureFinalizeState(t *testing.T, s *Store) finalizeState {
	t.Helper()
	state := finalizeState{Counts: map[string]int{}}
	var err error
	state.Session, err = s.GetSession("finalize")
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sessions", "observations", "session_finalizations", "sync_mutations"} {
		var count int
		if err := s.DB().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		state.Counts[table] = count
	}
	if err := s.DB().QueryRow("SELECT last_enqueued_seq FROM sync_state WHERE target_key = 'cloud'").Scan(&state.Seq); err != nil {
		t.Fatal(err)
	}
	return state
}

func assertFinalizeState(t *testing.T, s *Store, before finalizeState) {
	t.Helper()
	after := captureFinalizeState(t, s)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("state changed: before=%+v after=%+v", before, after)
	}
}

func TestFinalizeSessionPreservesFreeTextSummary(t *testing.T) {
	existing := "Existing free-text summary"
	empty := ""
	for _, summary := range []*string{nil, &existing, &empty} {
		name := "nil"
		if summary != nil {
			name = "existing"
			if *summary == "" {
				name = "empty"
			}
		}
		t.Run(name, func(t *testing.T) {
			s := finalizeFixture(t)
			if summary != nil {
				if _, err := s.DB().Exec("UPDATE sessions SET summary = ? WHERE id = 'finalize'", *summary); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.FinalizeSession(FinalizeSessionParams{SessionID: "finalize", Project: "project", Summary: "Structured content <private>secret</private>"})
			if err != nil {
				t.Fatal(err)
			}
			sess, err := s.GetSession("finalize")
			if err != nil || !reflect.DeepEqual(sess.Summary, summary) || sess.EndedAt == nil {
				t.Fatalf("summary changed: %+v %v", sess, err)
			}
			obs, err := s.GetObservation(result.SummaryID)
			if err != nil || obs.Content != "Structured content [REDACTED]" || obs.Title != "Session summary: project" {
				t.Fatalf("observation: %+v %v", obs, err)
			}
		})
	}
}

func TestFinalizeSessionHappyAndReopenRetry(t *testing.T) {
	s := finalizeFixture(t)
	p := FinalizeSessionParams{SessionID: "finalize", Project: " PROJECT ", Summary: "Caller-authored summary"}
	result, err := s.FinalizeSession(p)
	if err != nil || result.AlreadyFinalized || result.SummaryID == 0 || result.SummarySyncID == "" {
		t.Fatalf("result %+v: %v", result, err)
	}
	obs, err := s.GetObservation(result.SummaryID)
	if err != nil || obs.Type != "session_summary" || obs.Content != p.Summary || obs.SessionID != p.SessionID {
		t.Fatalf("observation %+v: %v", obs, err)
	}
	sess, err := s.GetSession(p.SessionID)
	if err != nil || sess.EndedAt == nil || sess.Summary != nil {
		t.Fatalf("session %+v: %v", sess, err)
	}
	var summaryMutations, sessionMutations int
	if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity = 'observation' AND entity_key = ?`, result.SummarySyncID).Scan(&summaryMutations); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM sync_mutations WHERE entity = 'session' AND entity_key = 'finalize' AND json_extract(payload, '$.ended_at') IS NOT NULL AND json_extract(payload, '$.summary') IS NULL`).Scan(&sessionMutations); err != nil {
		t.Fatal(err)
	}
	if summaryMutations != 1 || sessionMutations != 1 {
		t.Fatalf("sync: observation=%d session=%d", summaryMutations, sessionMutations)
	}
	cfg := s.cfg
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	before := captureFinalizeState(t, reopened)
	reopened.hooks.exec = func(execer, string, ...any) (sql.Result, error) { return nil, errors.New("retry wrote") }
	p.Summary = "Different retry text"
	p.Project = "other"
	if _, err := reopened.FinalizeSession(p); !errors.Is(err, ErrSessionOwnershipMismatch) {
		t.Fatalf("ownership: %v", err)
	}
	assertFinalizeState(t, reopened, before)
	p.Project = "project"
	retry, err := reopened.FinalizeSession(p)
	if err != nil || !retry.AlreadyFinalized || retry.SummaryID != result.SummaryID || retry.SummarySyncID != result.SummarySyncID {
		t.Fatalf("retry %+v: %v", retry, err)
	}
	assertFinalizeState(t, reopened, before)
}

func TestFinalizeSessionValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    FinalizeSessionParams
		want error
	}{
		{"missing identity", FinalizeSessionParams{Project: "project", Summary: "text"}, ErrSessionIDRequired},
		{"unknown", FinalizeSessionParams{SessionID: "unknown", Project: "project", Summary: "text"}, ErrSessionNotFound},
		{"exact identity", FinalizeSessionParams{SessionID: " finalize ", Project: "project", Summary: "text"}, ErrSessionNotFound},
		{"missing summary", FinalizeSessionParams{SessionID: "finalize", Project: "project"}, ErrObservationContentRequired},
		{"blank summary", FinalizeSessionParams{SessionID: "finalize", Project: "project", Summary: " \n\t"}, ErrObservationContentRequired},
		{"missing project", FinalizeSessionParams{SessionID: "finalize", Summary: "text"}, ErrProjectRequired},
		{"wrong project", FinalizeSessionParams{SessionID: "finalize", Project: "other", Summary: "text"}, ErrSessionOwnershipMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := finalizeFixture(t)
			before := captureFinalizeState(t, s)
			_, err := s.FinalizeSession(tc.p)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			assertFinalizeState(t, s, before)
		})
	}
}

func TestFinalizeSessionBareEndedAndExistingSave(t *testing.T) {
	s := finalizeFixture(t)
	id, err := s.AddObservation(AddObservationParams{SessionID: "finalize", Project: "project", Type: "session_summary", Title: "Session summary", Content: "Existing arbitrary summary"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EndSession("finalize", "Legacy free text"); err != nil {
		t.Fatal(err)
	}
	before := captureFinalizeState(t, s)
	_, err = s.FinalizeSession(FinalizeSessionParams{SessionID: "finalize", Project: "project", Summary: "New text"})
	if !errors.Is(err, ErrSessionAlreadyEnded) {
		t.Fatal(err)
	}
	assertFinalizeState(t, s, before)
	obs, err := s.GetObservation(id)
	if err != nil || obs.Content != "Existing arbitrary summary" {
		t.Fatalf("%+v %v", obs, err)
	}
}

func TestFinalizeSessionRollback(t *testing.T) {
	for _, stage := range []string{"INSERT INTO sync_mutations", "second sync mutation", "UPDATE sessions SET ended_at", "INSERT INTO session_finalizations"} {
		t.Run(stage, func(t *testing.T) {
			s := finalizeFixture(t)
			before := captureFinalizeState(t, s)
			injected := errors.New("injected failure")
			var inserts int
			s.hooks.exec = func(db execer, q string, args ...any) (sql.Result, error) {
				if strings.Contains(q, "INSERT INTO sync_mutations") {
					inserts++
				}
				if strings.Contains(q, stage) || (stage == "second sync mutation" && strings.Contains(q, "INSERT INTO sync_mutations") && inserts == 2) {
					return nil, injected
				}
				return db.Exec(q, args...)
			}
			_, err := s.FinalizeSession(FinalizeSessionParams{SessionID: "finalize", Project: "project", Summary: "text"})
			if !errors.Is(err, injected) {
				t.Fatalf("got %v", err)
			}
			assertFinalizeState(t, s, before)
		})
	}
}

func TestAddObservationFinalizeHelperCompatibility(t *testing.T) {
	s := finalizeFixture(t)
	p := AddObservationParams{SessionID: "finalize", Project: "project", Type: "decision", Title: "Decision", Content: "Public <private>secret</private>", TopicKey: "stable-topic"}
	id, err := s.AddObservation(p)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := s.GetObservation(id)
	if err != nil || obs.Content != "Public [REDACTED]" || obs.ReviewAfter == nil || obs.SyncID == "" || obs.RevisionCount != 1 {
		t.Fatalf("insert: %+v %v", obs, err)
	}
	p.Content = "Revised decision"
	revised, err := s.AddObservation(p)
	obs, readErr := s.GetObservation(id)
	if err != nil || readErr != nil || revised != id || obs.RevisionCount != 2 || obs.Content != p.Content {
		t.Fatalf("revision: %+v %v %v", obs, err, readErr)
	}
	p.TopicKey = ""
	p.Title = "Independent observation"
	p.Type = "discovery"
	first, err := s.AddObservation(p)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.AddObservation(p)
	obs, readErr = s.GetObservation(first)
	if err != nil || readErr != nil || duplicate != first || obs.DuplicateCount != 2 || obs.ReviewAfter != nil {
		t.Fatalf("dedupe: %+v %v %v", obs, err, readErr)
	}
}

func TestEndSessionFinalizeCompatibility(t *testing.T) {
	s := finalizeFixture(t)
	if err := s.EndSession("finalize", "Original free text"); err != nil {
		t.Fatal(err)
	}
	if err := s.EndSession("finalize", "Updated free text"); err != nil {
		t.Fatal(err)
	}
	sess, err := s.GetSession("finalize")
	if err != nil || sess.EndedAt == nil || sess.Summary == nil || *sess.Summary != "Updated free text" {
		t.Fatalf("legacy end: %+v %v", sess, err)
	}
	var markers int
	if err := s.DB().QueryRow("SELECT count(*) FROM session_finalizations").Scan(&markers); err != nil || markers != 0 {
		t.Fatalf("markers=%d %v", markers, err)
	}
	if err := s.EndSession("unknown", "Ignored"); err != nil {
		t.Fatal(err)
	}
}

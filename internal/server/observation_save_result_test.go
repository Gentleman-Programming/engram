package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestHandleGetObservationSaveResultReturnsCommittedID(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("sess-result", "proj-result", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	committedID, err := st.AddObservation(store.AddObservationParams{
		SessionID:   "sess-result",
		Type:        "manual",
		Title:       "Result title",
		Content:     "Result content.",
		Project:     "proj-result",
		Scope:       "project",
		OperationID: "op-server-lookup-1",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}

	srv := New(st, 0)
	req := httptest.NewRequest(http.MethodGet, "/observations/save-result?operation_id=op-server-lookup-1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if int64(body["id"].(float64)) != committedID {
		t.Fatalf("id = %v, want %d", body["id"], committedID)
	}
	if body["status"] != "committed" {
		t.Fatalf("status = %v, want committed", body["status"])
	}
}

func TestHandleGetObservationSaveResultRequiresOperationID(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	req := httptest.NewRequest(http.MethodGet, "/observations/save-result", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetObservationSaveResultReturnsNotFoundForUnknown(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	req := httptest.NewRequest(http.MethodGet, "/observations/save-result?operation_id=op-unknown", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetObservationSaveResultReturnsNotFoundForTombstonedOperation(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("sess-tombstone", "proj-tombstone", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	committedID, err := st.AddObservation(store.AddObservationParams{
		SessionID:   "sess-tombstone",
		Type:        "manual",
		Title:       "Tombstone title",
		Content:     "Tombstone content.",
		Project:     "proj-tombstone",
		Scope:       "project",
		OperationID: "op-server-tombstone-1",
	})
	if err != nil {
		t.Fatalf("AddObservation: %v", err)
	}
	if err := st.DeleteObservation(committedID, true); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}

	srv := New(st, 0)
	req := httptest.NewRequest(http.MethodGet, "/observations/save-result?operation_id=op-server-tombstone-1", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for tombstoned operation, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAddObservationAfterHardDeletion(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("sess-expired", "proj-expired", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := st.EnrollProject("proj-expired"); err != nil {
		t.Fatalf("EnrollProject: %v", err)
	}
	srv := New(st, 0)
	payload := `{"session_id":"sess-expired","type":"manual","title":"Expired title","content":"Original content.","project":"proj-expired","scope":"project","operation_id":"op-post-expired"}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(payload)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var saved struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode saved: %v", err)
	}
	if err := st.DeleteObservation(saved.ID, true); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}
	before, err := st.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
	if err != nil {
		t.Fatalf("ListPendingSyncMutations: %v", err)
	}
	writes := 0
	srv.SetOnWrite(func() { writes++ })
	for _, tc := range []struct {
		name    string
		payload string
		status  int
		err     error
	}{
		{"same payload", payload, http.StatusGone, store.ErrObservationOperationExpired},
		{"changed payload", strings.Replace(payload, "Original content.", "Changed content.", 1), http.StatusConflict, store.ErrObservationOperationConflict},
		{"same payload after conflict", payload, http.StatusGone, store.ErrObservationOperationExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(tc.payload)))
			if rec.Code != tc.status {
				t.Errorf("expected %d, got %d body=%s", tc.status, rec.Code, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if body["error"] != tc.err.Error() {
				t.Errorf("error = %q, want %q", body["error"], tc.err.Error())
			}
			stats, err := st.Stats()
			if err != nil {
				t.Fatalf("Stats: %v", err)
			}
			if stats.TotalObservations != 0 || writes != 0 {
				t.Errorf("rejected replay mutated state: observations=%d notifications=%d", stats.TotalObservations, writes)
			}
			after, err := st.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
			if err != nil {
				t.Fatalf("ListPendingSyncMutations: %v", err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Error("rejected replay changed sync mutations")
			}
		})
	}
}

func TestHandleAddObservationAcceptsOperationID(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("sess-op", "proj-op", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	srv := New(st, 0)
	payload := `{"session_id":"sess-op","type":"manual","title":"Op title","content":"Op content.","project":"proj-op","scope":"project","operation_id":"op-post-1"}`
	req := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Replay must return the same id.
	req2 := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(payload))
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusCreated {
		t.Fatalf("expected 201 on replay, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	var first, second map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if first["id"] != second["id"] {
		t.Fatalf("replay id changed: %v vs %v", first["id"], second["id"])
	}
}

func TestHandleAddObservationReturnsConflictForMismatchedReplay(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("sess-op", "proj-op", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	srv := New(st, 0)
	payload := `{"session_id":"sess-op","type":"manual","title":"Op title","content":"Op content.","project":"proj-op","scope":"project","operation_id":"op-conflict-post"}`
	req := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	mismatch := `{"session_id":"sess-op","type":"manual","title":"Op title","content":"Changed content.","project":"proj-op","scope":"project","operation_id":"op-conflict-post"}`
	req2 := httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(mismatch))
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestHandleAddObservationReplaySkipsOwnershipValidation(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("sess-replay-owner", "proj-original", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	srv := New(st, 0)
	payload := `{"session_id":"sess-replay-owner","type":"manual","title":"Owner title","content":"Owner content.","project":"proj-original","scope":"project","operation_id":"op-owner-replay"}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(payload)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first save: expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	var first struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}

	// Simulate a session ownership change after the original commit.
	if _, err := st.DB().Exec(`UPDATE sessions SET project = 'proj-new' WHERE id = 'sess-replay-owner'`); err != nil {
		t.Fatalf("change session project: %v", err)
	}

	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(payload)))
	if rec2.Code != http.StatusCreated {
		t.Fatalf("replay after ownership change: expected 201, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	var second struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("replay id changed: got %d, want %d", second.ID, first.ID)
	}
}

func TestHandleAddObservationNewOperationIDSessionProjectMismatch(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("sess-mismatch", "proj-a", "/tmp"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	before, err := st.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	srv := New(st, 0)
	payload := `{"session_id":"sess-mismatch","type":"manual","title":"Mismatch title","content":"Mismatch content.","project":"proj-b","scope":"project","operation_id":"op-mismatch"}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/observations", strings.NewReader(payload)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["code"] != "session_project_mismatch" {
		t.Fatalf("code = %v, want session_project_mismatch", body["code"])
	}

	after, err := st.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if after.TotalObservations != before.TotalObservations {
		t.Fatalf("mismatch saved observation despite 400: before=%d after=%d", before.TotalObservations, after.TotalObservations)
	}

	var ledgerCount int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM observation_save_operations WHERE operation_id = 'op-mismatch'`).Scan(&ledgerCount); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if ledgerCount != 0 {
		t.Fatalf("ledger has %d rows for rejected operation, want 0", ledgerCount)
	}
}

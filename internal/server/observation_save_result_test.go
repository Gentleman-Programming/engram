package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

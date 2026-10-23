package server

import (
	"encoding/json"
	"fmt"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"net/http/httptest"
	"testing"
)

func TestConflictsActionable(t *testing.T) {
	st, _ := conflictsTestStore(t)
	a, a2 := seedConflictsSession(t, st, "alpha")
	b, _ := seedConflictsSession(t, st, "beta")
	for _, p := range []store.SaveRelationParams{{SyncID: "same-evidence", SourceID: a, TargetID: a2}, {SyncID: "cross-evidence", SourceID: a, TargetID: b}} {
		if _, err := st.SaveRelation(p); err != nil {
			t.Fatal(err)
		}
	}
	items, err := st.ListRelations(store.ListRelationsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	for _, item := range items {
		if item.SyncID == "cross-evidence" {
			id = item.ID
		}
	}
	h := New(st, 0).Handler()
	get := func(path string) []byte {
		t.Helper()
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", path, r.Code, r.Body.String())
		}
		return r.Body.Bytes()
	}
	var queue struct {
		Total     int                      `json:"total"`
		Relations []store.RelationListItem `json:"relations"`
	}
	if err := json.Unmarshal(get("/conflicts?project=alpha"), &queue); err != nil {
		t.Fatal(err)
	}
	if queue.Total != 1 || len(queue.Relations) != 1 || queue.Relations[0].SyncID != "same-evidence" {
		t.Fatalf("queue: %+v", queue)
	}
	var stats store.RelationStats
	if err := json.Unmarshal(get("/conflicts/stats?project=alpha"), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.ByJudgmentStatus["pending"] != 1 {
		t.Fatalf("stats: %+v", stats)
	}
	var detail store.RelationListItem
	if err := json.Unmarshal(get(fmt.Sprintf("/conflicts/%d", id)), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.SyncID != "cross-evidence" {
		t.Fatalf("detail: %+v", detail)
	}
}

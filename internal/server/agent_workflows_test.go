package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestAgentWorkflow(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("workflow", "project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	srv := New(st, 0)
	writes := 0
	srv.SetOnWrite(func() { writes++ })
	post := func(path string, body any, status int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, bytes.NewReader(raw)))
		if rec.Code != status {
			t.Fatalf("%s: got %d want %d: %s", path, rec.Code, status, rec.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	ids := []int64{}
	for i := 0; i < 2; i++ {
		id, err := st.AddObservation(store.AddObservationParams{SessionID: "workflow", Project: "project", Type: "discovery", Title: fmt.Sprint("source", i), Content: fmt.Sprint("distinct source content ", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	snapshot := func() string {
		t.Helper()
		data, err := st.Export()
		if err != nil {
			t.Fatal(err)
		}
		data.ExportedAt = ""
		stats, err := st.Stats()
		if err != nil {
			t.Fatal(err)
		}
		seq, err := st.MaxPendingSyncMutationSeq("")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal([]any{data, stats, seq})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	before := snapshot()
	args := map[string]any{"ids_to_delete": ids, "new_title": "merged", "new_content": "combined", "expected_project": "project"}
	preview := post("/observations/consolidate", args, 200)
	if preview["status"] != "preview" || writes != 0 {
		t.Fatalf("preview wrote: %v / %d", preview, writes)
	}
	args["confirm"] = true
	args["fingerprint"] = "stale"
	refusal := post("/observations/consolidate", args, 409)
	if refusal["code"] != "fingerprint_mismatch" || refusal["plan"] == nil || writes != 0 {
		t.Fatalf("refusal: %v writes=%d", refusal, writes)
	}
	args["fingerprint"] = preview["plan"].(map[string]any)["fingerprint"]
	args["expected_project"] = "foreign"
	post("/observations/consolidate", args, 403)
	args["expected_project"] = "project"
	args["session_id"] = "override"
	post("/observations/consolidate", args, 400)
	delete(args, "session_id")
	if writes != 0 || snapshot() != before {
		t.Fatal("preview/refusal changed public data/counters")
	}
	result := post("/observations/consolidate", args, 201)
	if result["status"] != "consolidated" || len(result["soft_deleted_ids"].([]any)) != 2 || writes != 1 {
		t.Fatalf("confirm: %v writes=%d", result, writes)
	}
	before = snapshot()
	post("/conflicts/judge-many", map[string]any{"judgments": []any{}}, 400)
	post("/conflicts/judge-many", map[string]any{"judgments": []any{map[string]any{}}, "session_id": 42}, 400)
	batch := post("/conflicts/judge-many", map[string]any{"judgments": []any{map[string]any{"judgment_id": "missing", "relation": "related"}, map[string]any{"relation": false}}}, 200)
	for i, raw := range batch["outcomes"].([]any) {
		out := raw.(map[string]any)
		if out["index"] != float64(i) || out["status"] != "error" || out["error"] == nil {
			t.Fatalf("outcome: %v", out)
		}
	}
	post("/sessions/unknown/finalize", map[string]any{"project": "project", "summary": "summary"}, 404)
	post("/sessions/workflow/finalize", map[string]any{"project": "foreign", "summary": "summary"}, 403)
	post("/sessions/workflow/finalize", map[string]any{"summary": "summary"}, 400)
	if writes != 1 || snapshot() != before {
		t.Fatal("rejected workflows changed data/counters")
	}
	relationIDs := []int64{}
	for i := 0; i < 2; i++ {
		id, err := st.AddObservation(store.AddObservationParams{SessionID: "workflow", Project: "project", Type: "discovery", Title: fmt.Sprint("relation", i), Content: fmt.Sprint("relation content ", i)})
		if err != nil {
			t.Fatal(err)
		}
		relationIDs = append(relationIDs, id)
	}
	a, err := st.GetObservation(relationIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.GetObservation(relationIDs[1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveRelation(store.SaveRelationParams{SyncID: "rel-workflow", SourceID: a.SyncID, TargetID: b.SyncID}); err != nil {
		t.Fatal(err)
	}
	successful := post("/conflicts/judge-many", map[string]any{"session_id": "workflow", "judgments": []any{map[string]any{"judgment_id": "missing", "relation": "related"}, map[string]any{"judgment_id": "rel-workflow", "relation": "not_conflict", "confidence": 0.9}, map[string]any{"judgment_id": "rel-workflow", "relation": "bogus"}}}, 200)
	outcomes := successful["outcomes"].([]any)
	if outcomes[0].(map[string]any)["status"] != "error" || outcomes[1].(map[string]any)["judgment_status"] != "judged" || outcomes[2].(map[string]any)["status"] != "error" {
		t.Fatalf("partial ordered batch: %v", successful)
	}
	if writes != 2 {
		t.Fatal("successful batch must notify only its successful item")
	}
	final := post("/sessions/workflow/finalize", map[string]any{"project": "project", "summary": "structured summary"}, 200)
	before = snapshot()
	retry := post("/sessions/workflow/finalize", map[string]any{"project": "project", "summary": "different summary"}, 200)
	if final["state"] != "finalized" || retry["state"] != "already_finalized" || final["summary_id"] != retry["summary_id"] || final["summary_sync_id"] != retry["summary_sync_id"] || writes != 3 || snapshot() != before {
		t.Fatalf("finalize %v retry %v writes=%d", final, retry, writes)
	}
	if err := st.CreateSession("ended", "project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if err := st.EndSession("ended", "legacy"); err != nil {
		t.Fatal(err)
	}
	post("/sessions/ended/finalize", map[string]any{"project": "project", "summary": "summary"}, 409)
}

func TestAgentWorkflowAuth(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", "test-workflow-token")
	srv := New(newServerTestStore(t), 0)
	for _, path := range []string{"/observations/consolidate", "/conflicts/judge-many", "/sessions/exact/finalize"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, bytes.NewBufferString(`{}`)))
		if rec.Code != 401 {
			t.Fatalf("%s bypassed mutation auth: %d", path, rec.Code)
		}
	}
}

package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

type workflowReviewFixture struct {
	store  *store.Store
	server *Server
	ids    []int64
	writes int
}

func newWorkflowReviewFixture(t *testing.T) *workflowReviewFixture {
	t.Helper()
	t.Setenv("ENGRAM_HTTP_TOKEN", "")
	f := &workflowReviewFixture{store: newServerTestStore(t)}
	if err := f.store.CreateSession("review", "project", "/tmp"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		id, err := f.store.AddObservation(store.AddObservationParams{SessionID: "review", Project: "project", Type: "discovery", Title: fmt.Sprint("source", i), Content: fmt.Sprint("review source ", i)})
		if err != nil {
			t.Fatal(err)
		}
		f.ids = append(f.ids, id)
	}
	a, err := f.store.GetObservation(f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.store.GetObservation(f.ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveRelation(store.SaveRelationParams{SyncID: "rel-review", SourceID: a.SyncID, TargetID: b.SyncID}); err != nil {
		t.Fatal(err)
	}
	f.server = New(f.store, 0)
	f.server.SetOnWrite(func() { f.writes++ })
	return f
}

func (f *workflowReviewFixture) snapshot(t *testing.T) string {
	t.Helper()
	data, err := f.store.Export()
	if err != nil {
		t.Fatal(err)
	}
	data.ExportedAt = ""
	stats, err := f.store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	seq, err := f.store.MaxPendingSyncMutationSeq("")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]any{data, stats, seq})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (f *workflowReviewFixture) post(t *testing.T, path, body string, status int, want map[string]any) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	if rec.Code != status {
		t.Errorf("%s: status=%d want=%d body=%s", path, rec.Code, status, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("JSON response: %v: %s", err, rec.Body.String())
	}
	if want != nil && !reflect.DeepEqual(got, want) {
		t.Errorf("%s: response=%v want=%v", path, got, want)
	}
	return got
}

func (f *workflowReviewFixture) unchanged(t *testing.T, before string) {
	t.Helper()
	if f.writes != 0 || f.snapshot(t) != before {
		t.Error("rejected request changed public data, counters, sync sequence or write notifications")
	}
}

func (f *workflowReviewFixture) consolidate() string {
	return fmt.Sprintf(`{"ids_to_delete":[%d,%d],"new_title":"merged","new_content":"summary","expected_project":"project"}`, f.ids[0], f.ids[1])
}

func TestAgentWorkflowValidationRoutes(t *testing.T) {
	f := newWorkflowReviewFixture(t)
	before := f.snapshot(t)
	for _, path := range []string{"/observations/consolidate", "/conflicts/judge-many", "/sessions/review/finalize"} {
		for _, body := range []string{"", "null", "[]", "{} {}"} {
			message := "body must be a JSON object"
			if body == "{} {}" {
				message = "body must contain exactly one JSON object"
			}
			f.post(t, path, body, 400, map[string]any{"error": message, "code": "invalid_arguments"})
			f.unchanged(t, before)
		}
	}
	f.post(t, "/sessions/review/finalize", `{"project":"project","summary":"summary","unsupported":true}`, 400, map[string]any{"error": "unsupported argument unsupported", "code": "invalid_arguments"})
	f.unchanged(t, before)
	f.post(t, "/observations/consolidate", `{"ids_to_delete":[1],"new_title":"merged","new_content":"summary","expected_project":"project"}`, 400, map[string]any{"error": "ids_to_delete must be an array of at least two positive integer IDs", "code": "invalid_arguments"})
	f.unchanged(t, before)
	item := `{"judgment_id":"rel-review","relation":"not_conflict","reason":"different contexts"}`
	f.post(t, "/conflicts/judge-many", `{"judgments":[`+strings.TrimSuffix(strings.Repeat(item+",", 33), ",")+`]}`, 400, map[string]any{"error": "judgments must be an array containing 1..32 items"})
	f.unchanged(t, before)
}

func TestAgentWorkflowBodyLimit(t *testing.T) {
	const limit = 1 << 20
	for _, route := range []string{"consolidate", "judge", "finalize"} {
		for _, position := range []string{"first value", "trailing value", "trailing whitespace", "exact limit"} {
			t.Run(route+"/"+position, func(t *testing.T) {
				f := newWorkflowReviewFixture(t)
				path, body := "/observations/consolidate", f.consolidate()
				if route == "judge" {
					path, body = "/conflicts/judge-many", `{"judgments":[{"judgment_id":"rel-review","relation":"not_conflict"}]}`
				}
				if route == "finalize" {
					path, body = "/sessions/review/finalize", `{"project":"project","summary":"summary"}`
				}
				before := f.snapshot(t)
				status := 413
				want := map[string]any{"error": "body exceeds 1048576-byte limit", "code": "request_too_large"}
				switch position {
				case "first value":
					body = `{"padding":"` + strings.Repeat("x", limit) + `"}`
				case "trailing value":
					body += ` {"padding":"` + strings.Repeat("x", limit) + `"}`
				case "trailing whitespace":
					body += strings.Repeat(" ", limit+1-len(body))
				case "exact limit":
					body += strings.Repeat(" ", limit-len(body))
					status, want = 200, nil
				}
				got := f.post(t, path, body, status, want)
				if position != "exact limit" || route == "consolidate" {
					f.unchanged(t, before)
				}
				if position == "exact limit" {
					switch route {
					case "consolidate":
						if got["status"] != "preview" || got["plan"] == nil {
							t.Errorf("preview=%v", got)
						}
					case "judge":
						outcomes, ok := got["outcomes"].([]any)
						if !ok || len(outcomes) != 1 || outcomes[0].(map[string]any)["status"] != "success" || f.writes != 1 || f.snapshot(t) == before {
							t.Errorf("exact-limit judgment=%v writes=%d", got, f.writes)
						}
					case "finalize":
						if got["state"] != "finalized" || got["session_id"] != "review" || got["summary_id"] == nil || f.writes != 1 || f.snapshot(t) == before {
							t.Errorf("exact-limit finalization=%v writes=%d", got, f.writes)
						}
					}
				}
			})
		}
	}
}

func TestAgentWorkflowInfrastructureErrors(t *testing.T) {
	for _, route := range []string{"consolidate", "finalize"} {
		t.Run(route, func(t *testing.T) {
			f := newWorkflowReviewFixture(t)
			before, directory := f.snapshot(t), f.store.DataDir()
			path, body, code := "/observations/consolidate", f.consolidate(), "consolidation_failed"
			message := "consolidation failed"
			if route == "finalize" {
				path, body, code = "/sessions/review/finalize", `{"project":"project","summary":"summary"}`, "session_finalize_failed"
				message = "session finalization failed"
			}
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"error": message, "code": code}
			if route == "finalize" {
				want["session_id"] = "review"
			}
			f.post(t, path, body, 500, want)
			reopened, err := store.New(store.FallbackConfig(directory))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			f.store = reopened
			f.unchanged(t, before)
		})
	}
}

func TestAgentWorkflowDomainRefusals(t *testing.T) {
	f := newWorkflowReviewFixture(t)
	before := f.snapshot(t)
	for _, ids := range [][]int64{{f.ids[0], f.ids[0]}, {f.ids[0], 999999}} {
		message := store.ErrConsolidateMissingObservation.Error()
		if ids[0] == ids[1] {
			message = store.ErrConsolidateDuplicateID.Error()
		}
		body := fmt.Sprintf(`{"ids_to_delete":[%d,%d],"new_title":"merged","new_content":"summary","expected_project":"project"}`, ids[0], ids[1])
		f.post(t, "/observations/consolidate", body, 400, map[string]any{"error": message, "code": "consolidation_refused"})
		f.unchanged(t, before)
	}
	f.post(t, "/sessions/%20/finalize", `{"project":"project","summary":"summary"}`, 400, map[string]any{"error": store.ErrSessionIDRequired.Error(), "code": "session_finalize_failed", "session_id": " "})
	f.unchanged(t, before)
	f.post(t, "/sessions/review/finalize", `{"project":"project","summary":"   "}`, 400, map[string]any{"error": "project and summary must be non-empty strings", "code": "invalid_arguments"})
	f.unchanged(t, before)
}

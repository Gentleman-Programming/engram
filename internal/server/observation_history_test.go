package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestObservationHistoryHTTPInputs(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("history-http", "history-owner", ""); err != nil {
		t.Fatal(err)
	}
	var id int64
	for i := 0; i <= 3; i++ {
		var err error
		id, err = st.AddObservation(store.AddObservationParams{SessionID: "history-http", Project: "history-owner", Scope: "project", Type: "note", Title: "History", Content: fmt.Sprintf("state-%d", i), TopicKey: "history/http"})
		if err != nil {
			t.Fatal(err)
		}
	}
	h := New(st, 0).Handler()
	get := func(path string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("JSON: %s: %v", rec.Body.String(), err)
		}
		return rec.Code, body
	}
	path := fmt.Sprintf("/observations/%d", id)
	status, baseline := get(path)
	if status != 200 {
		t.Fatalf("baseline: %d %#v", status, baseline)
	}
	statsStatus, statsBefore := get("/stats")
	if statsStatus != 200 {
		t.Fatalf("stats: %d %#v", statsStatus, statsBefore)
	}
	for _, include := range []string{"", "false"} {
		query := url.Values{"history_cursor": {"invalid"}, "project": {"different-project"}}
		if include != "" {
			query.Set("include_history", include)
		}
		status, body := get(path + "?" + query.Encode())
		if status != 200 || !reflect.DeepEqual(body, baseline) {
			t.Fatalf("legacy response changed: %d %#v", status, body)
		}
	}
	for _, tc := range []struct {
		cursor  string
		wantLen int
	}{
		{"0", 3}, {"0e0", 3}, {"3.0", 2}, {"3e0", 2}, {"1", 0}, {"9223372036854774784", 3},
	} {
		t.Run("valid "+tc.cursor, func(t *testing.T) {
			status, body := get(path + "?include_history=true&history_cursor=" + url.QueryEscape(tc.cursor))
			entries, ok := body["history"].([]any)
			if status != 200 || !ok || len(entries) != tc.wantLen || body["version_count"] != float64(3) {
				t.Fatalf("page: %d %#v", status, body)
			}
			for key, value := range baseline {
				if !reflect.DeepEqual(body[key], value) {
					t.Fatalf("current %s changed", key)
				}
			}
			for i, raw := range entries {
				entry, ok := raw.(map[string]any)
				if !ok || entry["version"] != float64(i+1) || entry["content"] != fmt.Sprintf("state-%d", i) || entry["title"] != "History" {
					t.Fatalf("entry: %#v", raw)
				}
			}
			for _, key := range []string{"history_truncated", "history_from_version", "history_cursor"} {
				if _, ok := body[key]; ok {
					t.Fatalf("terminal key %s present", key)
				}
			}
		})
	}
	for _, tc := range []struct {
		name, target, message string
		status                int
	}{
		{"empty", path + "?include_history=true&history_cursor=", "history_cursor must be a non-negative integer", 400},
		{"fraction", path + "?include_history=true&history_cursor=1.5", "history_cursor must be a non-negative integer", 400},
		{"negative", path + "?include_history=true&history_cursor=-1", "history_cursor must be a non-negative integer", 400},
		{"text", path + "?include_history=true&history_cursor=no", "history_cursor must be a non-negative integer", 400},
		{"boundary", path + "?include_history=true&history_cursor=9223372036854775808", "history_cursor must be a non-negative integer", 400},
		{"overflow", path + "?include_history=true&history_cursor=1e309", "history_cursor must be a non-negative integer", 400},
		{"invalid ID first", "/observations/no?include_history=true&history_cursor=-1", "invalid observation id", 400},
		{"not found first", "/observations/999999?include_history=true&history_cursor=-1", "observation not found", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beforeStatus, before := get(path)
			statsStatus, statsBefore := get("/stats")
			if beforeStatus != 200 || statsStatus != 200 {
				t.Fatal("public pre-request snapshot failed")
			}
			status, body := get(tc.target)
			afterStatus, after := get(path)
			statsStatus, statsAfter := get("/stats")
			if afterStatus != 200 || statsStatus != 200 || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(statsBefore, statsAfter) {
				t.Fatal("rejected read mutated current record or counters")
			}
			if status != tc.status || body["error"] != tc.message {
				t.Fatalf("error: %d %#v want %d %q", status, body, tc.status, tc.message)
			}
		})
	}
	_, after := get(path)
	_, statsAfter := get("/stats")
	if !reflect.DeepEqual(baseline, after) || !reflect.DeepEqual(statsBefore, statsAfter) {
		t.Fatal("reads mutated current record or counters")
	}
}

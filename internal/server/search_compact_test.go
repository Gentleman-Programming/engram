package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestSearchCompactHTTPContract(t *testing.T) {
	st := newServerTestStore(t)
	if err := st.CreateSession("compact-http", "compact-http", ""); err != nil {
		t.Fatal(err)
	}
	content := "aurora " + strings.Repeat("世界", 160)
	id, err := st.AddObservation(store.AddObservationParams{SessionID: "compact-http", Project: "compact-http", Scope: "project", Type: "decision", Title: "Aurora", Content: content, TopicKey: "decision/aurora"})
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, 0).Handler()
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	for _, suffix := range []string{"", "&response_format="} {
		rec := get("/search?q=aurora&project=compact-http" + suffix)
		var entries []map[string]any
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &entries) != nil || len(entries) != 1 || entries[0]["content"] != content {
			t.Fatalf("legacy response: %d %s", rec.Code, rec.Body.String())
		}
	}
	t.Run("compact", func(t *testing.T) {
		rec := get("/search?q=aurora&project=compact-http&response_format=compact")
		var body struct {
			Result  string           `json:"result"`
			Results []map[string]any `json:"results"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Result != "Found 1 memories." || len(body.Results) != 1 {
			t.Fatalf("compact envelope: %d %s", rec.Code, rec.Body.String())
		}
		entry := body.Results[0]
		preview, _ := entry["preview"].(string)
		if utf8.RuneCountInString(preview) != 300 || !utf8.ValidString(preview) || entry["truncated"] != true || entry["id"] != float64(id) || entry["topic_key"] != "decision/aurora" {
			t.Fatalf("bounded projection: %#v", entry)
		}
		for _, key := range []string{"id", "sync_id", "title", "type", "state", "scope", "pinned", "project"} {
			if _, ok := entry[key]; !ok {
				t.Errorf("missing %s", key)
			}
		}
		if _, ok := entry["content"]; ok {
			t.Fatal("compact result leaked full content")
		}
		var envelope map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if _, ok := envelope["project"]; ok {
			t.Fatal("HTTP must not add outer project")
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/search?q=aurora&project=compact-http&response_format=compact", nil).WithContext(ctx)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "context canceled") {
			t.Fatalf("cancellation became success: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("empty", func(t *testing.T) {
		rec := get("/search?q=absenttoken&project=compact-http&response_format=compact")
		var body struct {
			Result  string `json:"result"`
			Results []any  `json:"results"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Result != `No memories found for: "absenttoken"` || body.Results == nil || len(body.Results) != 0 {
			t.Fatalf("empty envelope: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("invalid before project resolution and no mutation", func(t *testing.T) {
		observationPath := fmt.Sprintf("/observations/%d", id)
		before := get(observationPath).Body.String()
		statsBefore := get("/stats").Body.String()
		for _, format := range []string{"full", "COMPACT"} {
			rec := get("/search?q=aurora&project=missing-project&response_format=" + format)
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			want := `invalid response_format "` + format + `": must be "compact"`
			if rec.Code != 400 || body["error"] != want {
				t.Fatalf("invalid format: %d %s", rec.Code, rec.Body.String())
			}
		}
		after := get(observationPath).Body.String()
		if before != after {
			t.Fatalf("rejected search mutated observation: before=%s after=%s", before, after)
		}
		if statsAfter := get("/stats").Body.String(); statsBefore != statsAfter {
			t.Fatalf("rejected search mutated counters: before=%s after=%s", statsBefore, statsAfter)
		}
	})
}

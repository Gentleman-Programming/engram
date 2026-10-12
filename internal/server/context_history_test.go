package server

import (
	"encoding/json"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestContextHistoryHTTPRealRendering(t *testing.T) {
	s, err := store.New(store.Config{DataDir: t.TempDir(), MaxContextResults: 20})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateSession("real-history", "alpha", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		project := "alpha"
		if i%2 == 0 {
			project = "beta"
		}
		if _, err := s.DB().Exec(`INSERT INTO observations(session_id,type,title,content,project,scope,created_at) VALUES('real-history','decision','past','body',?,'personal','2022-01-01')`, project); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	call := func(query string) string {
		t.Helper()
		w := httptest.NewRecorder()
		New(s, 0).Handler().ServeHTTP(w, httptest.NewRequest("GET", "/context?all_projects=true&scope=personal"+query, nil))
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		return body["context"]
	}
	absent := call("")
	disabled := call("&older_history=false")
	legacy, err := s.FormatContext("", "personal")
	if err != nil {
		t.Fatal(err)
	}
	if absent != disabled || absent != legacy || strings.Contains(absent, "Older History") {
		t.Fatal("disabled output changed")
	}
	enabled := call("&older_history=true")
	if !strings.Contains(enabled, "2022: 5 observations") {
		t.Fatal(enabled)
	}
	after, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	before.ExportedAt = ""
	after.ExportedAt = ""
	if !reflect.DeepEqual(before, after) {
		t.Fatal("context mutated store")
	}
}

func TestContextHistoryForwarding(t *testing.T) {
	cfg := store.Config{DataDir: t.TempDir()}
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateSession("history", "engram", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	old := contextHistoryFormatter
	t.Cleanup(func() { contextHistoryFormatter = old })
	for _, tc := range []struct {
		query string
		want  bool
	}{{"", false}, {"&older_history=false", false}, {"&older_history=true", true}, {"&older_history=invalid", false}} {
		t.Run(tc.query, func(t *testing.T) {
			called := false
			contextHistoryFormatter = func(_ *store.Store, project, scope string, opts store.ContextOptions) (string, error) {
				called = true
				if project != "engram" || scope != "personal" || opts.OlderHistory != tc.want || opts.MaxBytes != 1024 || !opts.Compact || opts.Observations != -1 {
					t.Errorf("forwarded %q %q %+v", project, scope, opts)
				}
				return "fixture", nil
			}
			w := httptest.NewRecorder()
			New(s, 0).Handler().ServeHTTP(w, httptest.NewRequest("GET", "/context?project=engram&scope=personal&max_bytes=1024&compact=true&observations=-1"+tc.query, nil))
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if !called || w.Code != 200 || body["context"] != "fixture" {
				t.Fatalf("status=%d body=%s called=%v", w.Code, w.Body.String(), called)
			}
		})
	}
}

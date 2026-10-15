package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Exercise only the HTTP interface so this regression also runs before the
// endpoint's Go types exist. A matching stale projection must not hide new tools.
func TestRuntimeContractCurrentAgentSurface(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", "current-contract-test-token")
	st := newServerTestStore(t)
	srv := New(st, 0)
	t.Cleanup(func() { _ = srv.Close() })
	writes := 0
	srv.SetOnWrite(func() { writes++ })
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	beforeStats := request("GET", "/stats?all_projects=true", "")
	beforeRows := request("GET", "/observations?all_projects=true&limit=100", "")
	if beforeStats.Code != 200 || beforeRows.Code != 200 {
		t.Fatal("public baseline unavailable")
	}
	w := request("GET", "/runtime-contract", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /runtime-contract = %d, want 200: %s", w.Code, w.Body.String())
	}
	var contract struct {
		Operations map[string]struct {
			Kind     string `json:"kind"`
			Bindings []struct {
				Method         string `json:"method"`
				Path           string `json:"path"`
				ServerContract string `json:"server_contract"`
				ConfiguredAuth bool   `json:"configured_auth"`
			} `json:"bindings"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	if len(contract.Operations) != 22 {
		t.Errorf("default Pi operations = %d, want 22", len(contract.Operations))
	}
	for _, name := range []string{"mem_delete", "mem_stats", "mem_timeline"} {
		if _, exists := contract.Operations[name]; exists {
			t.Errorf("admin-only %s advertised in the default Pi projection", name)
		}
	}
	for _, tc := range []struct {
		name, path, id string
		auth           bool
	}{
		{"mem_suggest_topic_key", "/topic-keys/suggest", "topic-key-suggest", false},
		{"mem_consolidate", "/observations/consolidate", "observation-consolidate", true},
		{"mem_judge_many", "/conflicts/judge-many", "conflict-judge-many", true},
		{"mem_session_finalize", "/sessions/{id}/finalize", "session-finalize", true},
	} {
		op := contract.Operations[tc.name]
		if op.Kind != "http" || len(op.Bindings) != 1 {
			t.Errorf("%s binding = %+v, want one HTTP handler", tc.name, op)
			continue
		}
		binding := op.Bindings[0]
		if binding.Method != "POST" || binding.Path != tc.path || binding.ServerContract != tc.id+"/v1" || binding.ConfiguredAuth != tc.auth {
			t.Errorf("%s descriptor = %+v", tc.name, binding)
		}
	}
	for _, path := range []string{"/observations/consolidate", "/conflicts/judge-many", "/sessions/missing/finalize"} {
		denied := request("POST", path, "{}")
		if denied.Code != 401 || denied.Header().Get("WWW-Authenticate") != `Bearer realm="engram"` || denied.Body.String() != "{\"error\":\"authorization required\"}\n" {
			t.Errorf("%s lost its configured-token guard: status=%d headers=%v body=%s", path, denied.Code, denied.Header(), denied.Body.String())
		}
	}
	// Admin handlers still exist with their original auth; only their default
	// Pi operation advertisements are removed.
	if denied := request("DELETE", "/observations/1", ""); denied.Code != 401 {
		t.Errorf("existing admin handler lost authentication: %d %s", denied.Code, denied.Body.String())
	}
	topic := request("POST", "/topic-keys/suggest", `{"type":"architecture","title":"Auth model"}`)
	if topic.Code != 200 || topic.Body.String() != "{\"topic_key\":\"architecture/auth-model\"}\n" {
		t.Errorf("Go topic suggestion changed: %d %s", topic.Code, topic.Body.String())
	}
	for _, tc := range []struct {
		path   string
		before *httptest.ResponseRecorder
	}{
		{"/stats?all_projects=true", beforeStats},
		{"/observations?all_projects=true&limit=100", beforeRows},
	} {
		after := request("GET", tc.path, "")
		if after.Code != tc.before.Code || !reflect.DeepEqual(after.Body.Bytes(), tc.before.Body.Bytes()) {
			t.Errorf("public state changed at %s: before=%s after=%s", tc.path, tc.before.Body.String(), after.Body.String())
		}
	}
	if writes != 0 {
		t.Errorf("metadata/refused/topic requests emitted %d writes", writes)
	}
}

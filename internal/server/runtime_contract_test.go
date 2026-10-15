package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"testing"
)

// The runtime contract is a read-only public HTTP contract, not MCP parity.
func TestRuntimeContractEndpoint(t *testing.T) {
	st := newServerTestStore(t)
	srv := New(st, 0)
	get := func(path string) (int, map[string]json.RawMessage) {
		t.Helper()
		r := httptest.NewRecorder()
		srv.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		var body map[string]json.RawMessage
		if r.Code == http.StatusOK {
			if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
				t.Fatalf("GET %s JSON: %v", path, err)
			}
		}
		return r.Code, body
	}
	code, before := get("/stats?all_projects=true")
	if code != http.StatusOK {
		t.Fatalf("baseline stats status = %d", code)
	}
	code, body := get("/runtime-contract")
	if code != http.StatusOK {
		t.Fatalf("runtime contract status = %d, want 200", code)
	}
	for key, want := range map[string]string{
		"contract_id": "engram.pi-http-runtime/v1",
		"status":      "pi-http-server-contract",
		"instance_id": st.InstanceID(),
	} {
		var got string
		if err := json.Unmarshal(body[key], &got); err != nil || got != want {
			t.Errorf("%s = %s, want %q (error %v)", key, body[key], want, err)
		}
	}
	var revision int
	if err := json.Unmarshal(body["revision"], &revision); err != nil || revision != 1 {
		t.Errorf("revision = %s, want 1", body["revision"])
	}
	var digest string
	if err := json.Unmarshal(body["digest"], &digest); err != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(digest) {
		t.Errorf("digest = %s, want SHA-256", body["digest"])
	}
	var operations map[string]json.RawMessage
	if err := json.Unmarshal(body["operations"], &operations); err != nil {
		t.Fatalf("operations: %v", err)
	}
	names := []string{
		"mem_search", "mem_context", "mem_consolidate", "mem_judge_many", "mem_get_observation",
		"mem_save", "mem_session_summary", "mem_update", "mem_session_finalize", "mem_save_prompt",
		"mem_session_start", "mem_session_end", "mem_current_project", "mem_doctor",
		"mem_capture_passive", "mem_review", "mem_judge", "mem_compare", "mem_list_projects",
		"mem_pin", "mem_unpin", "mem_suggest_topic_key",
	}
	// mem_session_summary and mem_save share a route but remain distinct operations.
	// Count against the actual names rather than an assumed MCP profile size.
	if len(operations) != len(names) {
		t.Errorf("operations count = %d, want %d", len(operations), len(names))
	}
	for _, name := range names {
		if len(operations[name]) == 0 {
			t.Errorf("missing operation %s", name)
		}
	}
	code, again := get("/runtime-contract")
	if code != http.StatusOK || !reflect.DeepEqual(body, again) {
		t.Error("repeated contract request changed the stable projection or identity")
	}
	code, after := get("/stats?all_projects=true")
	if code != http.StatusOK || !reflect.DeepEqual(before, after) {
		t.Errorf("contract request changed public stats: before=%v after=%v", before, after)
	}
}

func TestRuntimeContractAuthAndMethod(t *testing.T) {
	t.Setenv("ENGRAM_HTTP_TOKEN", "configured-runtime-test-token")
	st := newServerTestStore(t)
	srv := New(st, 0)
	request := func(server *Server, method, path, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	// Read-only metadata retains the existing shared-server health policy.
	for _, path := range []string{"/health", "/runtime-contract"} {
		if w := request(srv, "GET", path, ""); w.Code != 200 {
			t.Errorf("shared %s status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
	if w := request(srv, "DELETE", "/observations/1", ""); w.Code != 401 {
		t.Errorf("existing destructive route lost auth: %d %s", w.Code, w.Body.String())
	}
	if w := request(srv, "POST", "/runtime-contract", ""); w.Code != 405 {
		t.Errorf("unsupported contract method=%d body=%s", w.Code, w.Body.String())
	}
	owned := New(st, 0)
	owned.SetSessionOwnedToken("owned-runtime-test-token")
	for _, token := range []string{"", "incorrect-runtime-test-token"} {
		w := request(owned, "GET", "/runtime-contract", token)
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") != `Bearer realm="engram"` {
			t.Errorf("owned qualification auth=%d headers=%v", w.Code, w.Header())
		}
	}
	if w := request(owned, "GET", "/runtime-contract", "owned-runtime-test-token"); w.Code != 200 {
		t.Errorf("owned qualification status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestRuntimeContractDigestAndIdentity(t *testing.T) {
	get := func(srv *Server) (RuntimeContract, string) {
		t.Helper()
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/runtime-contract", nil))
		if w.Code != 200 {
			t.Fatalf("contract status=%d body=%s", w.Code, w.Body.String())
		}
		var body struct {
			RuntimeContract
			InstanceID string `json:"instance_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.RuntimeContract, body.InstanceID
	}
	first := New(newServerTestStore(t), 0)
	contract, identity := get(first)
	first.SetVersion("unrelated-version-string")
	afterVersion, afterIdentity := get(first)
	if !reflect.DeepEqual(contract, afterVersion) || identity != afterIdentity {
		t.Fatal("version string changed runtime qualification")
	}
	other, otherIdentity := get(New(newServerTestStore(t), 0))
	if !reflect.DeepEqual(contract, other) || identity == "" || otherIdentity == "" || identity == otherIdentity {
		t.Fatal("dynamic store identity leaked into stable contract or was absent")
	}
	topic := contract.Operations["mem_suggest_topic_key"]
	if topic.Kind != "http" || len(topic.Bindings) != 1 || topic.Bindings[0].Path != "/topic-keys/suggest" {
		t.Errorf("Go topic suggestion must declare its real HTTP binding: %+v", topic)
	}
	digest := contract.Digest
	contract.Digest = ""
	unsigned, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(unsigned)
	if got := hex.EncodeToString(sum[:]); got != digest {
		t.Errorf("public stable projection digest=%s want=%s", got, digest)
	}
}

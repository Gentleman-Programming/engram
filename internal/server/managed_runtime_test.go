package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestManagedRuntimeOwnedAPI(t *testing.T) {
	st := newServerTestStore(t)
	dir := t.TempDir()
	mode := store.SessionOwnershipProjectOwned
	if err := st.CreateSessionWithOwnershipMode("root", "project", dir, mode); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"root": "root", "effective_id": "root", "directory": dir, "project": "project", "ownership_mode": mode}
	call := func(s *Server, token, path string, values map[string]any, want int) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(data)))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s got %d %s, want %d", path, w.Code, w.Body, want)
		}
		return w
	}
	const prefix = "/internal/managed-runtime/"
	const token = "owned-test-token"
	shared, owned := New(st, 0), New(st, 0)
	owned.SetSessionOwnedToken(token)
	call(shared, token, prefix+"admit", body, 404)
	call(owned, "invalid", prefix+"admit", body, 401)
	resp := call(owned, token, prefix+"admit", body, 200)
	var admitted struct {
		Generation int64 `json:"generation"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &admitted); err != nil || admitted.Generation != 1 {
		t.Fatalf("bad admission: %s", resp.Body)
	}
	body["generation"] = admitted.Generation
	call(owned, token, prefix+"write-intent", body, 400)
	body["operation_id"] = "managed-save"
	call(owned, token, prefix+"write-intent", body, 200)
	delete(body, "operation_id")
	pending := call(owned, token, prefix+"close", body, 409)
	if !strings.Contains(pending.Body.String(), "managed_runtime_write_unresolved") {
		t.Fatal(pending.Body)
	}
	body["operation_id"] = "unrecorded"
	body["observation"] = map[string]any{"title": "Managed observation", "content": "content"}
	call(owned, token, prefix+"save", body, 409)
	body["operation_id"] = "managed-save"
	call(owned, token, prefix+"save", body, 201)
	delete(body, "operation_id")
	delete(body, "observation")
	call(owned, token, prefix+"close", body, 200)
	call(owned, token, prefix+"close", body, 200)
	row, err := st.GetSession("root")
	if err != nil || row.EndedAt == nil {
		t.Fatalf("managed terminal session: %+v %v", row, err)
	}
	body["project"] = "other"
	call(owned, token, prefix+"close", body, 404)
}

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestManagedObservationUpdateOwnerAndExactHost(t *testing.T) {
	st := newServerTestStore(t)
	const token = "managed-edit-owner"
	owned, shared := New(st, 0), New(st, 0)
	owned.SetSessionOwnedToken(token)
	call := func(server *Server, bearer, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/internal/managed-runtime/update", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("managed update: got %d %s, want %d", w.Code, w.Body, want)
		}
		return w
	}
	scope := store.ManagedRuntimeScope{Root: "root", Project: "project", Directory: t.TempDir(), Mode: store.SessionOwnershipProjectOwned}
	host := store.ManagedRuntimeHost{PID: 12, Start: "fixture-start", Domain: "fixture-domain"}
	id, generation, err := st.AttachManagedRuntime(scope, host, false)
	if err != nil {
		t.Fatal(err)
	}
	scope.EffectiveID = id
	observation, err := st.AddObservation(store.AddObservationParams{SessionID: id, Project: scope.Project, Title: "Before", Content: "original"})
	if err != nil {
		t.Fatal(err)
	}
	payload := func(host store.ManagedRuntimeHost, project, change string) string {
		t.Helper()
		data, err := json.Marshal(map[string]any{
			"scope": scope, "host": host, "operation_id": "edit",
			"update": map[string]any{"id": observation, "expected_project": project, "changes": map[string]string{"title": change}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	valid := payload(host, scope.Project, "After")
	call(shared, token, valid, http.StatusNotFound)
	call(owned, "wrong", valid, http.StatusUnauthorized)
	first := call(owned, token, valid, http.StatusOK)
	if !strings.Contains(first.Body.String(), `"replayed":false`) {
		t.Fatalf("first managed receipt: %s", first.Body)
	}
	call(owned, token, valid+" {}", http.StatusBadRequest)
	call(owned, token, strings.Replace(valid, `"title":"After"`, "", 1), http.StatusBadRequest)
	call(owned, token, payload(host, "", "Missing project"), http.StatusBadRequest)
	imposter := host
	imposter.Start = "other-incarnation"
	call(owned, token, payload(imposter, scope.Project, "Wrong host"), http.StatusConflict)
	call(owned, token, payload(host, "other", "Wrong project"), http.StatusNotFound)
	call(owned, token, payload(host, scope.Project, "Same key, changed edit"), http.StatusConflict)
	retry := call(owned, token, valid, http.StatusOK)
	if !strings.Contains(retry.Body.String(), `"replayed":true`) {
		t.Fatalf("replayed managed receipt: %s", retry.Body)
	}
	if err := st.RetireManagedRuntimeHost(scope, generation, host); err != nil {
		t.Fatal(err)
	}
	call(owned, token, valid, http.StatusConflict)
	obs, err := st.GetObservation(observation)
	if err != nil || obs.Title != "After" || obs.RevisionCount != 2 {
		t.Fatalf("unexpected observation after retries: %+v %v", obs, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	call(owned, token, valid, http.StatusInternalServerError)
}

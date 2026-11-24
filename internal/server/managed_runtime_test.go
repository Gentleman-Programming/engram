package server

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/hostbind"
	"github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestManagedRuntimeScopeInventory(t *testing.T) {
	st := newServerTestStore(t)
	const token = "scope-inventory-owner"
	const path = "/internal/managed-runtime/scopes"
	shared, owned := New(st, 0), New(st, 0)
	owned.SetSessionOwnedToken(token)
	get := func(s *Server, bearer, target string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("GET %s got %d %s, want %d", target, w.Code, w.Body, want)
		}
		return w.Body.Bytes()
	}
	get(shared, token, path, http.StatusNotFound)
	get(owned, "wrong-token", path, http.StatusUnauthorized)
	get(owned, token, path+"?project=other", http.StatusBadRequest)
	if body := get(owned, token, path, http.StatusOK); strings.TrimSpace(string(body)) != `{"scopes":[]}` {
		t.Fatalf("fresh inventory must return an empty array: %s", body)
	}

	dir := project.RuntimeWorktreeDirectory(t.TempDir())
	scope := store.ManagedRuntimeScope{Root: "root", Project: "project", Directory: dir, Mode: store.SessionOwnershipProjectOwned}
	host := store.ManagedRuntimeHost{PID: 123, Start: "fixture-start", Domain: "fixture-domain"}
	effective, generation, err := st.AttachManagedRuntime(scope, host, false)
	if err != nil {
		t.Fatal(err)
	}
	scope.EffectiveID = effective
	var got struct {
		Scopes []map[string]string `json:"scopes"`
	}
	body := get(owned, token, path, http.StatusOK)
	want := map[string]string{"root": scope.Root, "effective_id": scope.EffectiveID,
		"project": scope.Project, "directory": dir, "ownership_mode": scope.Mode}
	if err := json.Unmarshal(body, &got); err != nil || len(got.Scopes) != 1 || !maps.Equal(got.Scopes[0], want) {
		t.Fatalf("scope inventory response: %s (%v)", body, err)
	}
	get(shared, token, path, http.StatusNotFound)
	get(owned, "wrong-token", path, http.StatusUnauthorized)
	if err := st.RetireManagedRuntimeHost(scope, generation, host); err != nil {
		t.Fatal(err)
	}
	if closed, err := st.CloseManagedRuntime(scope, generation); !closed || err != nil {
		t.Fatalf("close managed scope: %v %v", closed, err)
	}
	if body := get(owned, token, path, http.StatusOK); strings.TrimSpace(string(body)) != `{"scopes":[]}` {
		t.Fatalf("closed inventory must return an empty array: %s", body)
	}
}

func TestManagedRuntimeOwnedAPI(t *testing.T) {
	st := newServerTestStore(t)
	dir := t.TempDir()
	mode := store.SessionOwnershipProjectOwned
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
	call(shared, token, prefix+"attach", body, 404)
	call(owned, "invalid", prefix+"attach", body, 401)
	call(shared, token, prefix+"recover-writes", body, 404)
	call(owned, "invalid", prefix+"recover-writes", body, 401)
	call(owned, token, prefix+"recover-writes", body, 400)
	if runtime.GOOS != "linux" {
		body["host"] = map[string]any{"pid": 10, "start": "fixture"}
		call(owned, token, prefix+"admit", body, 503)
		body["resume"] = false
		call(owned, token, prefix+"attach", body, 503)
		return
	}
	// Spawn a child that stays live until the fixture explicitly stops it.
	host := exec.Command("sh", "-c", "exec sleep 120")
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if host.ProcessState == nil {
			_ = host.Process.Kill()
			_ = host.Wait()
		}
	}()
	start, err := hostbind.ProcessStart(host.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	body["host"] = store.ManagedRuntimeHost{PID: host.Process.Pid, Start: start}
	call(owned, token, prefix+"admit", body, 404)
	call(owned, token, prefix+"attach", body, 400)
	body["resume"] = false
	body["effective_id"] = "wrong"
	call(owned, token, prefix+"attach", body, 409)
	body["effective_id"] = "root"
	call(owned, token, prefix+"admit", body, 400)
	delete(body, "effective_id")
	resp := call(owned, token, prefix+"attach", body, 200)
	body["effective_id"] = "root"
	retry := call(owned, token, prefix+"attach", body, 200)
	if retry.Body.String() != resp.Body.String() {
		t.Fatalf("attachment retry changed receipt: %s and %s", retry.Body, resp.Body)
	}
	delete(body, "resume")
	call(owned, token, prefix+"admit", body, 200)
	call(owned, token, "/runtime-sessions/end", map[string]any{
		"id": "root", "directory": dir, "project": "project",
		"ownership_mode": mode, "expected_effective_id": "root",
	}, http.StatusConflict)
	delete(body, "host")
	roster := call(owned, token, prefix+"hosts", body, 200)
	var stored struct{ Hosts []store.ManagedRuntimeHost }
	if err := json.Unmarshal(roster.Body.Bytes(), &stored); err != nil || len(stored.Hosts) != 1 ||
		stored.Hosts[0].PID != host.Process.Pid || stored.Hosts[0].Domain == "" {
		t.Fatalf("invalid managed host roster: %s", roster.Body)
	}
	var admitted struct {
		Generation int64 `json:"generation"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &admitted); err != nil || admitted.Generation != 1 {
		t.Fatalf("bad admission: %s", resp.Body)
	}
	body["generation"] = admitted.Generation
	openHost := call(owned, token, prefix+"close", body, 409)
	if !strings.Contains(openHost.Body.String(), "managed_runtime_hosts_remain") {
		t.Fatal(openHost.Body)
	}
	body["host"] = stored.Hosts[0]
	call(owned, token, prefix+"retire", body, 409)
	delete(body, "host")

	call(owned, token, prefix+"write-intent", body, 400)
	body["operation_id"] = "recoverable-save"
	content := strings.Repeat("x", 9<<10)
	body["observation"] = map[string]any{"title": "Recoverable observation", "content": content, "project": "other"}
	call(owned, token, prefix+"write-intent", body, 400)
	body["observation"] = map[string]any{"title": "Recoverable observation", "content": content}
	call(owned, token, prefix+"write-intent", body, 200)
	body["observation"] = map[string]any{"title": "Recoverable observation", "content": "changed"}
	call(owned, token, prefix+"save", body, 409)
	delete(body, "observation")
	body["operation_id"] = "managed-save"
	call(owned, token, prefix+"write-intent", body, 200)
	delete(body, "operation_id")
	if err := host.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = host.Wait()
	wrongDomain := stored.Hosts[0]
	wrongDomain.Domain = "not-the-same-process-domain"
	body["host"] = wrongDomain
	call(owned, token, prefix+"retire", body, 409)
	body["host"] = stored.Hosts[0]
	call(owned, token, prefix+"retire", body, 200)
	delete(body, "host")
	for _, want := range []int{1, 0} {
		recovered := call(owned, token, prefix+"recover-writes", body, 200)
		var result struct {
			Recovered int
			More      bool
		}
		if err := json.Unmarshal(recovered.Body.Bytes(), &result); err != nil || result.Recovered != want || result.More {
			t.Fatalf("recovery result = %s, expected %d newly recovered writes", recovered.Body, want)
		}
	}
	recoveredID, err := st.GetObservationSaveResult("recoverable-save")
	if err != nil {
		t.Fatal(err)
	}
	if row, err := st.GetObservation(recoveredID); err != nil || row.Content != content {
		t.Fatalf("recovered observation content did not round-trip: %v", err)
	}
	pending := call(owned, token, prefix+"close", body, 409)
	if !strings.Contains(pending.Body.String(), "managed_runtime_write_unresolved") {
		t.Fatal(pending.Body)
	}
	body["operation_id"] = "unrecorded"
	body["observation"] = map[string]any{"title": "Managed observation", "content": "content"}
	call(owned, token, prefix+"save", body, 409)
	body["operation_id"] = "managed-save"
	body["observation"] = map[string]any{"title": "Managed observation", "content": ""}
	call(owned, token, prefix+"save", body, 400)
	delete(body, "operation_id")
	delete(body, "observation")
	stillPending := call(owned, token, prefix+"close", body, 409)
	if !strings.Contains(stillPending.Body.String(), "managed_runtime_write_unresolved") {
		t.Fatal(stillPending.Body)
	}
	body["operation_id"] = "managed-save"
	body["observation"] = map[string]any{"title": "Managed observation", "content": content}
	saved := call(owned, token, prefix+"save", body, 201)
	var receipt struct{ ID int64 }
	if err := json.Unmarshal(saved.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if row, err := st.GetObservation(receipt.ID); err != nil || row.Content != content {
		t.Fatalf("managed observation content did not round-trip: %v", err)
	}
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

func TestManagedRuntimeRecoveryDistinguishesExactEndedSession(t *testing.T) {
	st := newServerTestStore(t)
	scope := store.ManagedRuntimeScope{
		Root: "ended-root", EffectiveID: "ended-root", Project: "project",
		Directory: t.TempDir(), Mode: store.SessionOwnershipProjectOwned,
	}
	host := store.ManagedRuntimeHost{PID: 20, Start: "test-start", Domain: "test-domain"}
	_, generation, err := st.AttachManagedRuntime(scope, host, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordManagedRuntimeObservation(scope, generation, "staged",
		store.AddObservationParams{Title: "Pending", Content: "keep this pending"}); err != nil {
		t.Fatal(err)
	}

	const token = "owned-test-token"
	s := New(st, 0)
	s.SetSessionOwnedToken(token)
	call := func(path string, data any, wantStatus int) string {
		t.Helper()
		content, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(content)))
		request.Header.Set("Authorization", "Bearer "+token)
		result := httptest.NewRecorder()
		s.Handler().ServeHTTP(result, request)
		if result.Code != wantStatus {
			t.Fatalf("%s got %d %s, want %d", path, result.Code, result.Body, wantStatus)
		}
		return result.Body.String()
	}
	call("/sessions/ended-root/end", map[string]string{"summary": "closed explicitly"}, http.StatusOK)
	body := map[string]any{
		"root": scope.Root, "effective_id": scope.EffectiveID, "project": scope.Project,
		"directory": scope.Directory, "ownership_mode": scope.Mode, "generation": generation,
	}
	const recover = "/internal/managed-runtime/recover-writes"
	body["for_retirement"] = true
	call(recover, body, http.StatusBadRequest)
	delete(body, "generation")
	if result := call("/internal/managed-runtime/hosts", body, http.StatusOK); !strings.Contains(result, host.Start) {
		t.Fatalf("retirement roster missing the recorded host: %s", result)
	}
	delete(body, "for_retirement")
	body["generation"] = generation
	if result := call(recover, body, http.StatusConflict); !strings.Contains(result, "managed_runtime_scope_ended") {
		t.Fatalf("exact terminal evidence missing: %s", result)
	}
	body["project"] = "other"
	result := call(recover, body, http.StatusNotFound)
	if strings.Contains(result, "managed_runtime_scope_ended") {
		t.Fatalf("scope mismatch received terminal evidence: %s", result)
	}
	if id, err := st.GetObservationSaveResult("staged"); err != nil || id != 0 {
		t.Fatalf("recovery settled an ended scope's pending write: %d %v", id, err)
	}
}

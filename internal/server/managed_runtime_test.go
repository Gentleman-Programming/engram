package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/hostbind"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

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

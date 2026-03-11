package remote

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── SetTeamSync Tests ──────────────────────────────────────────────────────

func TestSetTeamSyncDefaultTrue(t *testing.T) {
	rt, err := NewRemoteTransport("http://localhost", "tok")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}
	if !rt.teamSync {
		t.Fatal("expected teamSync to be true by default")
	}
}

func TestSetTeamSyncDisable(t *testing.T) {
	rt, err := NewRemoteTransport("http://localhost", "tok")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}
	rt.SetTeamSync(false)
	if rt.teamSync {
		t.Fatal("expected teamSync to be false after SetTeamSync(false)")
	}
}

func TestSetTeamSyncReEnable(t *testing.T) {
	rt, err := NewRemoteTransport("http://localhost", "tok")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}
	rt.SetTeamSync(false)
	rt.SetTeamSync(true)
	if !rt.teamSync {
		t.Fatal("expected teamSync to be true after re-enabling")
	}
}

// ─── PullMutations team_sync query param ─────────────────────────────────────

func TestPullMutationsTeamSyncParamDefaultTrue(t *testing.T) {
	var receivedTeamSync string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTeamSync = r.URL.Query().Get("team_sync")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"mutations":[],"has_more":false}`))
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "tok")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	if _, err := rt.PullMutations(0, 100); err != nil {
		t.Fatalf("PullMutations: %v", err)
	}
	if receivedTeamSync != "true" {
		t.Fatalf("expected team_sync=true, got %q", receivedTeamSync)
	}
}

func TestPullMutationsTeamSyncParamFalse(t *testing.T) {
	var receivedTeamSync string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTeamSync = r.URL.Query().Get("team_sync")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"mutations":[],"has_more":false}`))
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "tok")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}
	rt.SetTeamSync(false)

	if _, err := rt.PullMutations(0, 100); err != nil {
		t.Fatalf("PullMutations: %v", err)
	}
	if receivedTeamSync != "false" {
		t.Fatalf("expected team_sync=false, got %q", receivedTeamSync)
	}
}

// ─── SyncEnrollments Tests ──────────────────────────────────────────────────

func TestSyncEnrollmentsSuccess(t *testing.T) {
	var receivedBody []byte
	var receivedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/sync/enrollments" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		receivedAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "my-token")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	if err := rt.SyncEnrollments([]string{"proj-a", "proj-b"}); err != nil {
		t.Fatalf("SyncEnrollments: %v", err)
	}

	if receivedAuth != "Bearer my-token" {
		t.Fatalf("auth: got %q want %q", receivedAuth, "Bearer my-token")
	}

	var req map[string][]string
	if err := json.Unmarshal(receivedBody, &req); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	projects := req["projects"]
	if len(projects) != 2 || projects[0] != "proj-a" || projects[1] != "proj-b" {
		t.Fatalf("expected [proj-a, proj-b], got %v", projects)
	}
}

func TestSyncEnrollmentsEmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/sync/enrollments" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string][]string
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("unmarshal body: %v", err)
		}
		if len(req["projects"]) != 0 {
			t.Errorf("expected empty projects, got %v", req["projects"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "tok")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	if err := rt.SyncEnrollments([]string{}); err != nil {
		t.Fatalf("SyncEnrollments: %v", err)
	}
}

func TestSyncEnrollmentsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "tok")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	err = rt.SyncEnrollments([]string{"proj"})
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error should mention 403: %v", err)
	}
}

// ─── ListEnrollments Tests ──────────────────────────────────────────────────

func TestListEnrollmentsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/sync/enrollments" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string][]string{
			"projects": {"proj-1", "proj-2", "proj-3"},
		})
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "test-token")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	projects, err := rt.ListEnrollments()
	if err != nil {
		t.Fatalf("ListEnrollments: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(projects))
	}
	if projects[0] != "proj-1" || projects[1] != "proj-2" || projects[2] != "proj-3" {
		t.Fatalf("unexpected projects: %v", projects)
	}
}

func TestListEnrollmentsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"projects":[]}`))
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "tok")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	projects, err := rt.ListEnrollments()
	if err != nil {
		t.Fatalf("ListEnrollments: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("expected empty projects, got %d", len(projects))
	}
}

func TestListEnrollmentsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"invalid credentials"}`))
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "bad-tok")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	_, err = rt.ListEnrollments()
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error should mention 401: %v", err)
	}
}

func TestListEnrollmentsInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	rt, err := NewRemoteTransport(srv.URL, "tok")
	if err != nil {
		t.Fatalf("NewRemoteTransport: %v", err)
	}

	_, err = rt.ListEnrollments()
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

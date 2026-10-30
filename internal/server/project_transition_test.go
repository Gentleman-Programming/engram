package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHTTPDoctorFirstGitInspection(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Git integration")
	}
	for _, history := range []string{"", "remote-app"} {
		t.Run("history="+history, func(t *testing.T) {
			dir := t.TempDir()
			st := newServerTestStore(t)
			if history != "" {
				if err := st.CreateSession("history", history, dir); err != nil {
					t.Fatal(err)
				}
			}
			for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://example.test/team/remote-app.git"}} {
				if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git: %s %v", out, err)
				}
			}
			rec := httptest.NewRecorder()
			New(st, 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/doctor?check=session_project_directory_mismatch&cwd="+url.QueryEscape(dir), nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("doctor status=%d %s", rec.Code, rec.Body.String())
			}
			if _, err := os.Stat(filepath.Join(dir, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
				t.Fatalf("doctor published binding: %v", err)
			}
		})
	}
}

func TestHTTPFirstBindingTransitionConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("requires Git integration")
	}
	dir := t.TempDir()
	st := newServerTestStore(t)
	if err := st.CreateSession("history", "local-app", dir); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://example.test/team/remote-app.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	srv := New(st, 0)
	for _, tt := range []struct {
		path   string
		status int
	}{
		{"/project/current?cwd=" + url.QueryEscape(dir), http.StatusConflict},
		{"/search?q=marker&cwd=" + url.QueryEscape(dir), http.StatusConflict},
		{"/stats?cwd=" + url.QueryEscape(dir), http.StatusConflict},
		{"/search?q=marker&project=local-app", http.StatusOK},
		{"/search?q=marker&all_projects=true", http.StatusOK},
		{"/doctor?project=local-app&check=session_project_directory_mismatch", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != tt.status {
			t.Fatalf("%s: status=%d %s", tt.path, rec.Code, rec.Body.String())
		}
		if tt.status == http.StatusConflict {
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["code"] != "project_transition_conflict" || body["candidate_project"] != "remote-app" {
				t.Fatalf("conflict=%v", body)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
			t.Fatalf("unexpected binding: %v", err)
		}
	}
}

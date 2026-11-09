package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectCurrentIncludesConfiguredOrg(t *testing.T) {
	for _, override := range []string{"", "override-project"} {
		for _, org := range []string{"  acme-corp  ", ""} {
			t.Run("override="+override+"/org="+org, func(t *testing.T) {
				t.Setenv("ENGRAM_PROJECT", override)
				root := t.TempDir()
				if err := os.Mkdir(filepath.Join(root, ".engram"), 0o755); err != nil {
					t.Fatal(err)
				}
				config, err := json.Marshal(map[string]string{"project_name": "org-project", "org": org})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, ".engram", "config.json"), config, 0o600); err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				New(newServerTestStore(t), 0).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/project/current?cwd="+url.QueryEscape(root), nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
				}
				var response map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if org == "" {
					if _, exists := response["org"]; exists {
						t.Errorf("org must be omitted without configuration: %v", response["org"])
					}
				} else if response["org"] != "acme-corp" {
					t.Errorf("org = %v, want acme-corp", response["org"])
				}
				wantProject := "org-project"
				if override != "" {
					wantProject = override
				}
				if response["project"] != wantProject {
					t.Errorf("project = %v, want %s", response["project"], wantProject)
				}
			})
		}
	}
}

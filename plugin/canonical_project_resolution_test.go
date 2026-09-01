package plugin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLifecycleScriptsUseCanonicalProjectResolution(t *testing.T) {
	bashPath := codexTestBash(t)
	requireCodexUnixTools(t, bashPath)

	for _, agent := range []string{"claude-code", "codex"} {
		t.Run(agent+" uses configured project instead of git-derived name", func(t *testing.T) {
			cwd := lifecycleProjectDirectory(t)
			var requestedCWD string
			var sessionProject string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					w.WriteHeader(http.StatusOK)
				case "/project/current":
					requestedCWD = r.URL.Query().Get("cwd")
					_, _ = w.Write([]byte(`{"project":"configured-project","project_source":"config"}`))
				case "/sessions":
					var payload struct {
						Project string `json:"project"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Errorf("decode session payload: %v", err)
					}
					sessionProject = payload.Project
					w.WriteHeader(http.StatusNoContent)
				case "/context":
					_, _ = w.Write([]byte(`{"context":""}`))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			runLifecycleSessionStart(t, bashPath, agent, server.URL, `{"session_id":"canonical-session","cwd":`+jsonQuote(cwd)+`}`)
			if requestedCWD != cwd {
				t.Fatalf("canonical request cwd = %q, want %q", requestedCWD, cwd)
			}
			if sessionProject != "configured-project" {
				t.Fatalf("session project = %q, want configured project instead of git-derived", sessionProject)
			}
		})

		for _, response := range []struct {
			name   string
			status int
			body   string
		}{
			{name: "unavailable", status: http.StatusServiceUnavailable, body: "unavailable"},
			{name: "malformed", status: http.StatusOK, body: "not-json"},
			{name: "empty", status: http.StatusOK, body: `{"project":"","project_source":"config"}`},
			{name: "ambiguous", status: http.StatusOK, body: `{"project":"","project_source":"ambiguous","available_projects":["one","two"]}`},
			{name: "invalid", status: http.StatusOK, body: `{"project":"configured-project","project_source":"unexpected"}`},
			{name: "error hint", status: http.StatusOK, body: `{"project":"configured-project","project_source":"config","error_hint":"choose a project"}`},
		} {
			t.Run(agent+" fails closed when canonical resolution is "+response.name, func(t *testing.T) {
				cwd := lifecycleProjectDirectory(t)
				wroteSession := false
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/health":
						w.WriteHeader(http.StatusOK)
					case "/project/current":
						w.WriteHeader(response.status)
						_, _ = w.Write([]byte(response.body))
					case "/sessions":
						wroteSession = true
						w.WriteHeader(http.StatusNoContent)
					default:
						t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()

				runLifecycleSessionStart(t, bashPath, agent, server.URL, `{"session_id":"closed-session","cwd":`+jsonQuote(cwd)+`}`)
				if wroteSession {
					t.Fatal("session write must not occur when canonical project resolution fails")
				}
			})
		}

		t.Run(agent+" accepts a process override from canonical resolution", func(t *testing.T) {
			cwd := lifecycleProjectDirectory(t)
			var sessionProject string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					w.WriteHeader(http.StatusOK)
				case "/project/current":
					_, _ = w.Write([]byte(`{"project":"override-project","project_source":"process_override"}`))
				case "/sessions":
					var payload struct {
						Project string `json:"project"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Errorf("decode session payload: %v", err)
					}
					sessionProject = payload.Project
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			runLifecycleSessionStart(t, bashPath, agent, server.URL, `{"session_id":"override-session","cwd":`+jsonQuote(cwd)+`}`)
			if sessionProject != "override-project" {
				t.Fatalf("session project = %q; want process override", sessionProject)
			}
		})
	}
}

func lifecycleProjectDirectory(t *testing.T) string {
	t.Helper()
	cwd := filepath.Join(t.TempDir(), "git-derived & space")
	if err := os.MkdirAll(filepath.Join(cwd, ".engram"), 0o755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".engram", "config.json"), []byte(`{"project_name":"configured-project"}`), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	if output, err := exec.Command("git", "-C", cwd, "init").CombinedOutput(); err != nil {
		t.Fatalf("initialize git project: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", cwd, "remote", "add", "origin", "https://example.com/git-derived.git").CombinedOutput(); err != nil {
		t.Fatalf("set git remote: %v: %s", err, output)
	}
	return cwd
}

func runLifecycleSessionStart(t *testing.T, bashPath, agent, serverURL, input string) {
	t.Helper()
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	adapterPath := filepath.Join(repoRoot(t), "plugin", agent, "scripts", "session-start.sh")
	run := exec.Command(bashPath, adapterPath)
	run.Env = append(os.Environ(), "ENGRAM_PORT="+parsedURL.Port())
	run.Stdin = strings.NewReader(input)
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("run %s session start: %v: %s", agent, err, output)
	}
}

func jsonQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

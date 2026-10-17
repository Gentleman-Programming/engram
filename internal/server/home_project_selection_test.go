package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func httpHomeSelectionFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cwd := filepath.Join(home, "new-project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_TEMPLATE_DIR", "GIT_SHALLOW_FILE", "GIT_NAMESPACE", "GIT_REPLACE_REF_BASE", "GIT_PREFIX", "ENGRAM_PROJECT", "ENGRAM_HTTP_TOKEN"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{
		"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"APPDATA": filepath.Join(root, "appdata"), "LOCALAPPDATA": filepath.Join(root, "localappdata"), "GIT_ATTR_NOSYSTEM": "1",
		"ENGRAM_DATA_DIR": filepath.Join(root, "data"), "GIT_CONFIG_COUNT": "0",
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": filepath.Join(root, "global.gitconfig"),
		"GIT_CONFIG_SYSTEM": filepath.Join(root, "system.gitconfig"), "GIT_CEILING_DIRECTORIES": root,
	} {
		t.Setenv(key, value)
	}
	httpHomeGit(t, home, "init")
	t.Chdir(cwd)
	return home, cwd
}

func httpHomeGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func homeSelectionHTTP(t *testing.T, srv *Server, method, path, body string, status int) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	if rec.Code != status {
		t.Errorf("%s %s: status=%d want=%d body=%s", method, path, rec.Code, status, rec.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("JSON response: %s: %v", rec.Body.String(), err)
	}
	return result
}

func TestHomeProjectSelectionHTTPRejectsContext(t *testing.T) {
	for _, name := range []string{"untracked", "new-dotfiles"} {
		t.Run(name, func(t *testing.T) {
			home, cwd := httpHomeSelectionFixture(t)
			if name == "new-dotfiles" {
				cwd = filepath.Join(home, ".config")
				if err := os.Mkdir(cwd, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			f := newWorkflowReviewFixture(t)
			before := f.snapshot(t)
			mutations, err := f.store.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1000)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"code": "explicit_project_required", "error": "project resolution failed: explicit project required: use --project, ENGRAM_PROJECT, or .engram/config.json project_name"}
			for _, request := range []struct{ method, path, body string }{
				{"GET", "/project/current?cwd=" + url.QueryEscape(cwd), ""},
				{"POST", "/prompts/capture-decision", `{"source":"claude-code","cwd":` + quoteCapture(cwd) + `,"content":"human input"}`},
			} {
				got := homeSelectionHTTP(t, f.server, request.method, request.path, request.body, 409)
				if !reflect.DeepEqual(got, want) {
					t.Errorf("context error=%v want=%v", got, want)
				}
				f.unchanged(t, before)
				after, err := f.store.ListPendingSyncMutations(store.DefaultSyncTargetKey, 1000)
				if err != nil || !reflect.DeepEqual(mutations, after) {
					t.Errorf("pending journal changed: %v", err)
				}
				if _, err := os.Stat(filepath.Join(home, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
					t.Errorf("context rejection published binding: %v", err)
				}
			}
			got := f.post(t, "/prompts/capture-decision", `{"source":"claude-code","cwd":`+quoteCapture(cwd)+`,"content":"<task-notification>done"}`, 200, map[string]any{"decision": "skip"})
			if got["decision"] != "skip" {
				t.Errorf("skip=%v", got)
			}
			f.unchanged(t, before)
		})
	}
}

func TestHomeProjectSelectionHTTPAuthorityAndReads(t *testing.T) {
	home, _ := httpHomeSelectionFixture(t)
	f := newWorkflowReviewFixture(t)
	before := f.snapshot(t)
	for _, path := range []string{"/search?q=source", "/projects", fmt.Sprintf("/observations/%d", f.ids[0]), "/sessions/review"} {
		rec := httptest.NewRecorder()
		f.server.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !json.Valid(rec.Body.Bytes()) {
			t.Errorf("ordinary read %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	f.unchanged(t, before)
	binding, err := os.ReadFile(filepath.Join(home, ".git", "engram-project-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Explicit project and existing session authority must not acquire a cwd guard.
	f.post(t, "/sessions", `{"id":"explicit","project":"project","ownership_mode":"project_owned"}`, 201, map[string]any{"id": "explicit", "status": "created"})
	f.post(t, "/observations", `{"session_id":"explicit","project":"project","title":"explicit marker","content":"accepted evidence","current_prompt":"accepted automatic prompt"}`, 201, nil)
	f.post(t, "/prompts", `{"session_id":"explicit","project":"project","content":"accepted direct prompt"}`, 201, nil)
	f.post(t, "/observations/passive", `{"session_id":"explicit","project":"project","content":"## Key Learnings:\n- Explicit authority remains intact","source":"test"}`, 200, nil)
	f.post(t, "/sessions/explicit/end", `{"summary":"finished explicitly"}`, 200, map[string]any{"id": "explicit", "status": "completed"})
	session, err := f.store.GetSession("explicit")
	if err != nil || session.Project != "project" || session.EndedAt == nil {
		t.Fatalf("session authority=%+v err=%v", session, err)
	}
	rows, err := f.store.RecentObservations("project", "project", 10)
	if err != nil || len(rows) < 3 {
		t.Fatalf("explicit capture attribution rows=%v err=%v", rows, err)
	}
	prompts, err := f.store.RecentPrompts("project", 10)
	if err != nil || len(prompts) != 2 {
		t.Fatalf("explicit prompt attribution=%v err=%v", prompts, err)
	}
	// Reads may legitimately establish a binding; explicit captures must not rename it.
	f.post(t, "/sessions", `{"id":"another","project":"other"}`, 201, nil)
	after, err := os.ReadFile(filepath.Join(home, ".git", "engram-project-identity.json"))
	if err != nil || !reflect.DeepEqual(binding, after) {
		t.Fatalf("explicit registration changed binding: %v", err)
	}
}

func TestHomeProjectSelectionHTTPControls(t *testing.T) {
	for _, name := range []string{"home-root", "tracked-dotfiles", "nested", "non-home", "config", "inherited-config", "environment", "request"} {
		t.Run(name, func(t *testing.T) {
			home, cwd := httpHomeSelectionFixture(t)
			want := "home"
			switch name {
			case "home-root":
				cwd = home
			case "tracked-dotfiles":
				cwd = filepath.Join(home, ".config")
				if err := os.Mkdir(cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cwd, "settings"), []byte("tracked"), 0o600); err != nil {
					t.Fatal(err)
				}
				httpHomeGit(t, home, "add", "--", ".config/settings")
			case "nested":
				httpHomeGit(t, cwd, "init")
				want = "new-project"
			case "non-home":
				repo := filepath.Join(filepath.Dir(home), "monorepo")
				cwd = filepath.Join(repo, "untracked")
				if err := os.MkdirAll(cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				httpHomeGit(t, repo, "init")
				want = "monorepo"
			default:
				if err := os.Mkdir(filepath.Join(cwd, ".engram"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cwd, ".engram", "config.json"), []byte(`{"project_name":"configured"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "configured"
				if name == "inherited-config" {
					cwd = filepath.Join(cwd, "child")
					if err := os.Mkdir(cwd, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if name == "environment" || name == "request" {
					t.Setenv("ENGRAM_PROJECT", "environment")
					want = "environment"
				}
			}
			f := newWorkflowReviewFixture(t)
			if name == "request" {
				// Ordinary scoped reads retain request > environment > config precedence.
				got := homeSelectionHTTP(t, f.server, "GET", "/stats?project=project&cwd="+url.QueryEscape(cwd), "", 200)
				if got["total_observations"] != float64(2) {
					t.Fatalf("request scope=%v", got)
				}
				return
			}
			got := homeSelectionHTTP(t, f.server, "GET", "/project/current?cwd="+url.QueryEscape(cwd), "", 200)
			if got["project"] != want {
				t.Fatalf("project=%v want=%s", got, want)
			}
			before := f.snapshot(t)
			f.writes = 0
			got = f.post(t, "/prompts/capture-decision", `{"source":"claude-code","cwd":`+quoteCapture(cwd)+`,"content":"human input"}`, 200, nil)
			if got["decision"] != "capture" || got["project"] != want {
				t.Fatalf("decision=%v", got)
			}
			f.unchanged(t, before)
		})
	}
}

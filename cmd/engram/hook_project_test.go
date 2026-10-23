package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
)

func TestHookResolveHookProject(t *testing.T) {
	binary := buildCompatibilityBinary(t)
	var requests atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer trap.Close()

	for _, name := range []string{
		"configured", "unicode-config", "non-git", "relative-cwd", "relative-dot-cwd", "bound-git", "linked-worktree",
		"unbound-git", "unbound-child", "ambiguous", "invalid-config", "missing-directory", "file-directory",
		"broken-gitfile", "broken-git-directory", "broken-git-child", "broken-git-descendant",
		"malformed-json", "invalid-utf8", "missing-cwd", "null-cwd", "number-cwd", "blank-cwd", "nul-cwd", "wrong-case-cwd", "duplicate-cwd",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			env := hookProjectFixtureEnvironment(t, root, trap.URL)
			cwd := filepath.Join(root, "non-git")
			if strings.HasPrefix(name, "broken-git") {
				cwd = filepath.Join(root, "feature-x")
			}
			if err := os.Mkdir(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			wantProject, wantSource := "", ""
			switch name {
			case "configured", "unicode-config":
				value := "MyProj"
				wantProject = "myproj"
				if name == "unicode-config" {
					value, wantProject = "Cafe\u0301", "café"
				}
				hookProjectFixtureConfig(t, cwd, value)
				wantSource = "config"
			case "non-git":
				wantProject, wantSource = "non-git", "dir_basename"
			case "relative-cwd":
				cwd = "sub/dir"
				if err := os.MkdirAll(filepath.Join(root, "sub", "dir"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "relative-dot-cwd":
				hookProjectFixtureConfig(t, root, "ambient-project")
				cwd = "."
			case "bound-git", "linked-worktree", "unbound-git":
				hookProjectFixtureGit(t, env, "init", "-q", cwd)
				hookProjectFixtureGit(t, env, "-C", cwd, "remote", "add", "origin", "https://example.invalid/canonical-bound.git")
				if name != "unbound-git" {
					// Seed a binding through the existing public detector, not a private format.
					resolved := projectpkg.DetectProjectFull(cwd)
					if resolved.Error != nil || resolved.Project != "canonical-bound" {
						t.Fatalf("fixture binding: %+v", resolved)
					}
					wantProject, wantSource = "canonical-bound", "git_remote"
				}
				if name == "linked-worktree" {
					hookProjectFixtureGit(t, env, "-C", cwd, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "fixture")
					linked := filepath.Join(root, "linked")
					hookProjectFixtureGit(t, env, "-C", cwd, "worktree", "add", "-q", "--detach", linked)
					cwd = linked
				}
			case "unbound-child", "ambiguous":
				hookProjectFixtureGit(t, env, "init", "-q", filepath.Join(cwd, "first"))
				if name == "ambiguous" {
					hookProjectFixtureGit(t, env, "init", "-q", filepath.Join(cwd, "second"))
				}
			case "broken-gitfile":
				if err := os.WriteFile(filepath.Join(cwd, ".git"), []byte("gitdir: ../missing-metadata\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "broken-git-directory", "broken-git-child", "broken-git-descendant":
				marker := filepath.Join(cwd, ".git")
				if name == "broken-git-child" {
					marker = filepath.Join(cwd, "child", ".git")
				}
				if err := os.MkdirAll(marker, 0o700); err != nil {
					t.Fatal(err)
				}
				if name == "broken-git-descendant" {
					cwd = filepath.Join(cwd, "nested")
					if err := os.Mkdir(cwd, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			case "invalid-config":
				hookProjectFixtureConfig(t, cwd, "../invalid")
			case "missing-directory":
				cwd = filepath.Join(root, "absent", "feature-x")
			case "file-directory":
				cwd = filepath.Join(root, "not-a-directory")
				if err := os.WriteFile(cwd, []byte("sentinel"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(name, "broken-git") {
				// The strict new inspector must not change ordinary detection.
				legacyBefore := compatibilityTreeHash(t, root)
				legacy := projectpkg.DetectProjectFull(cwd)
				project, source, path, warning := filepath.Base(cwd), "dir_basename", cwd, ""
				if name == "broken-git-child" {
					project, source, path = "child", "git_child", filepath.Join(cwd, "child")
					warning = "auto-promoted child repository: child"
				}
				if legacy.Error != nil || legacy.Project != project || legacy.Source != source || legacy.Path != path || legacy.Warning != warning || len(legacy.AvailableProjects) != 0 {
					t.Fatalf("ordinary detector changed: %+v", legacy)
				}
				if compatibilityTreeHash(t, root) != legacyBefore {
					t.Fatal("ordinary malformed-Git detection changed fixture state")
				}
			}
			input, err := json.Marshal(map[string]any{"cwd": cwd, "session_id": "host", "private_field": "never-echo-this"})
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "malformed-json":
				input = []byte(`{"cwd":`)
			case "invalid-utf8":
				input = []byte{'{', '"', 'c', 'w', 'd', '"', ':', '"', 0xff, '"', '}'}
			case "missing-cwd":
				input = []byte(`{"session_id":"host"}`)
			case "null-cwd":
				input = []byte(`{"cwd":null}`)
			case "number-cwd":
				input = []byte(`{"cwd":7}`)
			case "blank-cwd":
				input = []byte(`{"cwd":"  "}`)
			case "nul-cwd":
				input = []byte(`{"cwd":"bad\u0000path"}`)
			case "wrong-case-cwd":
				input, _ = json.Marshal(map[string]string{"CWD": cwd})
			case "duplicate-cwd":
				encoded, _ := json.Marshal(cwd)
				input = []byte(fmt.Sprintf(`{"cwd":%s,"cwd":%s}`, encoded, encoded))
			}
			before := compatibilityTreeHash(t, root)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "hook", "resolve-hook-project")
			cmd.Dir, cmd.Env, cmd.Stdin = root, env, bytes.NewReader(input)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			runErr := cmd.Run()
			if runErr != nil || stderr.Len() != 0 {
				t.Errorf("exit=%v stdout=%q stderr=%q; want exit 0 and empty stderr", runErr, stdout.String(), stderr.String())
			}
			if wantProject == "" {
				if stdout.String() != "{}\n" {
					t.Errorf("stdout=%q, want exactly {} followed by newline", stdout.String())
				}
			} else {
				var result struct {
					Project string `json:"project"`
					Source  string `json:"project_source"`
					Path    string `json:"project_path"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Project != wantProject || result.Source != wantSource {
					t.Errorf("response=%q error=%v, want project=%q source=%q", stdout.String(), err, wantProject, wantSource)
				}
				// A linked worktree shares the primary repository's identity path;
				// the other positive fixtures resolve to this directory directly.
				wantInfo, wantErr := os.Stat(filepath.Join(root, "non-git"))
				gotInfo, pathErr := os.Stat(result.Path)
				if wantErr != nil || pathErr != nil || !os.SameFile(wantInfo, gotInfo) {
					t.Errorf("project_path=%q errors=%v/%v, want the fixture identity directory", result.Path, wantErr, pathErr)
				}
				if strings.Contains(stdout.String(), "never-echo-this") {
					t.Error("response leaked unrelated hook input")
				}
			}
			if after := compatibilityTreeHash(t, root); after != before {
				t.Error("inspection changed fixture HOME, data, config or Git state")
			}
			if requests.Load() != 0 {
				t.Errorf("HTTP requests=%d, want 0", requests.Load())
			}
		})
	}

	t.Run("existing-read-only-hook", func(t *testing.T) {
		root := t.TempDir()
		env := hookProjectFixtureEnvironment(t, root, trap.URL)
		before := compatibilityTreeHash(t, root)
		cmd := exec.Command(binary, "hook", "claude-pre-tool-use")
		cmd.Dir, cmd.Env = root, env
		cmd.Stdin = strings.NewReader(`{"tool_name":"mcp__other__read","session_id":"host","tool_input":{}}`)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil || stdout.String() != "{}" || stderr.Len() != 0 {
			t.Fatalf("existing hook exit=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
		if compatibilityTreeHash(t, root) != before || requests.Load() != 0 {
			t.Fatal("existing read-only hook caused side effects")
		}
	})
}

func hookProjectFixtureConfig(t *testing.T, cwd, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(cwd, ".engram"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"project_name": name})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".engram", "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func hookProjectFixtureEnvironment(t *testing.T, root, endpoint string) []string {
	t.Helper()
	env := make([]string, 0, 24)
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "TEMP", "TMP", "TMPDIR", "ENGRAM_DATA_DIR"} {
		dir := filepath.Join(root, strings.ToLower(key))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		env = append(env, key+"="+dir)
		if key == "HOME" || key == "USERPROFILE" {
			t.Setenv(key, dir)
		}
	}
	globalConfig := filepath.Join(root, "git-global-config")
	if err := os.WriteFile(globalConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"GIT_CONFIG_GLOBAL": globalConfig, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_COUNT": "0", "GIT_CEILING_DIRECTORIES": root} {
		env = append(env, key+"="+value)
		t.Setenv(key, value)
	}
	return append(env, "ENGRAM_URL="+endpoint, "ENGRAM_NO_UPDATE_CHECK=1", "ENGRAM_CLOUD_AUTOSYNC=0")
}

func hookProjectFixtureGit(t *testing.T, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

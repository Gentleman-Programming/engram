package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// All paths and Git configuration belong to this test, including on Windows.
func cliHomeSelectionFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	for _, dir := range []string{home, filepath.Join(home, "new-project")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
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
	cliHomeGit(t, home, "init")
	cliHomeGit(t, home, "remote", "add", "origin", "https://example.test/home.git")
	return home, filepath.Join(home, "new-project")
}

func cliHomeGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestHomeProjectSelectionCLI(t *testing.T) {
	for _, name := range []string{"untracked", "new-dotfiles", "home-root", "tracked-dotfiles", "nested", "non-home", "config", "inherited-config", "environment", "request"} {
		t.Run(name, func(t *testing.T) {
			home, cwd := cliHomeSelectionFixture(t)
			wantProject := "home"
			reject := name == "untracked" || name == "new-dotfiles"
			switch name {
			case "new-dotfiles", "tracked-dotfiles":
				cwd = filepath.Join(home, ".config")
			case "home-root":
				cwd = home
			case "non-home":
				repo := filepath.Join(filepath.Dir(home), "monorepo")
				cwd = filepath.Join(repo, "untracked")
				if err := os.MkdirAll(cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				cliHomeGit(t, repo, "init")
				cliHomeGit(t, repo, "remote", "add", "origin", "https://example.test/monorepo.git")
				wantProject = "monorepo"
			}
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			if name == "tracked-dotfiles" {
				if err := os.WriteFile(filepath.Join(cwd, "settings"), []byte("tracked"), 0o600); err != nil {
					t.Fatal(err)
				}
				cliHomeGit(t, home, "add", "--", ".config/settings")
			}
			if name == "nested" {
				cliHomeGit(t, cwd, "init")
				cliHomeGit(t, cwd, "remote", "add", "origin", "https://example.test/nested.git")
				wantProject = "nested"
			}
			if name == "config" || name == "inherited-config" || name == "environment" || name == "request" {
				configRoot := cwd
				if name == "inherited-config" {
					cwd = filepath.Join(cwd, "child")
					if err := os.Mkdir(cwd, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Mkdir(filepath.Join(configRoot, ".engram"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(configRoot, ".engram", "config.json"), []byte(`{"project_name":"configured"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				wantProject = "configured"
			}
			args := []string{"engram", "save", "selection marker", "selection evidence"}
			if name == "environment" || name == "request" {
				t.Setenv("ENGRAM_PROJECT", "environment")
				wantProject = "environment"
			}
			if name == "request" {
				args = append(args, "--project", "requested")
				wantProject = "requested"
			}
			withCwd(t, cwd)
			withArgs(t, args...)
			stubExitWithPanic(t)
			cfg := testConfig(t)
			stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdSave(cfg) })
			if reject {
				wantError := "engram: cannot save without an unambiguous project identity: explicit project required: use --project, ENGRAM_PROJECT, or .engram/config.json project_name; use --project <name>\n"
				if recovered != exitCode(1) || stdout != "" || stderr != wantError {
					t.Errorf("exit=%v stdout=%q stderr=%q", recovered, stdout, stderr)
				}
				entries, err := os.ReadDir(cfg.DataDir)
				if err != nil || len(entries) != 0 {
					t.Errorf("rejected save created SQLite/data: entries=%v err=%v", entries, err)
				}
			} else {
				if recovered != nil || stdout != "Memory saved: #1 \"selection marker\" (manual)\n" || stderr != "" {
					t.Fatalf("exit=%v stdout=%q stderr=%q", recovered, stdout, stderr)
				}
				s, err := store.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
				rows, err := s.RecentObservations(wantProject, "project", 10)
				if err != nil || len(rows) != 1 {
					t.Fatalf("project=%s rows=%v err=%v", wantProject, rows, err)
				}
				session, err := s.GetSession(fmt.Sprintf("manual-save-%s", wantProject))
				if err != nil || session.Project != wantProject {
					t.Fatalf("session=%+v err=%v", session, err)
				}
			}
			if reject || name == "config" || name == "inherited-config" || name == "environment" || name == "request" {
				if _, err := os.Stat(filepath.Join(home, ".git", "engram-project-identity.json")); !os.IsNotExist(err) {
					t.Errorf("unexpected HOME binding: %v", err)
				}
			}
		})
	}
}

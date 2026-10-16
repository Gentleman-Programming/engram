package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHomeProjectSelection(t *testing.T) {
	for _, name := range []string{
		"reject", "inspect", "new-dotfiles", "corrupt-index", "default-read",
		"home-root", "tracked", "nested-repo", "non-home", "config", "inherited-config",
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			home := filepath.Join(parent, "home")
			cwd := filepath.Join(home, "workspace")
			if name == "new-dotfiles" {
				cwd = filepath.Join(home, ".new-dotfiles")
			}
			if name == "non-home" {
				cwd = filepath.Join(parent, "ordinary", "workspace")
			}
			if err := os.MkdirAll(cwd, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"ENGRAM_PROJECT", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT"} {
				t.Setenv(key, "")
				if err := os.Unsetenv(key); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(parent, "no-global-config"))
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CEILING_DIRECTORIES", parent)
			root := home
			if name == "non-home" {
				root = filepath.Dir(cwd)
			}
			initGit(t, root)
			switch name {
			case "home-root":
				cwd = home
			case "tracked":
				file := filepath.Join(cwd, "dotfile")
				if err := os.WriteFile(file, []byte("tracked"), 0o600); err != nil {
					t.Fatal(err)
				}
				gitIdentity(t, root, "add", "--", file)
			case "nested-repo":
				initGit(t, cwd)
				root = cwd
			case "config", "inherited-config":
				configDir := cwd
				if name == "inherited-config" {
					configDir = home
				}
				if err := os.Mkdir(filepath.Join(configDir, ".engram"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(configDir, ".engram", "config.json"), []byte(`{"project_name":"designated"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "corrupt-index":
				if err := os.WriteFile(filepath.Join(root, ".git", "index"), []byte("invalid index"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			reject := name == "reject" || name == "inspect" || name == "new-dotfiles" || name == "corrupt-index"
			historyCalls := 0
			got := DetectProjectFullWithOptions(cwd, DetectionOptions{
				RequireExplicitHomeProject: name != "default-read",
				InspectOnly:                name == "inspect",
				HistoryLookup: func(string) ([]string, error) {
					historyCalls++
					return nil, nil
				},
			})
			binding := filepath.Join(root, ".git", "engram-project-identity.json")
			if reject {
				if got.Error == nil || got.Project != "" {
					t.Errorf("unsafe strict result: %+v", got)
				}
				if errors.Is(got.Error, ErrAmbiguousProject) {
					t.Errorf("strict failure must not permit ambiguity recovery: %v", got.Error)
				}
				if name != "corrupt-index" && !errors.Is(got.Error, ErrExplicitProjectRequired) {
					t.Errorf("error = %v, want explicit project requirement", got.Error)
				}
				if historyCalls != 0 {
					t.Errorf("rejection consulted history %d times", historyCalls)
				}
				if _, err := os.Stat(binding); !os.IsNotExist(err) {
					t.Errorf("rejection published binding: %v", err)
				}
				return
			}
			want := filepath.Base(root)
			if name == "config" || name == "inherited-config" {
				want = "designated"
				if historyCalls != 0 {
					t.Errorf("config consulted history %d times", historyCalls)
				}
			} else if _, err := os.Stat(binding); err != nil {
				t.Errorf("normal detection did not preserve binding initialization: %v", err)
			}
			if got.Error != nil || got.Project != want {
				t.Errorf("result = %+v, want %q", got, want)
			}
		})
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
	versioncheck "github.com/Gentleman-Programming/engram/v2/internal/version"
)

func TestCmdInit(t *testing.T) {
	for _, tt := range []struct {
		name, directory, existing, want, wantErr string
		args                                     []string
	}{
		{"explicit name", "workspace", "", "{\n  \"project_name\": \"my-project\"\n}\n", "", []string{"engram", "init", "my-project"}},
		{"directory basename", "workspace-alpha", "", "{\n  \"project_name\": \"workspace-alpha\"\n}\n", "", []string{"engram", "init"}},
		{"refuses existing", "workspace", `{"project_name":"old"}`, `{"project_name":"old"}`, "already exists", []string{"engram", "init", "new"}},
		{"force replaces", "workspace", `{"project_name":"old"}`, "{\n  \"project_name\": \"new\"\n}\n", "", []string{"engram", "init", "new", "--force"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workDir := filepath.Join(t.TempDir(), tt.directory)
			if err := os.Mkdir(workDir, 0o755); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(workDir, ".engram", "config.json")
			if tt.existing != "" {
				if err := os.Mkdir(filepath.Dir(configPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(configPath, []byte(tt.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			withCwd(t, workDir)
			stubExitWithPanic(t)
			withArgs(t, tt.args...)
			_, stderr, recovered := captureOutputAndRecover(t, cmdInit)
			if tt.wantErr != "" {
				if _, ok := recovered.(exitCode); !ok || !strings.Contains(stderr, tt.wantErr) {
					t.Fatalf("expected %q, panic=%v stderr=%q", tt.wantErr, recovered, stderr)
				}
			} else if recovered != nil || stderr != "" {
				t.Fatalf("cmdInit failed: panic=%v stderr=%q", recovered, stderr)
			}
			got, err := os.ReadFile(configPath)
			if err != nil || string(got) != tt.want {
				t.Fatalf("config = %q, %v; want %q", got, err, tt.want)
			}
			if tt.name == "force replaces" {
				entries, _ := os.ReadDir(filepath.Dir(configPath))
				if len(entries) != 1 || entries[0].Name() != "config.json" {
					t.Fatalf("config directory entries = %v", entries)
				}
			}
		})
	}
}

func TestCmdInitRejectsInvalidArguments(t *testing.T) {
	for _, tt := range []struct{ args []string }{
		{[]string{"engram", "init", "   "}},
		{[]string{"engram", "init", "invalid/name"}},
		{[]string{"engram", "init", "bad\nname"}},
		{[]string{"engram", "init", "--other"}},
		{[]string{"engram", "init", "one", "two"}},
	} {
		t.Run(strings.Join(tt.args[2:], " "), func(t *testing.T) {
			withCwd(t, t.TempDir())
			stubExitWithPanic(t)
			withArgs(t, tt.args...)
			_, _, recovered := captureOutputAndRecover(t, cmdInit)
			if _, ok := recovered.(exitCode); !ok {
				t.Fatalf("expected init failure, panic=%v", recovered)
			}
		})
	}
}

func TestWriteInitConfigRejectsStaticSymlinks(t *testing.T) {
	for _, configLink := range []bool{false, true} {
		t.Run(map[bool]string{false: "directory", true: "config"}[configLink], func(t *testing.T) {
			workDir := t.TempDir()
			target := filepath.Join(workDir, "target")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			targetConfig := filepath.Join(target, "config.json")
			if err := os.WriteFile(targetConfig, []byte("preserved"), 0o644); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(workDir, ".engram")
			linkTarget := target
			if configLink {
				if err := os.Mkdir(link, 0o755); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(link, "config.json")
				linkTarget = targetConfig
			}
			if err := os.Symlink(linkTarget, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if err := writeInitConfig(workDir, []byte("new"), true); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("symlink error = %v", err)
			}
			if got, _ := os.ReadFile(targetConfig); string(got) != "preserved" {
				t.Fatalf("symlink target changed to %q", got)
			}
		})
	}
}

func TestMainDispatchInitSkipsStartup(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	withArgs(t, "engram", "init", "dispatched")
	oldCheck := checkForUpdates
	checkForUpdates = func(string) versioncheck.CheckResult {
		t.Fatal("init must not check for updates")
		return versioncheck.CheckResult{}
	}
	t.Cleanup(func() { checkForUpdates = oldCheck })
	oldConfig := storeDefaultConfig
	storeDefaultConfig = func() (store.Config, error) {
		t.Fatal("init must not resolve store config")
		return store.Config{}, nil
	}
	t.Cleanup(func() { storeDefaultConfig = oldConfig })
	oldMigrate := migrateOrphanedDatabase
	migrateOrphanedDatabase = func(string) { t.Fatal("init must not migrate") }
	t.Cleanup(func() { migrateOrphanedDatabase = oldMigrate })

	_, stderr, recovered := captureOutputAndRecover(t, main)
	if recovered != nil || stderr != "" {
		t.Fatalf("main init dispatch failed: panic=%v stderr=%q", recovered, stderr)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".engram", "config.json")); err != nil {
		t.Fatalf("init config missing: %v", err)
	}
}

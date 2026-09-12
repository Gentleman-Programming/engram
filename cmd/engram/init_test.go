package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/project"
	"github.com/Gentleman-Programming/engram/v2/internal/store"
	versioncheck "github.com/Gentleman-Programming/engram/v2/internal/version"
)

func TestCmdInit_ExplicitProjectName(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	stubExitWithPanic(t)

	withArgs(t, "engram", "init", "my-awesome-project")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdInit() })
	if recovered != nil || stderr != "" {
		t.Fatalf("cmdInit failed: panic=%v stderr=%q", recovered, stderr)
	}

	if !strings.Contains(stdout, `Initialized Engram project "my-awesome-project"`) {
		t.Fatalf("unexpected stdout: %q", stdout)
	}

	configPath := filepath.Join(workDir, ".engram", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read created config: %v", err)
	}

	var cfg struct {
		ProjectName string `json:"project_name"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to parse created config: %v", err)
	}
	if cfg.ProjectName != "my-awesome-project" {
		t.Fatalf("expected project_name %q, got %q", "my-awesome-project", cfg.ProjectName)
	}

	// Verify project detection picks it up immediately
	res := project.DetectProjectFull(workDir)
	if res.Project != "my-awesome-project" {
		t.Fatalf("expected detected project %q, got %q (source=%s)", "my-awesome-project", res.Project, res.Source)
	}
}

func TestCmdInit_DefaultToDirectoryName(t *testing.T) {
	baseDir := t.TempDir()
	workDir := filepath.Join(baseDir, "workspace-alpha")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("failed to create workDir: %v", err)
	}
	withCwd(t, workDir)
	stubExitWithPanic(t)

	withArgs(t, "engram", "init")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdInit() })
	if recovered != nil || stderr != "" {
		t.Fatalf("cmdInit default failed: panic=%v stderr=%q", recovered, stderr)
	}

	if !strings.Contains(stdout, `Initialized Engram project "workspace-alpha"`) {
		t.Fatalf("unexpected stdout: %q", stdout)
	}

	configPath := filepath.Join(workDir, ".engram", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read created config: %v", err)
	}

	var cfg struct {
		ProjectName string `json:"project_name"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to parse created config: %v", err)
	}
	if cfg.ProjectName != "workspace-alpha" {
		t.Fatalf("expected project_name %q, got %q", "workspace-alpha", cfg.ProjectName)
	}
}

func TestCmdInit_ExistingConfigWithoutForce(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	stubExitWithPanic(t)

	configDir := filepath.Join(workDir, ".engram")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create .engram dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"project_name":"existing-proj"}`), 0644); err != nil {
		t.Fatalf("failed to write existing config: %v", err)
	}

	withArgs(t, "engram", "init", "new-proj")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdInit() })
	if _, ok := recovered.(exitCode); !ok || !strings.Contains(stderr, "already exists") {
		t.Fatalf("expected exit with already exists error, panic=%v stderr=%q", recovered, stderr)
	}
}

func TestCmdInit_ExistingConfigWithForce(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	stubExitWithPanic(t)

	configDir := filepath.Join(workDir, ".engram")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("failed to create .engram dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"project_name":"existing-proj"}`), 0644); err != nil {
		t.Fatalf("failed to write existing config: %v", err)
	}

	withArgs(t, "engram", "init", "--force", "overwritten-proj")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdInit() })
	if recovered != nil || stderr != "" {
		t.Fatalf("cmdInit with force failed: panic=%v stderr=%q", recovered, stderr)
	}

	if !strings.Contains(stdout, `Initialized Engram project "overwritten-proj"`) {
		t.Fatalf("unexpected stdout: %q", stdout)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read updated config: %v", err)
	}

	var cfg struct {
		ProjectName string `json:"project_name"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to parse updated config: %v", err)
	}
	if cfg.ProjectName != "overwritten-proj" {
		t.Fatalf("expected project_name %q, got %q", "overwritten-proj", cfg.ProjectName)
	}
}

func TestCmdInit_InvalidProjectName(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	stubExitWithPanic(t)

	withArgs(t, "engram", "init", "invalid/slash")
	_, stderr, recovered := captureOutputAndRecover(t, func() { cmdInit() })
	if _, ok := recovered.(exitCode); !ok || (!strings.Contains(stderr, "invalid") && !strings.Contains(stderr, "must be a name")) {
		t.Fatalf("expected exit with invalid name error, panic=%v stderr=%q", recovered, stderr)
	}
}

func TestCmdInit_Help(t *testing.T) {
	stubExitWithPanic(t)

	withArgs(t, "engram", "init", "--help")
	stdout, stderr, recovered := captureOutputAndRecover(t, func() { cmdInit() })
	if recovered != nil || stderr != "" {
		t.Fatalf("help failed: panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, "usage: engram init") {
		t.Fatalf("expected usage in stdout, got: %q", stdout)
	}
}

func TestMainDispatchInitSkipsUpdateCheck(t *testing.T) {
	workDir := t.TempDir()
	withCwd(t, workDir)
	t.Setenv("ENGRAM_DATA_DIR", t.TempDir())
	withArgs(t, "engram", "init", "dispatched-project")

	oldCheckForUpdates := checkForUpdates
	checkForUpdates = func(string) versioncheck.CheckResult {
		t.Fatal("init must not invoke the update check")
		return versioncheck.CheckResult{}
	}
	t.Cleanup(func() { checkForUpdates = oldCheckForUpdates })

	oldStoreDefaultConfig := storeDefaultConfig
	storeDefaultConfig = func() (store.Config, error) {
		t.Fatal("init must not resolve global store config")
		return store.Config{}, nil
	}
	t.Cleanup(func() { storeDefaultConfig = oldStoreDefaultConfig })

	oldMigrateOrphanedDB := migrateOrphanedDatabase
	migrateOrphanedDatabase = func(string) {
		t.Fatal("init must not migrate orphaned databases")
	}
	t.Cleanup(func() { migrateOrphanedDatabase = oldMigrateOrphanedDB })

	stdout, stderr, recovered := captureOutputAndRecover(t, main)
	if recovered != nil || stderr != "" {
		t.Fatalf("main init dispatch failed: panic=%v stderr=%q", recovered, stderr)
	}
	if !strings.Contains(stdout, `Initialized Engram project "dispatched-project"`) {
		t.Fatalf("unexpected stdout: %q", stdout)
	}

	data, err := os.ReadFile(filepath.Join(workDir, ".engram", "config.json"))
	if err != nil {
		t.Fatalf("read dispatched config: %v", err)
	}
	var cfg struct {
		ProjectName string `json:"project_name"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse dispatched config: %v", err)
	}
	if cfg.ProjectName != "dispatched-project" {
		t.Fatalf("dispatched config project_name = %q, want %q", cfg.ProjectName, "dispatched-project")
	}
}

func TestWriteInitConfigConcurrentNonForcePreservesWinner(t *testing.T) {
	workDir := t.TempDir()
	configs := [][]byte{
		[]byte(`{"project_name":"first"}`),
		[]byte(`{"project_name":"second"}`),
	}

	start := make(chan struct{})
	errs := make(chan error, len(configs))
	var wg sync.WaitGroup
	for _, config := range configs {
		wg.Add(1)
		go func(config []byte) {
			defer wg.Done()
			<-start
			errs <- writeInitConfig(workDir, config, false)
		}(config)
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	alreadyExists := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if strings.Contains(err.Error(), "already exists") {
			alreadyExists++
			continue
		}
		t.Fatalf("concurrent init returned unexpected error: %v", err)
	}
	if successes != 1 || alreadyExists != 1 {
		t.Fatalf("concurrent init results: successes=%d alreadyExists=%d, want one each", successes, alreadyExists)
	}

	got, err := os.ReadFile(filepath.Join(workDir, ".engram", "config.json"))
	if err != nil {
		t.Fatalf("read winning config: %v", err)
	}
	if string(got) != string(configs[0]) && string(got) != string(configs[1]) {
		t.Fatalf("winning config = %q, want one complete contender config", got)
	}
}

func TestWriteInitConfigForceRenameFailurePreservesExistingConfig(t *testing.T) {
	workDir := t.TempDir()
	configDir := filepath.Join(workDir, ".engram")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.json")
	oldConfig := []byte(`{"project_name":"old"}`)
	if err := os.WriteFile(configPath, oldConfig, 0o644); err != nil {
		t.Fatal(err)
	}

	oldRename := initConfigRename
	initConfigRename = func(_, _ string) error { return os.ErrPermission }
	t.Cleanup(func() { initConfigRename = oldRename })

	if err := writeInitConfig(workDir, []byte(`{"project_name":"new"}`), true); err == nil {
		t.Fatal("forced init succeeded despite publication failure")
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read original config: %v", err)
	}
	if string(got) != string(oldConfig) {
		t.Fatalf("config after failed publication = %q, want %q", got, oldConfig)
	}
	entries, err := os.ReadDir(configDir)
	if err != nil {
		t.Fatalf("read config directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("config directory entries after failed publication = %v, want only config.json", entries)
	}
}

func TestWriteInitConfigRejectsSymlinkedConfig(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "without force", true: "with force"}[force], func(t *testing.T) {
			workDir := t.TempDir()
			configDir := filepath.Join(workDir, ".engram")
			if err := os.Mkdir(configDir, 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(workDir, "target.json")
			if err := os.WriteFile(target, []byte(`{"project_name":"target"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(configDir, "config.json")); err != nil {
				t.Skipf("symlinks unavailable on this OS: %v", err)
			}

			err := writeInitConfig(workDir, []byte(`{"project_name":"new"}`), force)
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("symlinked config error = %v, want symlink rejection", err)
			}
			got, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatalf("read symlink target: %v", readErr)
			}
			if string(got) != `{"project_name":"target"}` {
				t.Fatalf("symlink target changed to %q", got)
			}
		})
	}
}

func TestWriteInitConfigRetainsStableParentDuringReplacement(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "without force", true: "with force"}[force], func(t *testing.T) {
			workDir := t.TempDir()
			configDir := filepath.Join(workDir, ".engram")
			if err := os.Mkdir(configDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if force {
				if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"project_name":"old"}`), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			externalDir := filepath.Join(workDir, "external")
			if err := os.Mkdir(externalDir, 0o755); err != nil {
				t.Fatal(err)
			}
			externalConfig := filepath.Join(externalDir, "config.json")
			externalContents := []byte(`{"project_name":"external"}`)
			if err := os.WriteFile(externalConfig, externalContents, 0o644); err != nil {
				t.Fatal(err)
			}

			oldAfterOpen := initConfigAfterOpen
			publishedDir := ""
			initConfigAfterOpen = func() {
				publishedDir = replaceInitConfigParentForTest(t, configDir, externalDir)
			}
			t.Cleanup(func() { initConfigAfterOpen = oldAfterOpen })

			if err := writeInitConfig(workDir, []byte(`{"project_name":"new"}`), force); err != nil {
				t.Fatalf("write init config after parent replacement: %v", err)
			}
			gotExternal, err := os.ReadFile(externalConfig)
			if err != nil {
				t.Fatalf("read external config: %v", err)
			}
			if string(gotExternal) != string(externalContents) {
				t.Fatalf("external config changed to %q, want %q", gotExternal, externalContents)
			}
			gotPublished, err := os.ReadFile(filepath.Join(publishedDir, "config.json"))
			if err != nil {
				t.Fatalf("read published config: %v", err)
			}
			if string(gotPublished) != `{"project_name":"new"}` {
				t.Fatalf("published config = %q, want new config", gotPublished)
			}
		})
	}
}

func TestWriteInitConfigRejectsNonDirectoryConfigDirectory(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, ".engram"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, force := range []bool{false, true} {
		err := writeInitConfig(workDir, []byte(`{"project_name":"new"}`), force)
		if err == nil || !strings.Contains(err.Error(), "must be a directory") {
			t.Fatalf("non-directory .engram error with force=%t = %v, want directory rejection", force, err)
		}
	}
}

func TestWriteInitConfigRejectsSymlinkedConfigDirectory(t *testing.T) {
	workDir := t.TempDir()
	targetDir := filepath.Join(workDir, "target")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, filepath.Join(workDir, ".engram")); err != nil {
		t.Skipf("symlinks unavailable on this OS: %v", err)
	}

	for _, force := range []bool{false, true} {
		err := writeInitConfig(workDir, []byte(`{"project_name":"new"}`), force)
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlinked .engram error with force=%t = %v, want symlink rejection", force, err)
		}
	}
	if _, err := os.Stat(filepath.Join(targetDir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("symlink target directory was written: %v", err)
	}
}

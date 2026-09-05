package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/project"
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

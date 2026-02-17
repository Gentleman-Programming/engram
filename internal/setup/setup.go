// Package setup handles agent plugin installation.
//
// Plugin files are embedded in the binary via go:embed so installation
// works from a Homebrew install or standalone binary — no repo clone needed.
package setup

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed plugins/opencode/*
var openCodeFS embed.FS

//go:embed all:plugins/claude-code/*
var claudeCodeFS embed.FS

// Agent represents a supported AI coding agent.
type Agent struct {
	Name        string
	Description string
	InstallDir  string // resolved at runtime
}

// Result holds the outcome of an installation.
type Result struct {
	Agent       string
	Destination string
	Files       int
}

// SupportedAgents returns the list of agents that have plugins available.
func SupportedAgents() []Agent {
	return []Agent{
		{
			Name:        "opencode",
			Description: "OpenCode — TypeScript plugin with session tracking, compaction recovery, and Memory Protocol",
			InstallDir:  openCodePluginDir(),
		},
		{
			Name:        "claude-code",
			Description: "Claude Code — Native plugin with hooks, skills, MCP registration, and compaction recovery",
			InstallDir:  claudeCodePluginDir(),
		},
	}
}

// Install copies the embedded plugin files for the given agent to its install directory.
func Install(agentName string) (*Result, error) {
	agents := SupportedAgents()
	var agent *Agent
	for i := range agents {
		if agents[i].Name == agentName {
			agent = &agents[i]
			break
		}
	}
	if agent == nil {
		return nil, fmt.Errorf("unknown agent: %q (supported: opencode, claude-code)", agentName)
	}

	switch agentName {
	case "opencode":
		return installOpenCode(agent)
	case "claude-code":
		return installClaudeCode(agent)
	default:
		return nil, fmt.Errorf("no installer for agent: %q", agentName)
	}
}

// ─── OpenCode ────────────────────────────────────────────────────────────────

func installOpenCode(agent *Agent) (*Result, error) {
	dir := agent.InstallDir
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create plugin dir %s: %w", dir, err)
	}

	data, err := openCodeFS.ReadFile("plugins/opencode/engram.ts")
	if err != nil {
		return nil, fmt.Errorf("read embedded engram.ts: %w", err)
	}

	dest := filepath.Join(dir, "engram.ts")
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return nil, fmt.Errorf("write %s: %w", dest, err)
	}

	return &Result{
		Agent:       agent.Name,
		Destination: dir,
		Files:       1,
	}, nil
}

// ─── Claude Code ─────────────────────────────────────────────────────────────

func installClaudeCode(agent *Agent) (*Result, error) {
	dir := agent.InstallDir
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create plugin dir %s: %w", dir, err)
	}

	fileCount := 0

	err := fs.WalkDir(claudeCodeFS, "plugins/claude-code", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Relative path from the embed root
		rel, err := filepath.Rel("plugins/claude-code", path)
		if err != nil {
			return err
		}

		target := filepath.Join(dir, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}

		data, err := claudeCodeFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}

		// Preserve executable bit for shell scripts
		perm := os.FileMode(0644)
		if filepath.Ext(path) == ".sh" {
			perm = 0755
		}

		if err := os.WriteFile(target, data, perm); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}

		fileCount++
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("install claude-code plugin: %w", err)
	}

	return &Result{
		Agent:       agent.Name,
		Destination: dir,
		Files:       fileCount,
	}, nil
}

// ─── Platform paths ──────────────────────────────────────────────────────────

func openCodePluginDir() string {
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "darwin", "linux":
		// XDG_CONFIG_HOME or default
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "opencode", "plugins")
		}
		return filepath.Join(home, ".config", "opencode", "plugins")
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "opencode", "plugins")
		}
		return filepath.Join(home, "AppData", "Roaming", "opencode", "plugins")
	default:
		return filepath.Join(home, ".config", "opencode", "plugins")
	}
}

func claudeCodePluginDir() string {
	home, _ := os.UserHomeDir()

	// Claude Code plugins live under ~/.claude/plugins/<plugin-name>/
	return filepath.Join(home, ".claude", "plugins", "engram")
}

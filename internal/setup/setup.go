// Package setup handles agent plugin installation.
//
// - OpenCode: copies embedded plugin file to ~/.config/opencode/plugins/
// - Claude Code: runs `claude plugin marketplace add` + `claude plugin install`
package setup

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed plugins/opencode/*
var openCodeFS embed.FS

// Agent represents a supported AI coding agent.
type Agent struct {
	Name        string
	Description string
	InstallDir  string // resolved at runtime (display only for claude-code)
}

// Result holds the outcome of an installation.
type Result struct {
	Agent       string
	Destination string
	Files       int
}

const claudeCodeMarketplace = "Gentleman-Programming/engram"

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
			Description: "Claude Code — Native plugin via marketplace (hooks, skills, MCP, compaction recovery)",
			InstallDir:  "managed by claude plugin system",
		},
	}
}

// Install installs the plugin for the given agent.
func Install(agentName string) (*Result, error) {
	switch agentName {
	case "opencode":
		return installOpenCode()
	case "claude-code":
		return installClaudeCode()
	default:
		return nil, fmt.Errorf("unknown agent: %q (supported: opencode, claude-code)", agentName)
	}
}

// ─── OpenCode ────────────────────────────────────────────────────────────────

func installOpenCode() (*Result, error) {
	dir := openCodePluginDir()
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
		Agent:       "opencode",
		Destination: dir,
		Files:       1,
	}, nil
}

// ─── Claude Code ─────────────────────────────────────────────────────────────

func installClaudeCode() (*Result, error) {
	// Check that claude CLI is available
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("claude CLI not found in PATH — install Claude Code first: https://docs.anthropic.com/en/docs/claude-code")
	}

	// Step 1: Add marketplace (idempotent — if already added, claude will say so)
	addCmd := exec.Command(claudeBin, "plugin", "marketplace", "add", claudeCodeMarketplace)
	addOut, err := addCmd.CombinedOutput()
	addOutputStr := strings.TrimSpace(string(addOut))
	if err != nil {
		// If marketplace is already added, that's fine
		if !strings.Contains(addOutputStr, "already") {
			return nil, fmt.Errorf("marketplace add failed: %s", addOutputStr)
		}
	}

	// Step 2: Install the plugin
	installCmd := exec.Command(claudeBin, "plugin", "install", "engram")
	installOut, err := installCmd.CombinedOutput()
	installOutputStr := strings.TrimSpace(string(installOut))
	if err != nil {
		// If plugin is already installed, that's fine
		if !strings.Contains(installOutputStr, "already") {
			return nil, fmt.Errorf("plugin install failed: %s", installOutputStr)
		}
	}

	return &Result{
		Agent:       "claude-code",
		Destination: "claude plugin system (managed by Claude Code)",
		Files:       0, // managed by claude, not by us
	}, nil
}

// ─── Platform paths ──────────────────────────────────────────────────────────

func openCodePluginDir() string {
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "darwin", "linux":
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

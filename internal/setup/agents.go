package setup

import (
	"os"
	"path/filepath"
)

// agentAdapters returns the full agent registry in display order.
//
// Existing agents with bespoke install needs (embedded plugin, package manager,
// CLI bootstrapping, TOML config) keep their hand-written installers via `custom`.
// Declarative agents only describe where their MCP config and instruction surfaces
// live; the generic injectMCP / writeInstruction machinery in registry.go does the
// rest. Adding a new agent of the declarative kind is just another entry here.
func agentAdapters() []agentAdapter {
	return []agentAdapter{
		{
			slug:        "opencode",
			description: "OpenCode — TypeScript plugin with session tracking, compaction recovery, and Memory Protocol",
			custom:      installOpenCode,
			installDir:  openCodePluginDir,
		},
		{
			slug:        "pi",
			description: "Pi — gentle-engram package plus pi-mcp-adapter MCP tools",
			custom:      installPi,
			installDir:  piAgentDir,
			postInstall: []string{
				"Restart Pi so packages and MCP config are reloaded",
				"Verify with: pi list",
			},
		},
		{
			slug:        "claude-code",
			description: "Claude Code — Native plugin via marketplace (hooks, skills, MCP, compaction recovery)",
			custom:      installClaudeCode,
			installDir:  func() string { return "managed by claude plugin system" },
		},
		{
			slug:        "gemini-cli",
			description: "Gemini CLI — MCP registration plus system prompt compaction recovery",
			custom:      installGeminiCLI,
			installDir:  geminiConfigPath,
			postInstall: []string{
				"Restart Gemini CLI so MCP config is reloaded",
				"Verify ~/.gemini/settings.json includes mcpServers.engram",
				"Verify ~/.gemini/system.md + ~/.gemini/.env exist for compaction recovery",
			},
		},
		{
			slug:        "codex",
			description: "Codex — MCP registration plus model/compaction instruction files",
			custom:      installCodex,
			installDir:  codexConfigPath,
			postInstall: []string{
				"Restart Codex so MCP config is reloaded",
				"Verify ~/.codex/config.toml has [mcp_servers.engram]",
				"Verify model_instructions_file + experimental_compact_prompt_file are set",
			},
		},
		{
			slug:        "antigravity-cli",
			description: "Antigravity CLI — MCP registration in the shared ~/.gemini config plus GEMINI.md Memory Protocol",
			mcpPath:     antigravityMCPConfigPath,
			mcpFormat:   mcpServersObject,
			instructions: []instrSurface{
				{path: antigravityContextPath, style: markerBlock, body: memoryProtocolMarkdown},
			},
			postInstall: []string{
				"Restart Antigravity so the shared MCP config is reloaded",
				"Verify ~/.gemini/config/mcp_config.json includes mcpServers.engram",
				"Verify ~/.gemini/GEMINI.md has the Engram Memory Protocol block",
			},
		},
		{
			slug:        "windsurf",
			description: "Windsurf (Cascade) — MCP registration plus global_rules.md Memory Protocol",
			mcpPath:     windsurfMCPPath,
			mcpFormat:   mcpServersObject,
			instructions: []instrSurface{
				{path: windsurfRulesPath, style: markerBlock, body: memoryProtocolMarkdown},
			},
			postInstall: []string{
				"Restart Windsurf so Cascade reloads MCP config",
				"Verify ~/.codeium/windsurf/mcp_config.json includes mcpServers.engram",
				"Verify ~/.codeium/windsurf/memories/global_rules.md has the Memory Protocol block",
			},
		},
		{
			slug:        "qwen",
			description: "Qwen Code — MCP registration in ~/.qwen/settings.json plus QWEN.md Memory Protocol",
			mcpPath:     qwenSettingsPath,
			mcpFormat:   mcpServersObject,
			instructions: []instrSurface{
				{path: qwenContextPath, style: markerBlock, body: memoryProtocolMarkdown},
			},
			postInstall: []string{
				"Restart Qwen Code so MCP config is reloaded",
				"Verify ~/.qwen/settings.json includes mcpServers.engram",
				"Verify ~/.qwen/QWEN.md has the Engram Memory Protocol block",
			},
		},
		{
			slug:        "kiro",
			description: "Kiro IDE — MCP registration in ~/.kiro/settings/mcp.json plus steering Memory Protocol",
			mcpPath:     kiroMCPPath,
			mcpFormat:   mcpServersObject,
			instructions: []instrSurface{
				{path: kiroSteeringPath, style: markerBlock, body: memoryProtocolMarkdown},
			},
			postInstall: []string{
				"Restart Kiro so MCP config is reloaded",
				"Verify ~/.kiro/settings/mcp.json includes mcpServers.engram",
				"Verify ~/.kiro/steering/engram.md has the Memory Protocol block",
			},
		},
		{
			slug:        "cursor",
			description: "Cursor — MCP registration in ~/.cursor/mcp.json plus an always-applied .mdc rule",
			mcpPath:     cursorMCPPath,
			mcpFormat:   mcpServersObject,
			instructions: []instrSurface{
				{path: cursorRulesPath, style: wholeFile, body: cursorRulesBody()},
			},
			postInstall: []string{
				"Restart Cursor so MCP config is reloaded",
				"Verify ~/.cursor/mcp.json includes mcpServers.engram",
				"Verify ~/.cursor/rules/engram.mdc exists (always-applied rule)",
			},
		},
		{
			slug:        "vscode-copilot",
			description: "VS Code (Copilot) — MCP registration in the User mcp.json plus a Copilot instructions file",
			mcpPath:     vscodeMCPPath,
			mcpFormat:   serversObject,
			instructions: []instrSurface{
				{path: vscodePromptPath, style: wholeFile, body: vscodeInstructionsBody()},
			},
			postInstall: []string{
				"Restart VS Code so Copilot reloads MCP config",
				"Verify <VS Code User>/mcp.json includes servers.engram",
				"Verify <VS Code User>/prompts/engram.instructions.md exists",
			},
		},
		{
			slug:        "kilocode",
			description: "Kilo Code — MCP registration in ~/.config/kilo/opencode.json plus AGENTS.md Memory Protocol",
			mcpPath:     kilocodeConfigPath,
			mcpFormat:   opencodeObject,
			instructions: []instrSurface{
				{path: kilocodeAgentsPath, style: markerBlock, body: memoryProtocolMarkdown},
			},
			postInstall: []string{
				"Restart Kilo Code so MCP config is reloaded",
				"Verify ~/.config/kilo/opencode.json includes mcp.engram",
				"Verify ~/.config/kilo/AGENTS.md has the Memory Protocol block",
			},
		},
	}
}

// cursorRulesBody wraps the Memory Protocol in the YAML frontmatter Cursor needs
// for an always-applied rule (alwaysApply:true ignores globs and attaches the rule
// regardless of context).
func cursorRulesBody() string {
	return "---\ndescription: Engram persistent memory protocol\nalwaysApply: true\n---\n\n" + memoryProtocolMarkdown
}

// vscodeInstructionsBody wraps the Memory Protocol in the frontmatter VS Code
// Copilot uses for a user-level instructions file that applies to every file.
func vscodeInstructionsBody() string {
	return "---\napplyTo: \"**\"\n---\n\n" + memoryProtocolMarkdown
}

// ─── Antigravity CLI paths ───────────────────────────────────────────────────
//
// Antigravity (CLI and IDE) read MCP servers from the shared ~/.gemini/config/
// mcp_config.json (top-level "mcpServers") and global instructions from
// ~/.gemini/GEMINI.md. These live under ~/.gemini but are distinct from the
// Gemini CLI's own settings.json / system.md surfaces.

func antigravityMCPConfigPath() string {
	home, _ := userHome()
	return filepath.Join(home, ".gemini", "config", "mcp_config.json")
}

func antigravityContextPath() string {
	home, _ := userHome()
	return filepath.Join(home, ".gemini", "GEMINI.md")
}

// ─── Windsurf paths ──────────────────────────────────────────────────────────

func windsurfMCPPath() string {
	home, _ := userHome()
	return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
}

func windsurfRulesPath() string {
	home, _ := userHome()
	return filepath.Join(home, ".codeium", "windsurf", "memories", "global_rules.md")
}

// ─── Qwen Code paths ─────────────────────────────────────────────────────────

func qwenSettingsPath() string {
	home, _ := userHome()
	return filepath.Join(home, ".qwen", "settings.json")
}

func qwenContextPath() string {
	home, _ := userHome()
	return filepath.Join(home, ".qwen", "QWEN.md")
}

// ─── Kiro IDE paths ──────────────────────────────────────────────────────────
//
// Kiro uses a split layout: MCP config and steering both live under ~/.kiro/
// regardless of where the IDE keeps its app settings.

func kiroMCPPath() string {
	home, _ := userHome()
	return filepath.Join(home, ".kiro", "settings", "mcp.json")
}

func kiroSteeringPath() string {
	home, _ := userHome()
	return filepath.Join(home, ".kiro", "steering", "engram.md")
}

// ─── Cursor paths ────────────────────────────────────────────────────────────

func cursorMCPPath() string {
	home, _ := userHome()
	return filepath.Join(home, ".cursor", "mcp.json")
}

func cursorRulesPath() string {
	home, _ := userHome()
	return filepath.Join(home, ".cursor", "rules", "engram.mdc")
}

// ─── VS Code (Copilot) paths ─────────────────────────────────────────────────

// vscodeUserDir returns the platform-specific VS Code "User" config directory.
func vscodeUserDir() string {
	home, _ := userHome()
	switch runtimeGOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "Code", "User")
		}
		return filepath.Join(home, "AppData", "Roaming", "Code", "User")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User")
	default:
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "Code", "User")
		}
		return filepath.Join(home, ".config", "Code", "User")
	}
}

func vscodeMCPPath() string {
	return filepath.Join(vscodeUserDir(), "mcp.json")
}

func vscodePromptPath() string {
	return filepath.Join(vscodeUserDir(), "prompts", "engram.instructions.md")
}

// ─── Kilo Code paths ─────────────────────────────────────────────────────────
//
// Kilo Code follows OpenCode's config model (top-level "mcp" object) but under
// its own ~/.config/kilo/ root.

func kilocodeConfigDir() string {
	home, _ := userHome()
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "kilo")
	}
	return filepath.Join(home, ".config", "kilo")
}

func kilocodeConfigPath() string {
	return filepath.Join(kilocodeConfigDir(), "opencode.json")
}

func kilocodeAgentsPath() string {
	return filepath.Join(kilocodeConfigDir(), "AGENTS.md")
}

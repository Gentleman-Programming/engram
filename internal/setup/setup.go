// Package setup handles agent plugin installation.
//
// - OpenCode: copies embedded plugin file to ~/.config/opencode/plugins/
// - Claude Code: runs `claude plugin marketplace add` + `claude plugin install`
// - Gemini CLI: injects MCP registration in ~/.gemini/settings.json
// - Codex: injects MCP registration in ~/.codex/config.toml
package setup

import (
	"embed"
	"encoding/json"
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
const codexEngramBlock = "[mcp_servers.engram]\ncommand = \"engram\"\nargs = [\"mcp\"]"

const memoryProtocolMarkdown = `## Engram Persistent Memory — Protocol

You have access to Engram, a persistent memory system that survives across sessions and compactions.

### WHEN TO SAVE (mandatory — not optional)

Call mem_save IMMEDIATELY after any of these:
- Bug fix completed
- Architecture or design decision made
- Non-obvious discovery about the codebase
- Configuration change or environment setup
- Pattern established (naming, structure, convention)
- User preference or constraint learned

Format for mem_save:
- **title**: Verb + what — short, searchable (e.g. "Fixed N+1 query in UserList", "Chose Zustand over Redux")
- **type**: bugfix | decision | architecture | discovery | pattern | config | preference
- **content**:
  **What**: One sentence — what was done
  **Why**: What motivated it (user request, bug, performance, etc.)
  **Where**: Files or paths affected
  **Learned**: Gotchas, edge cases, things that surprised you (omit if none)

### WHEN TO SEARCH MEMORY

When the user asks to recall something — any variation of "remember", "recall", "what did we do",
"how did we solve", "recordar", "acordate", "qué hicimos", or references to past work:
1. First call mem_context — checks recent session history (fast, cheap)
2. If not found, call mem_search with relevant keywords (FTS5 full-text search)
3. If you find a match, use mem_get_observation for full untruncated content

Also search memory PROACTIVELY when:
- Starting work on something that might have been done before
- The user mentions a topic you have no context on — check if past sessions covered it

### SESSION CLOSE PROTOCOL (mandatory)

Before ending a session or saying "done" / "listo" / "that's it", you MUST:
1. Call mem_session_summary with this structure:

## Goal
[What we were working on this session]

## Instructions
[User preferences or constraints discovered — skip if none]

## Discoveries
- [Technical findings, gotchas, non-obvious learnings]

## Accomplished
- [Completed items with key details]

## Next Steps
- [What remains to be done — for the next session]

## Relevant Files
- path/to/file — [what it does or what changed]

This is NOT optional. If you skip this, the next session starts blind.

### AFTER COMPACTION

If you see a message about compaction or context reset, or if you see "FIRST ACTION REQUIRED" in your context:
1. IMMEDIATELY call mem_session_summary with the compacted summary content — this persists what was done before compaction
2. Then call mem_context to recover any additional context from previous sessions
3. Only THEN continue working

Do not skip step 1. Without it, everything done before compaction is lost from memory.
`

const codexCompactPromptMarkdown = `You are compacting a coding session that uses Engram persistent memory.

You MUST prepend this exact sentence at the top of the compacted summary:

FIRST ACTION REQUIRED: Call mem_session_summary with the content of this compacted summary before doing anything else, then call mem_context.

After that sentence, summarize:
- Goal
- Key technical discoveries and decisions
- Completed work
- Remaining next steps
- Relevant files changed

Keep it concise and high-signal.`

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
		{
			Name:        "gemini-cli",
			Description: "Gemini CLI — MCP registration plus system prompt compaction recovery",
			InstallDir:  geminiConfigPath(),
		},
		{
			Name:        "codex",
			Description: "Codex — MCP registration plus model/compaction instruction files",
			InstallDir:  codexConfigPath(),
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
	case "gemini-cli":
		return installGeminiCLI()
	case "codex":
		return installCodex()
	default:
		return nil, fmt.Errorf("unknown agent: %q (supported: opencode, claude-code, gemini-cli, codex)", agentName)
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

	// Register engram MCP server in opencode.json
	files := 1
	if err := injectOpenCodeMCP(); err != nil {
		// Non-fatal: plugin works, MCP just needs manual config
		fmt.Fprintf(os.Stderr, "warning: could not auto-register MCP server in opencode.json: %v\n", err)
		fmt.Fprintf(os.Stderr, "  Add manually to your opencode.json under \"mcp\":\n")
		fmt.Fprintf(os.Stderr, "  \"engram\": { \"type\": \"local\", \"command\": [\"engram\", \"mcp\"], \"enabled\": true }\n")
	} else {
		files = 2
	}

	return &Result{
		Agent:       "opencode",
		Destination: dir,
		Files:       files,
	}, nil
}

// injectOpenCodeMCP adds the engram MCP server entry to opencode.json.
// It reads the existing config, adds/updates the engram entry under "mcp",
// and writes it back preserving all other settings.
func injectOpenCodeMCP() error {
	configPath := openCodeConfigPath()

	// Read existing config (or start with empty object)
	var config map[string]json.RawMessage
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			config = make(map[string]json.RawMessage)
		} else {
			return fmt.Errorf("read config: %w", err)
		}
	} else {
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	}

	// Parse or create the "mcp" block
	var mcpBlock map[string]json.RawMessage
	if raw, exists := config["mcp"]; exists {
		if err := json.Unmarshal(raw, &mcpBlock); err != nil {
			return fmt.Errorf("parse mcp block: %w", err)
		}
	} else {
		mcpBlock = make(map[string]json.RawMessage)
	}

	// Check if engram is already registered
	if _, exists := mcpBlock["engram"]; exists {
		return nil // already registered, nothing to do
	}

	// Add engram MCP entry
	engramEntry := map[string]interface{}{
		"type":    "local",
		"command": []string{"engram", "mcp"},
		"enabled": true,
	}
	entryJSON, err := json.Marshal(engramEntry)
	if err != nil {
		return fmt.Errorf("marshal engram entry: %w", err)
	}
	mcpBlock["engram"] = json.RawMessage(entryJSON)

	// Write mcp block back to config
	mcpJSON, err := json.Marshal(mcpBlock)
	if err != nil {
		return fmt.Errorf("marshal mcp block: %w", err)
	}
	config["mcp"] = json.RawMessage(mcpJSON)

	// Write config back with indentation
	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, output, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

// openCodeConfigPath returns the path to opencode.json.
func openCodeConfigPath() string {
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "darwin", "linux":
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "opencode", "opencode.json")
		}
		return filepath.Join(home, ".config", "opencode", "opencode.json")
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "opencode", "opencode.json")
		}
		return filepath.Join(home, "AppData", "Roaming", "opencode", "opencode.json")
	default:
		return filepath.Join(home, ".config", "opencode", "opencode.json")
	}
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

// ─── Gemini CLI ──────────────────────────────────────────────────────────────

func installGeminiCLI() (*Result, error) {
	path := geminiConfigPath()
	if err := injectGeminiMCP(path); err != nil {
		return nil, err
	}

	if err := writeGeminiSystemPrompt(); err != nil {
		return nil, err
	}

	if err := ensureGeminiEnvOverride(); err != nil {
		return nil, err
	}

	return &Result{
		Agent:       "gemini-cli",
		Destination: filepath.Dir(path),
		Files:       3,
	}, nil
}

func injectGeminiMCP(configPath string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	var config map[string]json.RawMessage
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			config = make(map[string]json.RawMessage)
		} else {
			return fmt.Errorf("read config: %w", err)
		}
	} else {
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	}

	var mcpServers map[string]json.RawMessage
	if raw, exists := config["mcpServers"]; exists {
		if err := json.Unmarshal(raw, &mcpServers); err != nil {
			return fmt.Errorf("parse mcpServers block: %w", err)
		}
	} else {
		mcpServers = make(map[string]json.RawMessage)
	}

	engramEntry := map[string]any{
		"command": "engram",
		"args":    []string{"mcp"},
	}
	entryJSON, err := json.Marshal(engramEntry)
	if err != nil {
		return fmt.Errorf("marshal engram entry: %w", err)
	}
	mcpServers["engram"] = json.RawMessage(entryJSON)

	mcpJSON, err := json.Marshal(mcpServers)
	if err != nil {
		return fmt.Errorf("marshal mcpServers block: %w", err)
	}
	config["mcpServers"] = json.RawMessage(mcpJSON)

	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, output, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

func writeGeminiSystemPrompt() error {
	systemPath := geminiSystemPromptPath()
	if err := os.MkdirAll(filepath.Dir(systemPath), 0755); err != nil {
		return fmt.Errorf("create gemini system prompt dir: %w", err)
	}

	if err := os.WriteFile(systemPath, []byte(memoryProtocolMarkdown), 0644); err != nil {
		return fmt.Errorf("write gemini system prompt: %w", err)
	}

	return nil
}

func ensureGeminiEnvOverride() error {
	envPath := geminiEnvPath()
	if err := os.MkdirAll(filepath.Dir(envPath), 0755); err != nil {
		return fmt.Errorf("create gemini env dir: %w", err)
	}

	line := "GEMINI_SYSTEM_MD=1"
	content, err := os.ReadFile(envPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read gemini env file: %w", err)
	}

	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	for _, existing := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(existing)
		if trimmed == line || strings.HasPrefix(trimmed, "GEMINI_SYSTEM_MD=") {
			if trimmed != line {
				text = strings.ReplaceAll(text, existing, line)
				if err := os.WriteFile(envPath, []byte(strings.TrimSpace(text)+"\n"), 0644); err != nil {
					return fmt.Errorf("write gemini env file: %w", err)
				}
			}
			return nil
		}
	}

	if strings.TrimSpace(text) == "" {
		text = line + "\n"
	} else {
		text = strings.TrimRight(text, "\n") + "\n" + line + "\n"
	}

	if err := os.WriteFile(envPath, []byte(text), 0644); err != nil {
		return fmt.Errorf("write gemini env file: %w", err)
	}

	return nil
}

// ─── Codex ───────────────────────────────────────────────────────────────────

func installCodex() (*Result, error) {
	path := codexConfigPath()

	instructionsPath, err := writeCodexMemoryInstructionFiles()
	if err != nil {
		return nil, err
	}

	if err := injectCodexMCP(path); err != nil {
		return nil, err
	}

	compactPromptPath := codexCompactPromptPath()
	if err := injectCodexMemoryConfig(path, instructionsPath, compactPromptPath); err != nil {
		return nil, err
	}

	return &Result{
		Agent:       "codex",
		Destination: filepath.Dir(path),
		Files:       3,
	}, nil
}

func injectCodexMCP(configPath string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config: %w", err)
	}

	updated := upsertCodexEngramBlock(string(data))
	if err := os.WriteFile(configPath, []byte(updated), 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

func writeCodexMemoryInstructionFiles() (string, error) {
	instructionsPath := codexInstructionsPath()
	if err := os.MkdirAll(filepath.Dir(instructionsPath), 0755); err != nil {
		return "", fmt.Errorf("create codex instructions dir: %w", err)
	}

	if err := os.WriteFile(instructionsPath, []byte(memoryProtocolMarkdown), 0644); err != nil {
		return "", fmt.Errorf("write codex instructions: %w", err)
	}

	compactPath := codexCompactPromptPath()
	if err := os.WriteFile(compactPath, []byte(codexCompactPromptMarkdown), 0644); err != nil {
		return "", fmt.Errorf("write codex compact prompt: %w", err)
	}

	return instructionsPath, nil
}

func injectCodexMemoryConfig(configPath, instructionsPath, compactPromptPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			data = nil
		} else {
			return fmt.Errorf("read config: %w", err)
		}
	}

	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	content = upsertTopLevelTOMLString(content, "model_instructions_file", instructionsPath)
	content = upsertTopLevelTOMLString(content, "experimental_compact_prompt_file", compactPromptPath)

	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

func upsertCodexEngramBlock(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")

	var kept []string
	for i := 0; i < len(lines); {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "[mcp_servers.engram]" {
			i++
			for i < len(lines) {
				next := strings.TrimSpace(lines[i])
				if strings.HasPrefix(next, "[") && strings.HasSuffix(next, "]") {
					break
				}
				i++
			}
			continue
		}

		kept = append(kept, lines[i])
		i++
	}

	base := strings.TrimSpace(strings.Join(kept, "\n"))
	if base == "" {
		return codexEngramBlock + "\n"
	}

	return base + "\n\n" + codexEngramBlock + "\n"
}

func upsertTopLevelTOMLString(content, key, value string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	lineValue := fmt.Sprintf("%s = %q", key, value)

	var cleaned []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+" ") || strings.HasPrefix(trimmed, key+"=") {
			continue
		}
		cleaned = append(cleaned, line)
	}

	insertAt := len(cleaned)
	for i, line := range cleaned {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			insertAt = i
			break
		}
	}

	var out []string
	out = append(out, cleaned[:insertAt]...)
	out = append(out, lineValue)
	out = append(out, cleaned[insertAt:]...)

	base := strings.TrimSpace(strings.Join(out, "\n"))
	if base == "" {
		return ""
	}

	return base + "\n"
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

func geminiConfigPath() string {
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "gemini", "settings.json")
		}
		return filepath.Join(home, "AppData", "Roaming", "gemini", "settings.json")
	default:
		return filepath.Join(home, ".gemini", "settings.json")
	}
}

func geminiSystemPromptPath() string {
	return filepath.Join(filepath.Dir(geminiConfigPath()), "system.md")
}

func geminiEnvPath() string {
	return filepath.Join(filepath.Dir(geminiConfigPath()), ".env")
}

func codexConfigPath() string {
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "codex", "config.toml")
		}
		return filepath.Join(home, "AppData", "Roaming", "codex", "config.toml")
	default:
		return filepath.Join(home, ".codex", "config.toml")
	}
}

func codexInstructionsPath() string {
	return filepath.Join(filepath.Dir(codexConfigPath()), "engram-instructions.md")
}

func codexCompactPromptPath() string {
	return filepath.Join(filepath.Dir(codexConfigPath()), "engram-compact-prompt.md")
}

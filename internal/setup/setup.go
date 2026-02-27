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

var (
	runtimeGOOS = runtime.GOOS
	userHomeDir = os.UserHomeDir
	lookPathFn  = exec.LookPath
	runCommand  = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput()
	}
	openCodeReadFile = func(path string) ([]byte, error) {
		return openCodeFS.ReadFile(path)
	}
	openCodeWriteFileFn                = os.WriteFile
	readFileFn                         = os.ReadFile
	writeFileFn                        = os.WriteFile
	jsonMarshalFn                      = json.Marshal
	jsonMarshalIndentFn                = json.MarshalIndent
	injectOpenCodeMCPFn                = injectOpenCodeMCP
	injectGeminiMCPFn                  = injectGeminiMCP
	writeGeminiSystemPromptFn          = writeGeminiSystemPrompt
	ensureGeminiEnvOverrideFn          = ensureGeminiEnvOverride
	writeCodexMemoryInstructionFilesFn = writeCodexMemoryInstructionFiles
	injectCodexMCPFn                   = injectCodexMCP
	injectCodexMemoryConfigFn          = injectCodexMemoryConfig
	addClaudeCodeAllowlistFn           = AddClaudeCodeAllowlist
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

// claudeCodeMCPTools are the MCP tool names registered by the engram plugin
// in Claude Code. Adding these to ~/.claude/settings.json permissions.allow
// prevents Claude Code from prompting for confirmation on every tool call.
var claudeCodeMCPTools = []string{
	"mcp__plugin_engram_engram__mem_capture_passive",
	"mcp__plugin_engram_engram__mem_context",
	"mcp__plugin_engram_engram__mem_get_observation",
	"mcp__plugin_engram_engram__mem_save",
	"mcp__plugin_engram_engram__mem_save_prompt",
	"mcp__plugin_engram_engram__mem_search",
	"mcp__plugin_engram_engram__mem_session_end",
	"mcp__plugin_engram_engram__mem_session_start",
	"mcp__plugin_engram_engram__mem_session_summary",
	"mcp__plugin_engram_engram__mem_suggest_topic_key",
	"mcp__plugin_engram_engram__mem_update",
}
const codexEngramBlock = "[mcp_servers.engram]\ncommand = \"engram\"\nargs = [\"mcp\", \"--tools=agent\"]"

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

### PASSIVE CAPTURE — automatic learning extraction

When completing a task or subtask, include a "## Key Learnings:" section at the end of your response
with numbered items. Engram will automatically extract and save these as observations.

Example:
## Key Learnings:

1. bcrypt cost=12 is the right balance for our server performance
2. JWT refresh tokens need atomic rotation to prevent race conditions

You can also call mem_capture_passive(content) directly with any text that contains a learning section.
This is a safety net — it captures knowledge even if you forget to call mem_save explicitly.

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

	data, err := openCodeReadFile("plugins/opencode/engram.ts")
	if err != nil {
		return nil, fmt.Errorf("read embedded engram.ts: %w", err)
	}

	dest := filepath.Join(dir, "engram.ts")
	if err := openCodeWriteFileFn(dest, data, 0644); err != nil {
		return nil, fmt.Errorf("write %s: %w", dest, err)
	}

	// Register engram MCP server in opencode.json
	files := 1
	if err := injectOpenCodeMCPFn(); err != nil {
		// Non-fatal: plugin works, MCP just needs manual config
		fmt.Fprintf(os.Stderr, "warning: could not auto-register MCP server in opencode.json: %v\n", err)
		fmt.Fprintf(os.Stderr, "  Add manually to your opencode.json under \"mcp\":\n")
		fmt.Fprintf(os.Stderr, "  \"engram\": { \"type\": \"local\", \"command\": [\"engram\", \"mcp\", \"--tools=agent\"], \"enabled\": true }\n")
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
	data, err := readFileFn(configPath)
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

	// Add engram MCP entry (agent profile — only tools agents need)
	engramEntry := map[string]interface{}{
		"type":    "local",
		"command": []string{"engram", "mcp", "--tools=agent"},
		"enabled": true,
	}
	entryJSON, err := jsonMarshalFn(engramEntry)
	if err != nil {
		return fmt.Errorf("marshal engram entry: %w", err)
	}
	mcpBlock["engram"] = json.RawMessage(entryJSON)

	// Write mcp block back to config
	mcpJSON, err := jsonMarshalFn(mcpBlock)
	if err != nil {
		return fmt.Errorf("marshal mcp block: %w", err)
	}
	config["mcp"] = json.RawMessage(mcpJSON)

	// Write config back with indentation
	output, err := jsonMarshalIndentFn(config, "", "  ")
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
	home, _ := userHomeDir()

	switch runtimeGOOS {
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
	claudeBin, err := lookPathFn("claude")
	if err != nil {
		return nil, fmt.Errorf("claude CLI not found in PATH — install Claude Code first: https://docs.anthropic.com/en/docs/claude-code")
	}

	// Step 1: Add marketplace (idempotent — if already added, claude will say so)
	addOut, err := runCommand(claudeBin, "plugin", "marketplace", "add", claudeCodeMarketplace)
	addOutputStr := strings.TrimSpace(string(addOut))
	if err != nil {
		// If marketplace is already added, that's fine
		if !strings.Contains(addOutputStr, "already") {
			return nil, fmt.Errorf("marketplace add failed: %s", addOutputStr)
		}
	}

	// Step 2: Install the plugin
	installOut, err := runCommand(claudeBin, "plugin", "install", "engram")
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

func claudeCodeSettingsPath() string {
	home, _ := userHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

// AddClaudeCodeAllowlist adds engram MCP tool names to ~/.claude/settings.json
// permissions.allow so Claude Code doesn't prompt for confirmation on each call.
// Idempotent: skips tools already present in the list.
func AddClaudeCodeAllowlist() error {
	settingsPath := claudeCodeSettingsPath()

	// Read existing settings (or start fresh)
	var config map[string]json.RawMessage
	data, err := readFileFn(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			config = make(map[string]json.RawMessage)
		} else {
			return fmt.Errorf("read settings: %w", err)
		}
	} else {
		if err := json.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("parse settings: %w", err)
		}
	}

	// Parse or create permissions block
	var permissions map[string]json.RawMessage
	if raw, exists := config["permissions"]; exists {
		if err := json.Unmarshal(raw, &permissions); err != nil {
			return fmt.Errorf("parse permissions: %w", err)
		}
	} else {
		permissions = make(map[string]json.RawMessage)
	}

	// Parse or create allow list
	var allowList []string
	if raw, exists := permissions["allow"]; exists {
		if err := json.Unmarshal(raw, &allowList); err != nil {
			return fmt.Errorf("parse allow list: %w", err)
		}
	}

	// Build set of existing entries for O(1) lookup
	existing := make(map[string]bool, len(allowList))
	for _, entry := range allowList {
		existing[entry] = true
	}

	// Add only missing tools
	added := 0
	for _, tool := range claudeCodeMCPTools {
		if !existing[tool] {
			allowList = append(allowList, tool)
			added++
		}
	}

	if added == 0 {
		return nil // all tools already present
	}

	// Write back
	allowJSON, err := jsonMarshalFn(allowList)
	if err != nil {
		return fmt.Errorf("marshal allow list: %w", err)
	}
	permissions["allow"] = json.RawMessage(allowJSON)

	permJSON, err := jsonMarshalFn(permissions)
	if err != nil {
		return fmt.Errorf("marshal permissions: %w", err)
	}
	config["permissions"] = json.RawMessage(permJSON)

	output, err := jsonMarshalIndentFn(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	// Ensure ~/.claude/ directory exists
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}

	if err := writeFileFn(settingsPath, output, 0644); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}

	return nil
}

// ─── Gemini CLI ──────────────────────────────────────────────────────────────

func installGeminiCLI() (*Result, error) {
	path := geminiConfigPath()
	if err := injectGeminiMCPFn(path); err != nil {
		return nil, err
	}

	if err := writeGeminiSystemPromptFn(); err != nil {
		return nil, err
	}

	if err := ensureGeminiEnvOverrideFn(); err != nil {
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
		"args":    []string{"mcp", "--tools=agent"},
	}
	entryJSON, err := jsonMarshalFn(engramEntry)
	if err != nil {
		return fmt.Errorf("marshal engram entry: %w", err)
	}
	mcpServers["engram"] = json.RawMessage(entryJSON)

	mcpJSON, err := jsonMarshalFn(mcpServers)
	if err != nil {
		return fmt.Errorf("marshal mcpServers block: %w", err)
	}
	config["mcpServers"] = json.RawMessage(mcpJSON)

	output, err := jsonMarshalIndentFn(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := writeFileFn(configPath, output, 0644); err != nil {
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
	content, err := readFileFn(envPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read gemini env file: %w", err)
	}

	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	for _, existing := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(existing)
		if trimmed == line || strings.HasPrefix(trimmed, "GEMINI_SYSTEM_MD=") {
			if trimmed != line {
				text = strings.ReplaceAll(text, existing, line)
				if err := writeFileFn(envPath, []byte(strings.TrimSpace(text)+"\n"), 0644); err != nil {
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

	if err := writeFileFn(envPath, []byte(text), 0644); err != nil {
		return fmt.Errorf("write gemini env file: %w", err)
	}

	return nil
}

// ─── Codex ───────────────────────────────────────────────────────────────────

func installCodex() (*Result, error) {
	path := codexConfigPath()

	instructionsPath, err := writeCodexMemoryInstructionFilesFn()
	if err != nil {
		return nil, err
	}

	if err := injectCodexMCPFn(path); err != nil {
		return nil, err
	}

	compactPromptPath := codexCompactPromptPath()
	if err := injectCodexMemoryConfigFn(path, instructionsPath, compactPromptPath); err != nil {
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

	data, err := readFileFn(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config: %w", err)
	}

	updated := upsertCodexEngramBlock(string(data))
	if err := writeFileFn(configPath, []byte(updated), 0644); err != nil {
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
	data, err := readFileFn(configPath)
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

	if err := writeFileFn(configPath, []byte(content), 0644); err != nil {
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

	return strings.TrimSpace(strings.Join(out, "\n")) + "\n"
}

// ─── Platform paths ──────────────────────────────────────────────────────────

func openCodePluginDir() string {
	home, _ := userHomeDir()

	switch runtimeGOOS {
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
	home, _ := userHomeDir()

	switch runtimeGOOS {
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
	home, _ := userHomeDir()

	switch runtimeGOOS {
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

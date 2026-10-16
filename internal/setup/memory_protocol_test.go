package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the public setup path without invoking a host CLI or using real profiles.
func TestMemoryProtocolInstalledHostInstructions(t *testing.T) {
	for _, agent := range []string{"gemini-cli", "codex"} {
		t.Run(agent, func(t *testing.T) {
			resetSetupSeams(t)
			useIsolatedProfile(t)
			lookPathFn = func(string) (string, error) { return "mock-host", nil }
			runCommand = func(string, ...string) ([]byte, error) { return []byte("ok"), nil }
			if _, err := Install(agent); err != nil {
				t.Fatal(err)
			}
			path := codexInstructionsPath()
			if agent == "gemini-cli" {
				path = geminiSystemPromptPath()
			}
			text := readMemoryContract(t, path)
			assertMemoryContract(t, text)
			for _, forbidden := range []string{"ToolSearch", "Core tools are loaded", "Deferred tools", "mcp__engram__", "permissions allowlist"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("generic instructions impose Claude bootstrap assumption %q", forbidden)
				}
			}
			if !strings.Contains(text, "Summary persistence does not end the session") {
				t.Error("missing summary/lifecycle distinction")
			}
			if agent == "gemini-cli" {
				if !strings.Contains(text, "After mem_session_summary succeeds, call mem_session_end") {
					t.Error("Gemini-specific closure was lost")
				}
			} else if strings.Contains(text, "call mem_session_end") {
				t.Error("generic protocol must not require every host to end via MCP")
			}
		})
	}
}

// These manually maintained skills and docs are prose contracts, not generated copies.
func TestMemoryProtocolHostAssets(t *testing.T) {
	for _, path := range []string{
		"plugin/claude-code/skills/memory/SKILL.md",
		"plugin/codex/skills/memory/SKILL.md",
		"DOCS.md",
	} {
		t.Run(path, func(t *testing.T) {
			text := readMemoryContract(t, filepath.Join("../..", path))
			if path == "DOCS.md" {
				text = memoryProseBlock(t, text, "## Memory Protocol\n", "\n## Project Name Normalization")
			}
			assertMemoryContract(t, text)
			// Claude SKILL.md still references its own adapter names and discovery.
			if strings.Contains(path, "claude-code") {
				if !strings.Contains(text, "mcp__plugin_engram_engram__") {
					t.Error("Claude-specific registration guidance was lost")
				}
			}
			if strings.Contains(path, "codex") || path == "DOCS.md" {
				assertCodexBindingContract(t, text)
			}
			if strings.Contains(path, "codex") {
				for _, forbidden := range []string{"ToolSearch", "Core tools are loaded", "permissions allowlist"} {
					if strings.Contains(text, forbidden) {
						t.Errorf("Codex skill contains unsupported assumption %q", forbidden)
					}
				}
			}
		})
	}
}

// Session-start hooks inject protocol copies into the agent's context at runtime.
// Every injected copy MUST contain the shared phrase and MUST NOT leak Claude-specific
// assumptions (except where the host is Claude Code itself).
func TestMemoryProtocolSessionStartSh(t *testing.T) {
	for _, path := range []string{
		"plugin/codex/scripts/session-start.sh",
		"plugin/claude-code/scripts/session-start.sh",
	} {
		t.Run(path, func(t *testing.T) {
			text := readMemoryContract(t, filepath.Join("../..", path))
			// All session-start protocol copies must have the shared phrase.
			sharedPhrase := "The agent MUST follow this protocol without waiting for activation"
			if !strings.Contains(text, sharedPhrase) {
				t.Errorf("session-start.sh missing shared phrase %q", sharedPhrase)
			}
			// Host-specific hosts must not leak forbidden phrases in their protocol block.
			if strings.Contains(path, "codex") {
				for _, forbidden := range []string{"Core tools are loaded", "Deferred tools"} {
					if strings.Contains(text, forbidden) {
						t.Errorf("Codex session-start.sh contains forbidden assumption %q", forbidden)
					}
				}
			}
			if strings.Contains(path, "claude-code") {
				// Claude's session-start.sh legitimately references ToolSearch for
				// its own adapter; we only assert the shared phrase is present.
				if !strings.Contains(text, "## Engram Persistent Memory — ACTIVE PROTOCOL") {
					t.Error("Claude session-start.sh missing protocol header")
				}
			}
		})
	}
}

func assertCodexBindingContract(t *testing.T, text string) {
	t.Helper()
	for _, required := range []string{
		"PreToolUse", "engram hook codex-pre-tool-use",
		"mcp__engram__mem_*", "mcp__plugin_engram_engram__mem_*",
		"authoritative Codex host", "server-confirmed", "Read-only tools",
		"direct/nonintercepted clients", "reuse this exact",
		"`mem_session_end` uses `id`, not `session_id`",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Codex binding contract missing %q", required)
		}
	}
	if strings.Contains(text, "does not automatically intercept") {
		t.Error("Codex contract falsely denies native PreToolUse interception")
	}
}

func TestMemoryProtocolClaudeVersions(t *testing.T) {
	if err := memoryVersionsMatch(
		[]byte(readMemoryContract(t, "../../plugin/claude-code/.claude-plugin/plugin.json")),
		[]byte(readMemoryContract(t, "../../.claude-plugin/marketplace.json")),
	); err != nil {
		t.Fatal(err)
	}
}

// Explicit inventory: check delivered prose, not surrounding adapter code/comments.
func TestMemoryProtocolSourceInventory(t *testing.T) {
	for _, source := range []struct {
		path, start, end string
		claude           bool
	}{
		{"internal/setup/setup.go", "const memoryProtocolMarkdown = `", "`", false},
		{"plugin/opencode/engram.ts", "const MEMORY_INSTRUCTIONS = `", "`;", false},
		{"internal/setup/plugins/opencode/engram.ts", "const MEMORY_INSTRUCTIONS = `", "`;", false},
		{"plugin/pi/index.ts", "const MEMORY_INSTRUCTIONS = `", "`;", false},
		{"plugin/pi/README.md", "### Injected memory contract", "### Automatic user-prompt capture", false},
		{"skills/memory-protocol/SKILL.md", "## When to Use", "", false},
		{"plugin/claude-code/skills/memory/SKILL.md", "", "", true},
		{"plugin/codex/skills/memory/SKILL.md", "", "", false},
		{"plugin/claude-code/scripts/session-start.sh", "cat <<'PROTOCOL'\n", "\nPROTOCOL", true},
		{"plugin/codex/scripts/session-start.sh", "cat <<'PROTOCOL'\n", "\nPROTOCOL", false},
		{"plugin/claude-code/scripts/post-compaction.sh", "cat <<'PROTOCOL'\n", "\nPROTOCOL", true},
		{"plugin/codex/scripts/post-compaction.sh", "cat <<'PROTOCOL'\n", "\nPROTOCOL", false},
		{"DOCS.md", "## Memory Protocol\n", "\n## Project Name Normalization", false},
	} {
		t.Run(source.path, func(t *testing.T) {
			text := readMemoryContract(t, filepath.Join("../..", source.path))
			text = memoryProseBlock(t, text, source.start, source.end)
			if !strings.Contains(text, "The agent MUST follow this protocol without waiting for activation") {
				t.Error("protocol prose missing shared activation phrase")
			}
			for _, forbidden := range []string{"Core tools are loaded", "Deferred tools", "Engram will automatically extract and save these", "does not automatically intercept"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("protocol prose contains obsolete assumption %q", forbidden)
				}
			}
			if !source.claude {
				for _, forbidden := range []string{"Use ToolSearch", "call ToolSearch", "permissions allowlist", "### CORE TOOLS — always available"} {
					if strings.Contains(text, forbidden) {
						t.Errorf("non-Claude prose imposes %q", forbidden)
					}
				}
			}
		})
	}
	source, err := os.ReadFile("../../plugin/opencode/engram.ts")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := os.ReadFile("plugins/opencode/engram.ts")
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != string(embedded) {
		t.Error("embedded OpenCode adapter differs from source")
	}
}

func memoryProseBlock(t *testing.T, text, start, end string) string {
	t.Helper()
	if start != "" {
		index := strings.Index(text, start)
		if index < 0 {
			t.Fatalf("missing prose start %q", start)
		}
		text = text[index+len(start):]
	}
	if end != "" {
		index := strings.Index(text, end)
		if index < 0 {
			t.Fatalf("missing prose end %q", end)
		}
		text = text[:index]
	}
	return text
}

func TestMemoryProtocolClaudeVersionFixtures(t *testing.T) {
	for _, fixture := range []struct {
		name, plugin, marketplace string
		wantError                 bool
	}{
		{"future synchronized bump", "9.4.2", "9.4.2", false},
		{"mismatch", "9.4.2", "9.4.1", true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			plugin := []byte(fmt.Sprintf(`{"version":%q}`, fixture.plugin))
			marketplace := []byte(fmt.Sprintf(`{"plugins":[{"name":"engram","version":%q}]}`, fixture.marketplace))
			if err := memoryVersionsMatch(plugin, marketplace); (err != nil) != fixture.wantError {
				t.Fatalf("synchronization error = %v, want error %v", err, fixture.wantError)
			}
		})
	}
}

func memoryVersionsMatch(pluginData, marketplaceData []byte) error {
	var plugin struct{ Version string }
	var marketplace struct {
		Plugins []struct{ Name, Version string }
	}
	if err := json.Unmarshal(pluginData, &plugin); err != nil {
		return err
	}
	if err := json.Unmarshal(marketplaceData, &marketplace); err != nil {
		return err
	}
	if plugin.Version == "" {
		return fmt.Errorf("Claude plugin version is empty")
	}
	for _, entry := range marketplace.Plugins {
		if entry.Name == "engram" && entry.Version == plugin.Version {
			return nil
		}
	}
	return fmt.Errorf("marketplace lacks matching Engram version")
}

func readMemoryContract(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func assertMemoryContract(t *testing.T, text string) {
	t.Helper()
	for _, required := range []string{
		"The agent MUST follow this protocol without waiting for activation",
		"User confirms", "User rejects", "Workflow", "non-obvious approach",
		"adapter-specific", "ordinary responses", "primary",
		"If memory work fails or needs follow-up, still send the answer",
	} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(required)) {
			t.Errorf("memory contract missing %q", required)
		}
	}
	if strings.Contains(text, "Engram will automatically extract and save these") {
		t.Error("passive capture promises universal response extraction")
	}
}

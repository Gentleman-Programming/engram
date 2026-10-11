package setup

import (
	"encoding/json"
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
				start := strings.Index(text, "## Memory Protocol\n")
				end := strings.Index(text[start:], "\n## Project Name Normalization")
				text = text[start : start+end]
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
	var plugin struct{ Version string }
	var marketplace struct {
		Plugins []struct{ Name, Version string }
	}
	if err := json.Unmarshal([]byte(readMemoryContract(t, "../../plugin/claude-code/.claude-plugin/plugin.json")), &plugin); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(readMemoryContract(t, "../../.claude-plugin/marketplace.json")), &marketplace); err != nil {
		t.Fatal(err)
	}
	if plugin.Version != "0.1.8" {
		t.Errorf("Claude plugin version = %q, want 0.1.8", plugin.Version)
	}
	for _, entry := range marketplace.Plugins {
		if entry.Name == "engram" && entry.Version == plugin.Version {
			return
		}
	}
	t.Fatal("marketplace lacks matching Engram version")
}

func readMemoryContract(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
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

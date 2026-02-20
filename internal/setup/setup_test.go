package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupportedAgentsIncludesGeminiAndCodex(t *testing.T) {
	agents := SupportedAgents()

	var hasGemini bool
	var hasCodex bool
	for _, agent := range agents {
		if agent.Name == "gemini-cli" {
			hasGemini = true
		}
		if agent.Name == "codex" {
			hasCodex = true
		}
	}

	if !hasGemini {
		t.Fatalf("expected gemini-cli in supported agents")
	}
	if !hasCodex {
		t.Fatalf("expected codex in supported agents")
	}
}

func TestInstallGeminiCLIInjectsMCPConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(home, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	original := `{"theme":"dark","mcpServers":{"other":{"command":"foo","args":["bar"]}}}`
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write initial settings: %v", err)
	}

	result, err := Install("gemini-cli")
	if err != nil {
		t.Fatalf("install gemini-cli: %v", err)
	}

	if result.Agent != "gemini-cli" {
		t.Fatalf("unexpected agent in result: %q", result.Agent)
	}

	if result.Files != 3 {
		t.Fatalf("expected 3 files written, got %d", result.Files)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse settings: %v", err)
	}

	mcpServers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("expected mcpServers object")
	}

	engram, ok := mcpServers["engram"].(map[string]any)
	if !ok {
		t.Fatalf("expected mcpServers.engram object")
	}

	if got := engram["command"]; got != "engram" {
		t.Fatalf("expected command engram, got %#v", got)
	}

	args, ok := engram["args"].([]any)
	if !ok || len(args) != 1 || args[0] != "mcp" {
		t.Fatalf("expected args [mcp], got %#v", engram["args"])
	}

	if _, ok := mcpServers["other"]; !ok {
		t.Fatalf("expected existing mcp server to be preserved")
	}

	systemPath := filepath.Join(home, ".gemini", "system.md")
	systemRaw, err := os.ReadFile(systemPath)
	if err != nil {
		t.Fatalf("read system prompt: %v", err)
	}
	systemText := string(systemRaw)
	if !strings.Contains(systemText, "### AFTER COMPACTION") {
		t.Fatalf("expected AFTER COMPACTION section in system prompt")
	}
	if !strings.Contains(systemText, "FIRST ACTION REQUIRED") {
		t.Fatalf("expected FIRST ACTION REQUIRED guidance in system prompt")
	}

	envPath := filepath.Join(home, ".gemini", ".env")
	envRaw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read gemini .env: %v", err)
	}
	if !strings.Contains(string(envRaw), "GEMINI_SYSTEM_MD=1") {
		t.Fatalf("expected GEMINI_SYSTEM_MD=1 in .env")
	}

	if _, err := Install("gemini-cli"); err != nil {
		t.Fatalf("second install should be idempotent: %v", err)
	}

	envRaw2, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read gemini .env after second install: %v", err)
	}
	if strings.Count(string(envRaw2), "GEMINI_SYSTEM_MD=1") != 1 {
		t.Fatalf("expected exactly one GEMINI_SYSTEM_MD line, got:\n%s", string(envRaw2))
	}
}

func TestInstallCodexInjectsTOMLAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	original := strings.Join([]string{
		"[profile]",
		"name = \"dev\"",
		"",
		"[mcp_servers.existing]",
		"command = \"existing\"",
		"args = [\"x\"]",
		"",
		"[mcp_servers.engram]",
		"command = \"wrong\"",
		"args = [\"wrong\"]",
	}, "\n")
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	result, err := Install("codex")
	if err != nil {
		t.Fatalf("install codex: %v", err)
	}

	if result.Agent != "codex" {
		t.Fatalf("unexpected agent in result: %q", result.Agent)
	}

	if result.Files != 3 {
		t.Fatalf("expected 3 files written, got %d", result.Files)
	}

	readAndAssert := func() string {
		t.Helper()
		raw, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read codex config: %v", err)
		}
		text := string(raw)

		if !strings.Contains(text, "[profile]") {
			t.Fatalf("expected existing profile section to be preserved")
		}
		if !strings.Contains(text, "[mcp_servers.existing]") {
			t.Fatalf("expected existing mcp server section to be preserved")
		}
		if strings.Count(text, "[mcp_servers.engram]") != 1 {
			t.Fatalf("expected exactly one engram section, got:\n%s", text)
		}
		if !strings.Contains(text, "command = \"engram\"") {
			t.Fatalf("expected engram command in config, got:\n%s", text)
		}
		if !strings.Contains(text, "args = [\"mcp\"]") {
			t.Fatalf("expected engram args in config, got:\n%s", text)
		}
		instructionsPath := filepath.Join(home, ".codex", "engram-instructions.md")
		if !strings.Contains(text, "model_instructions_file = \""+instructionsPath+"\"") {
			t.Fatalf("expected model_instructions_file in config, got:\n%s", text)
		}
		compactPromptPath := filepath.Join(home, ".codex", "engram-compact-prompt.md")
		if !strings.Contains(text, "experimental_compact_prompt_file = \""+compactPromptPath+"\"") {
			t.Fatalf("expected compact prompt file key in config, got:\n%s", text)
		}
		firstSection := strings.Index(text, "[profile]")
		if firstSection == -1 {
			t.Fatalf("expected [profile] section in config")
		}
		if idx := strings.Index(text, "model_instructions_file"); idx == -1 || idx > firstSection {
			t.Fatalf("expected model_instructions_file to be top-level before sections, got:\n%s", text)
		}
		if idx := strings.Index(text, "experimental_compact_prompt_file"); idx == -1 || idx > firstSection {
			t.Fatalf("expected compact prompt key to be top-level before sections, got:\n%s", text)
		}
		return text
	}

	first := readAndAssert()

	if _, err := Install("codex"); err != nil {
		t.Fatalf("second install should be idempotent: %v", err)
	}

	second := readAndAssert()
	if first != second {
		t.Fatalf("expected no changes on second install")
	}

	instructionsRaw, err := os.ReadFile(filepath.Join(home, ".codex", "engram-instructions.md"))
	if err != nil {
		t.Fatalf("read codex instructions: %v", err)
	}
	if !strings.Contains(string(instructionsRaw), "### AFTER COMPACTION") {
		t.Fatalf("expected AFTER COMPACTION section in codex instructions")
	}

	compactRaw, err := os.ReadFile(filepath.Join(home, ".codex", "engram-compact-prompt.md"))
	if err != nil {
		t.Fatalf("read codex compact prompt: %v", err)
	}
	if !strings.Contains(string(compactRaw), "FIRST ACTION REQUIRED") {
		t.Fatalf("expected FIRST ACTION REQUIRED text in compact prompt")
	}
}

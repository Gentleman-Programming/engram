package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testEngramBin = "/usr/local/bin/engram"

// declarativeAgent describes a registry agent installed through the generic
// driver (no custom installer), for table-driven assertions.
type declarativeAgent struct {
	slug      string
	mcpPath   func() string
	topKey    string
	mcpFormat mcpFormat
	instrPath func() string
	style     instrStyle
}

func declarativeAgents() []declarativeAgent {
	return []declarativeAgent{
		{"antigravity-cli", antigravityMCPConfigPath, "mcpServers", mcpServersObject, antigravityContextPath, markerBlock},
		{"windsurf", windsurfMCPPath, "mcpServers", mcpServersObject, windsurfRulesPath, markerBlock},
		{"qwen", qwenSettingsPath, "mcpServers", mcpServersObject, qwenContextPath, markerBlock},
		{"kiro", kiroMCPPath, "mcpServers", mcpServersObject, kiroSteeringPath, markerBlock},
		{"cursor", cursorMCPPath, "mcpServers", mcpServersObject, cursorRulesPath, wholeFile},
		{"vscode-copilot", vscodeMCPPath, "servers", serversObject, vscodePromptPath, wholeFile},
		{"kilocode", kilocodeConfigPath, "mcp", opencodeObject, kilocodeAgentsPath, markerBlock},
	}
}

// stubRegistryEnv isolates path resolution to a temp HOME with no XDG/APPDATA
// leakage and a deterministic OS and binary path.
func stubRegistryEnv(t *testing.T) string {
	t.Helper()
	resetSetupSeams(t)
	home := useTestHome(t)
	runtimeGOOS = "linux"
	osExecutable = func() (string, error) { return testEngramBin, nil }
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "")
	return home
}

func TestSupportedAgentsIncludesAllRegistryAgents(t *testing.T) {
	stubRegistryEnv(t)

	got := make(map[string]bool)
	for _, a := range SupportedAgents() {
		got[a.Name] = true
	}

	want := []string{
		"opencode", "pi", "claude-code", "gemini-cli", "codex",
		"antigravity-cli", "windsurf", "qwen", "kiro", "cursor",
		"vscode-copilot", "kilocode",
	}
	for _, slug := range want {
		if !got[slug] {
			t.Errorf("expected %q in SupportedAgents()", slug)
		}
	}
	if len(got) != len(want) {
		t.Errorf("expected %d agents, got %d", len(want), len(got))
	}
}

// readEngramEntry parses the MCP config at path and returns the engram server
// entry stored under topKey.
func readEngramEntry(t *testing.T, path, topKey string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mcp config %s: %v", path, err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse mcp config %s: %v", path, err)
	}
	block, ok := cfg[topKey].(map[string]any)
	if !ok {
		t.Fatalf("expected %q object in %s, got %#v", topKey, path, cfg[topKey])
	}
	entry, ok := block["engram"].(map[string]any)
	if !ok {
		t.Fatalf("expected %s.engram object in %s", topKey, path)
	}
	return entry
}

func TestInstallDeclarativeAgentsRegisterMCPAndInstructions(t *testing.T) {
	for _, agent := range declarativeAgents() {
		t.Run(agent.slug, func(t *testing.T) {
			stubRegistryEnv(t)

			result, err := Install(agent.slug)
			if err != nil {
				t.Fatalf("Install(%s) failed: %v", agent.slug, err)
			}
			if result.Agent != agent.slug {
				t.Fatalf("expected agent %q, got %q", agent.slug, result.Agent)
			}
			if result.Files != 2 {
				t.Fatalf("expected 2 files for %s, got %d", agent.slug, result.Files)
			}

			// MCP entry shape per format.
			entry := readEngramEntry(t, agent.mcpPath(), agent.topKey)
			switch agent.mcpFormat {
			case opencodeObject:
				if entry["type"] != "local" {
					t.Errorf("%s: expected type local, got %#v", agent.slug, entry["type"])
				}
				if entry["enabled"] != true {
					t.Errorf("%s: expected enabled true, got %#v", agent.slug, entry["enabled"])
				}
				cmd, ok := entry["command"].([]any)
				if !ok || len(cmd) != 3 || cmd[0] != testEngramBin || cmd[1] != "mcp" || cmd[2] != "--tools=agent" {
					t.Errorf("%s: unexpected command array %#v", agent.slug, entry["command"])
				}
			default:
				if entry["command"] != testEngramBin {
					t.Errorf("%s: expected command %q, got %#v", agent.slug, testEngramBin, entry["command"])
				}
				args, ok := entry["args"].([]any)
				if !ok || len(args) != 2 || args[0] != "mcp" || args[1] != "--tools=agent" {
					t.Errorf("%s: unexpected args %#v", agent.slug, entry["args"])
				}
				if agent.mcpFormat == serversObject && entry["type"] != "stdio" {
					t.Errorf("%s: expected type stdio, got %#v", agent.slug, entry["type"])
				}
			}

			// Instruction surface contains the protocol.
			instrRaw, err := os.ReadFile(agent.instrPath())
			if err != nil {
				t.Fatalf("read instruction file %s: %v", agent.instrPath(), err)
			}
			instr := string(instrRaw)
			if !strings.Contains(instr, "Engram Persistent Memory") {
				t.Errorf("%s: instruction file missing protocol content", agent.slug)
			}
			if agent.style == markerBlock && !strings.Contains(instr, engramMarkerBegin) {
				t.Errorf("%s: marker-block instruction missing begin marker", agent.slug)
			}

			// Idempotency: second run does not duplicate the entry or marker block.
			if _, err := Install(agent.slug); err != nil {
				t.Fatalf("second Install(%s) failed: %v", agent.slug, err)
			}
			instr2Raw, _ := os.ReadFile(agent.instrPath())
			if agent.style == markerBlock {
				if n := strings.Count(string(instr2Raw), engramMarkerBegin); n != 1 {
					t.Errorf("%s: expected 1 marker block after re-run, got %d", agent.slug, n)
				}
			}
		})
	}
}

func TestCursorAndVSCodeInstructionsCarryFrontmatter(t *testing.T) {
	stubRegistryEnv(t)

	if _, err := Install("cursor"); err != nil {
		t.Fatalf("Install(cursor): %v", err)
	}
	cursorRaw, _ := os.ReadFile(cursorRulesPath())
	if !strings.Contains(string(cursorRaw), "alwaysApply: true") {
		t.Errorf("cursor .mdc missing alwaysApply frontmatter")
	}

	if _, err := Install("vscode-copilot"); err != nil {
		t.Fatalf("Install(vscode-copilot): %v", err)
	}
	vscodeRaw, _ := os.ReadFile(vscodePromptPath())
	if !strings.Contains(string(vscodeRaw), `applyTo: "**"`) {
		t.Errorf("vscode instructions missing applyTo frontmatter")
	}
}

func TestInjectMCPPreservesExistingServersAndKeys(t *testing.T) {
	stubRegistryEnv(t)
	path := windsurfMCPPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	original := `{"theme":"dark","mcpServers":{"other":{"command":"foo","args":["bar"]}}}`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	if err := injectMCP(path, mcpServersObject); err != nil {
		t.Fatalf("injectMCP: %v", err)
	}

	raw, _ := os.ReadFile(path)
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg["theme"] != "dark" {
		t.Errorf("expected top-level theme preserved, got %#v", cfg["theme"])
	}
	servers := cfg["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Errorf("expected existing 'other' server preserved")
	}
	if _, ok := servers["engram"]; !ok {
		t.Errorf("expected engram server added")
	}
}

func TestUpsertMarkerBlockPreservesUserContentAndReplaces(t *testing.T) {
	resetSetupSeams(t)
	home := useTestHome(t)
	path := filepath.Join(home, "notes.md")
	if err := os.WriteFile(path, []byte("# My notes\n\nkeep me\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := upsertMarkerBlock(path, engramMarkerBegin, engramMarkerEnd, "BODY ONE"); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := upsertMarkerBlock(path, engramMarkerBegin, engramMarkerEnd, "BODY TWO"); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	raw, _ := os.ReadFile(path)
	text := string(raw)
	if !strings.Contains(text, "keep me") {
		t.Errorf("user content not preserved: %q", text)
	}
	if strings.Contains(text, "BODY ONE") {
		t.Errorf("stale managed block not replaced: %q", text)
	}
	if !strings.Contains(text, "BODY TWO") {
		t.Errorf("new managed block missing: %q", text)
	}
	if n := strings.Count(text, engramMarkerBegin); n != 1 {
		t.Errorf("expected exactly 1 marker block, got %d", n)
	}
}

func TestInstallDeclarativeAgentMCPWriteError(t *testing.T) {
	stubRegistryEnv(t)
	writeFileFn = func(string, []byte, os.FileMode) error { return errors.New("disk full") }

	if _, err := Install("windsurf"); err == nil {
		t.Fatalf("expected write error to propagate")
	}
}

func TestVSCodeUserDirPerPlatform(t *testing.T) {
	resetSetupSeams(t)
	home := useTestHome(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("APPDATA", "")

	cases := map[string]string{
		"linux":  filepath.Join(home, ".config", "Code", "User"),
		"darwin": filepath.Join(home, "Library", "Application Support", "Code", "User"),
	}
	for goos, want := range cases {
		runtimeGOOS = goos
		if got := vscodeUserDir(); got != want {
			t.Errorf("vscodeUserDir(%s) = %q, want %q", goos, got, want)
		}
	}
}

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
		{"cursor", cursorMCPPath, "mcpServers", mcpServersObject, cursorMemoryProtocolPath, wholeFile},
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

			// Instruction surface contains the FULL protocol text. Declarative
			// adapters (this table) are out of scope for --protocol=slim (see
			// openspec/changes/setup-protocol-flag/proposal.md, Out of Scope):
			// their protocol text is baked in at setup time from
			// memoryProtocolMarkdown, not read at runtime, so it must always be
			// the complete markdown — never a truncated/slim variant.
			instrRaw, err := os.ReadFile(agent.instrPath())
			if err != nil {
				t.Fatalf("read instruction file %s: %v", agent.instrPath(), err)
			}
			instr := string(instrRaw)
			if !strings.Contains(instr, "Engram Persistent Memory") {
				t.Errorf("%s: instruction file missing protocol content", agent.slug)
			}
			if !strings.Contains(instr, "SESSION CLOSE PROTOCOL") {
				t.Errorf("%s: instruction file missing full-protocol SESSION CLOSE section (adapter is full-only, not slim-eligible)", agent.slug)
			}
			if !strings.Contains(instr, "AFTER COMPACTION") {
				t.Errorf("%s: instruction file missing full-protocol AFTER COMPACTION section (adapter is full-only, not slim-eligible)", agent.slug)
			}
			if agent.style == markerBlock {
				begin := strings.Index(instr, engramMarkerBegin)
				end := strings.Index(instr, engramMarkerEnd)
				if begin == -1 {
					t.Errorf("%s: marker-block instruction missing begin marker", agent.slug)
				}
				if end == -1 {
					t.Errorf("%s: marker-block instruction missing end marker", agent.slug)
				}
				if begin != -1 && end != -1 && end <= begin {
					t.Errorf("%s: end marker (%d) does not follow begin marker (%d)", agent.slug, end, begin)
				}
			}

			// Idempotency: second run does not duplicate the entry or marker block.
			if _, err := Install(agent.slug); err != nil {
				t.Fatalf("second Install(%s) failed: %v", agent.slug, err)
			}
			instr2Raw, err := os.ReadFile(agent.instrPath())
			if err != nil {
				t.Fatalf("read instruction file after second install: %v", err)
			}
			if agent.style == markerBlock {
				instr2 := string(instr2Raw)
				if n := strings.Count(instr2, engramMarkerBegin); n != 1 {
					t.Errorf("%s: expected 1 marker block after re-run, got %d", agent.slug, n)
				}
				if n := strings.Count(instr2, engramMarkerEnd); n != 1 {
					t.Errorf("%s: expected 1 marker end after re-run, got %d", agent.slug, n)
				}
			}
		})
	}
}

func TestCursorMemoryProtocolHasNoFrontmatter(t *testing.T) {
	home := stubRegistryEnv(t)

	if _, err := Install("cursor"); err != nil {
		t.Fatalf("Install(cursor): %v", err)
	}

	// The old .mdc path must NOT exist — we no longer write a global rule file.
	oldMDCPath := filepath.Join(home, ".cursor", "rules", "engram.mdc")
	if _, err := os.Stat(oldMDCPath); err == nil {
		t.Errorf("cursor: ~/.cursor/rules/engram.mdc should not be written (Cursor ignores global rule files)")
	}

	// The new informational file must exist, contain the protocol, and have no YAML frontmatter.
	protocolRaw, err := os.ReadFile(cursorMemoryProtocolPath())
	if err != nil {
		t.Fatalf("cursor: memory protocol file not written at %s: %v", cursorMemoryProtocolPath(), err)
	}
	content := string(protocolRaw)
	if !strings.Contains(content, "Engram Persistent Memory") {
		t.Errorf("cursor: memory protocol file missing expected content")
	}
	if strings.Contains(content, "alwaysApply") {
		t.Errorf("cursor: memory protocol file must not contain alwaysApply frontmatter")
	}
	if strings.HasPrefix(content, "---") {
		t.Errorf("cursor: memory protocol file must not start with YAML frontmatter")
	}
}

func TestVSCodeInstructionsCarryFrontmatter(t *testing.T) {
	stubRegistryEnv(t)

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

// TestUpsertMarkerBlockStrayEndMarkerStaysIdempotent guards the anchored end
// search: a stray end marker in user content ABOVE the managed block must not
// defeat idempotency (an unanchored search would find it and append a duplicate).
func TestUpsertMarkerBlockStrayEndMarkerStaysIdempotent(t *testing.T) {
	resetSetupSeams(t)
	home := useTestHome(t)
	path := filepath.Join(home, "notes.md")
	seed := "# My notes\n\n" + engramMarkerEnd + "\n\nkeep me\n"
	if err := os.WriteFile(path, []byte(seed), 0644); err != nil {
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
		t.Errorf("expected exactly 1 managed block despite stray end marker, got %d: %q", n, text)
	}
}

func TestInstallDeclarativeAgentMCPWriteError(t *testing.T) {
	stubRegistryEnv(t)
	writeFileFn = func(string, []byte, os.FileMode) error { return errors.New("disk full") }

	if _, err := Install("windsurf"); err == nil {
		t.Fatalf("expected write error to propagate")
	}
}

// TestInstallDeclarativeAgentInstructionWriteError verifies an instruction-surface
// write failure propagates even when the MCP write succeeds: writeFileFn fails only
// for the (non-JSON) instruction file so injectMCP — which goes through
// writeJSONConfig — completes first.
func TestInstallDeclarativeAgentInstructionWriteError(t *testing.T) {
	stubRegistryEnv(t)
	instrPath := windsurfRulesPath()
	writeFileFn = func(path string, _ []byte, _ os.FileMode) error {
		if path == instrPath {
			return errors.New("disk full")
		}
		return os.WriteFile(path, nil, 0644)
	}

	if _, err := Install("windsurf"); err == nil {
		t.Fatalf("expected instruction write error to propagate")
	}
}

// TestInjectMCPHandlesNullServersObject guards the case where an existing config
// stores the top-level servers key as JSON null — unmarshalling leaves the map
// nil, and the engram assignment must not panic.
func TestInjectMCPHandlesNullServersObject(t *testing.T) {
	stubRegistryEnv(t)
	path := windsurfMCPPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"mcpServers":null}`), 0644); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	if err := injectMCP(path, mcpServersObject); err != nil {
		t.Fatalf("injectMCP with null servers: %v", err)
	}
	entry := readEngramEntry(t, path, "mcpServers")
	if entry["command"] != testEngramBin {
		t.Errorf("expected engram entry written, got %#v", entry)
	}
}

// TestInstallDeclarativeAgentFailsOnUnresolvableHome verifies that a home-dir
// lookup failure fails the install rather than silently writing to a relative
// path under the current working directory.
func TestInstallDeclarativeAgentFailsOnUnresolvableHome(t *testing.T) {
	stubRegistryEnv(t)
	userHomeDir = func() (string, error) { return "", errors.New("no home") }

	if _, err := Install("windsurf"); err == nil {
		t.Fatalf("expected install to fail when home is unresolvable")
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

// TestConfigDirsIgnoreRelativeConfigHome verifies a relative XDG_CONFIG_HOME /
// APPDATA is ignored (falling back to the absolute home path) instead of
// resolving config under the current working directory.
func TestConfigDirsIgnoreRelativeConfigHome(t *testing.T) {
	resetSetupSeams(t)
	home := useTestHome(t)

	t.Run("relative XDG_CONFIG_HOME ignored", func(t *testing.T) {
		runtimeGOOS = "linux"
		t.Setenv("XDG_CONFIG_HOME", "relative/xdg")
		if got, want := vscodeUserDir(), filepath.Join(home, ".config", "Code", "User"); got != want {
			t.Errorf("vscodeUserDir with relative XDG = %q, want %q", got, want)
		}
		if got, want := kilocodeConfigDir(), filepath.Join(home, ".config", "kilo"); got != want {
			t.Errorf("kilocodeConfigDir with relative XDG = %q, want %q", got, want)
		}
	})

	t.Run("relative APPDATA ignored", func(t *testing.T) {
		runtimeGOOS = "windows"
		t.Setenv("APPDATA", "relative/appdata")
		if got, want := vscodeUserDir(), filepath.Join(home, "AppData", "Roaming", "Code", "User"); got != want {
			t.Errorf("vscodeUserDir with relative APPDATA = %q, want %q", got, want)
		}
	})
}

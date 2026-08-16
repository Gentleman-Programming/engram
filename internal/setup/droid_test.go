package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallDroidRequiresDroidCLI(t *testing.T) {
	resetSetupSeams(t)
	home := useTestHome(t)
	lookPathFn = func(name string) (string, error) {
		if name == "droid" {
			return "", errors.New("not found")
		}
		return "", errors.New("not found")
	}

	_, err := Install("droid")
	if err == nil {
		t.Fatalf("expected error when droid CLI is missing")
	}
	if !strings.Contains(err.Error(), "droid CLI not found") {
		t.Fatalf("expected droid CLI error, got: %v", err)
	}
	_ = home
}

func TestInstallDroidWritesMCPAndHooks(t *testing.T) {
	resetSetupSeams(t)
	home := useTestHome(t)

	lookPathFn = func(name string) (string, error) {
		if name == "droid" {
			return "/usr/local/bin/droid", nil
		}
		return "", errors.New("not found")
	}

	// Mock the plugin install so the test does not hit the network.
	installDroidPluginFn = func() error { return nil }

	result, err := Install("droid")
	if err != nil {
		t.Fatalf("install droid: %v", err)
	}

	if result.Agent != "droid" {
		t.Fatalf("unexpected agent: %q", result.Agent)
	}
	if result.Files != 3 {
		t.Fatalf("expected 3 files written, got %d", result.Files)
	}

	// Verify MCP config.
	mcpPath := filepath.Join(home, ".factory", "mcp.json")
	mcpRaw, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read mcp config: %v", err)
	}
	var mcpCfg map[string]any
	if err := json.Unmarshal(mcpRaw, &mcpCfg); err != nil {
		t.Fatalf("parse mcp config: %v", err)
	}
	mcpServers, ok := mcpCfg["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("expected mcpServers object")
	}
	engram, ok := mcpServers["engram"].(map[string]any)
	if !ok {
		t.Fatalf("expected mcpServers.engram object")
	}
	if engram["type"] != "stdio" {
		t.Fatalf("expected type stdio, got %v", engram["type"])
	}
	cmd, ok := engram["command"].(string)
	if !ok || cmd == "" {
		t.Fatalf("expected non-empty command string")
	}
	args, ok := engram["args"].([]any)
	if !ok || len(args) != 2 || args[0] != "mcp" || args[1] != "--tools=agent" {
		t.Fatalf("expected args [mcp --tools=agent], got %#v", engram["args"])
	}

	// Verify hook scripts were extracted.
	hooksDir := filepath.Join(home, ".factory", "hooks", "engram")
	if _, err := os.Stat(filepath.Join(hooksDir, "user-prompt-submit.sh")); err != nil {
		t.Fatalf("user-prompt-submit.sh not extracted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hooksDir, "_helpers.sh")); err != nil {
		t.Fatalf("_helpers.sh not extracted: %v", err)
	}

	// Verify hooks.json.
	hooksPath := filepath.Join(home, ".factory", "hooks.json")
	hooksRaw, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks config: %v", err)
	}
	var hooksCfg map[string]any
	if err := json.Unmarshal(hooksRaw, &hooksCfg); err != nil {
		t.Fatalf("parse hooks config: %v", err)
	}
	hooks, ok := hooksCfg["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("expected hooks object")
	}
	ups, ok := hooks["UserPromptSubmit"].([]any)
	if !ok || len(ups) != 1 {
		t.Fatalf("expected one UserPromptSubmit matcher group")
	}
	group, ok := ups[0].(map[string]any)
	if !ok {
		t.Fatalf("expected matcher group map")
	}
	hookList, ok := group["hooks"].([]any)
	if !ok || len(hookList) != 1 {
		t.Fatalf("expected one hook command")
	}
	hookCmd, ok := hookList[0].(map[string]any)
	if !ok {
		t.Fatalf("expected hook command map")
	}
	if hookCmd["type"] != "command" {
		t.Fatalf("expected command type, got %v", hookCmd["type"])
	}
	if !strings.HasSuffix(hookCmd["command"].(string), "user-prompt-submit.sh") {
		t.Fatalf("expected command to end with user-prompt-submit.sh, got %v", hookCmd["command"])
	}
}

func TestInstallDroidContinuesIfPluginInstallFails(t *testing.T) {
	resetSetupSeams(t)
	home := useTestHome(t)

	lookPathFn = func(name string) (string, error) {
		if name == "droid" {
			return "/usr/local/bin/droid", nil
		}
		return "", errors.New("not found")
	}
	installDroidPluginFn = func() error { return errors.New("network unavailable") }

	result, err := Install("droid")
	if err != nil {
		t.Fatalf("install droid should not fail when plugin install fails: %v", err)
	}
	if result.Files != 3 {
		t.Fatalf("expected 3 files written, got %d", result.Files)
	}

	// MCP and hooks should still be written.
	if _, err := os.Stat(filepath.Join(home, ".factory", "mcp.json")); err != nil {
		t.Fatalf("mcp.json not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".factory", "hooks.json")); err != nil {
		t.Fatalf("hooks.json not written: %v", err)
	}
}

func TestInstallDroidIsIdempotent(t *testing.T) {
	resetSetupSeams(t)
	home := useTestHome(t)

	lookPathFn = func(name string) (string, error) {
		if name == "droid" {
			return "/usr/local/bin/droid", nil
		}
		return "", errors.New("not found")
	}
	installDroidPluginFn = func() error { return nil }

	if _, err := Install("droid"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	firstMCP, err := os.ReadFile(filepath.Join(home, ".factory", "mcp.json"))
	if err != nil {
		t.Fatalf("read first mcp: %v", err)
	}
	firstHooks, err := os.ReadFile(filepath.Join(home, ".factory", "hooks.json"))
	if err != nil {
		t.Fatalf("read first hooks: %v", err)
	}

	if _, err := Install("droid"); err != nil {
		t.Fatalf("second install should be idempotent: %v", err)
	}
	secondMCP, err := os.ReadFile(filepath.Join(home, ".factory", "mcp.json"))
	if err != nil {
		t.Fatalf("read second mcp: %v", err)
	}
	secondHooks, err := os.ReadFile(filepath.Join(home, ".factory", "hooks.json"))
	if err != nil {
		t.Fatalf("read second hooks: %v", err)
	}

	if string(firstMCP) != string(secondMCP) {
		t.Fatalf("mcp.json changed on second install")
	}
	if string(firstHooks) != string(secondHooks) {
		t.Fatalf("hooks.json changed on second install")
	}
}

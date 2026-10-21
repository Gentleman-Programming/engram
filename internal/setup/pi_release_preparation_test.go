package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstallPiMigratesPublishedSessionOwnedPredecessor(t *testing.T) {
	resetSetupSeams(t)
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	lookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	var commands []string
	runCommand = func(name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	settingsPath := filepath.Join(agentDir, "settings.json")
	original := `{"packages":["npm:existing","npm:gentle-engram@0.4.2","./local-engram",{"source":"./custom-engram","autoUpdate":false},"npm:gentle-engram@9.9.9","npm:gentle-engram@0.4.2"],"theme":"dark","npmCommand":["custom-npm"],"custom":{"keep":true}}`
	if err := os.WriteFile(settingsPath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(agentDir, "mcp.json")
	mcp := []byte(`{"mcpServers":{"other":{"command":"custom-server"}}}`)
	if err := os.WriteFile(mcpPath, mcp, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := Install("pi")
	if err != nil {
		t.Fatal(err)
	}
	if result.Agent != "pi" || result.Destination != agentDir || result.Files != 1 {
		t.Errorf("first migration result = %#v", result)
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(after, &got); err != nil {
		t.Fatal(err)
	}
	expected := `{"packages":["npm:existing","./local-engram",{"source":"./custom-engram","autoUpdate":false},"npm:gentle-engram@9.9.9","npm:gentle-engram@0.5.0"],"theme":"dark","npmCommand":["custom-npm"],"custom":{"keep":true}}`
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("migrated settings = %s; want %s", after, expected)
	}
	result, err = Install("pi")
	if err != nil || result == nil || result.Files != 0 {
		t.Fatalf("repeat migration = %#v, %v", result, err)
	}
	repeated, err := os.ReadFile(settingsPath)
	if err != nil || !bytes.Equal(after, repeated) {
		t.Fatalf("repeat changed settings: %s, %v", repeated, err)
	}
	mcpAfter, err := os.ReadFile(mcpPath)
	if err != nil || !bytes.Equal(mcp, mcpAfter) {
		t.Errorf("unrelated MCP config changed: %s, %v", mcpAfter, err)
	}
	wantCommands := []string{"pi install npm:gentle-engram@0.5.0", "pi install npm:gentle-engram@0.5.0"}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Errorf("commands = %#v; want %#v", commands, wantCommands)
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// claudeEngramWriteAndSessionTools is the complete set of Engram MCP tools
// that mutate memory or session state. Claude's PreToolUse hook binds only
// these calls to its authoritative session identity; read-only MCP calls and
// every non-Engram server retain their existing contracts.
var claudeEngramWriteAndSessionTools = []string{
	"mem_save",
	"mem_update",
	"mem_review",
	"mem_delete",
	"mem_save_prompt",
	"mem_pin",
	"mem_unpin",
	"mem_session_summary",
	"mem_session_start",
	"mem_session_end",
	"mem_capture_passive",
	"mem_merge_projects",
	"mem_judge",
	"mem_compare",
}

var claudeEngramToolPrefixes = []string{
	"mcp__engram__",
	"mcp__plugin_engram_engram__",
}

var claudeHookOutput = func(response []byte) error {
	_, err := os.Stdout.Write(response)
	return err
}

func cmdHook(args []string) {
	if len(args) == 1 && args[0] == "codex-user-prompt-submit" {
		cmdCodexUserPromptSubmit()
		return
	}
	if len(args) != 1 || args[0] != "claude-pre-tool-use" {
		fmt.Fprintln(os.Stderr, "usage: engram hook claude-pre-tool-use|codex-user-prompt-submit")
		exitFunc(1)
		return
	}

	input, err := io.ReadAll(os.Stdin)
	response := claudePreToolUseDeny("cannot read authoritative Claude hook input")
	if err == nil {
		response = transformClaudePreToolUse(input)
	}
	if err := claudeHookOutput(response); err != nil {
		exitFunc(1)
		return
	}
}

// transformClaudePreToolUse consumes Claude Code's authoritative PreToolUse
// input. It never grants a permission decision: successful calls use only
// updatedInput to replace the tool's untrusted model-provided session reference.
// Invalid hook input is denied, rather than allowing a write whose session cannot be bound.
func transformClaudePreToolUse(input []byte) []byte {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(input, &payload); err != nil {
		return claudePreToolUseDeny("malformed authoritative Claude hook input")
	}

	sessionID, ok := claudeHookRequiredString(payload, "session_id")
	if !ok {
		return claudePreToolUseDeny("authoritative Claude session_id is required")
	}
	toolName, ok := claudeHookRequiredString(payload, "tool_name")
	if !ok {
		return claudePreToolUseDeny("authoritative Claude tool_name is required")
	}

	toolInputRaw, ok := payload["tool_input"]
	if !ok {
		return claudePreToolUseDeny("authoritative Claude tool_input is required")
	}
	var toolInput map[string]json.RawMessage
	if err := json.Unmarshal(toolInputRaw, &toolInput); err != nil || toolInput == nil {
		return claudePreToolUseDeny("authoritative Claude tool_input must be an object")
	}

	if !isClaudeEngramWriteOrSessionTool(toolName) {
		return []byte("{}")
	}

	boundSessionID, err := json.Marshal(sessionID)
	if err != nil {
		return claudePreToolUseDeny("cannot bind authoritative Claude session_id")
	}
	toolInput[claudeAuthoritativeSessionField(toolName)] = boundSessionID
	updatedInput, err := json.Marshal(toolInput)
	if err != nil {
		return claudePreToolUseDeny("cannot encode bound Claude tool input")
	}

	return claudePreToolUseResponse(updatedInput)
}

func claudeHookRequiredString(payload map[string]json.RawMessage, field string) (string, bool) {
	raw, ok := payload[field]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", false
	}
	return value, true
}

func claudeAuthoritativeSessionField(toolName string) string {
	if strings.HasSuffix(toolName, "__mem_session_start") || strings.HasSuffix(toolName, "__mem_session_end") {
		return "id"
	}
	return "session_id"
}

func isClaudeEngramWriteOrSessionTool(name string) bool {
	for _, prefix := range claudeEngramToolPrefixes {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		tool := strings.TrimPrefix(name, prefix)
		for _, writeTool := range claudeEngramWriteAndSessionTools {
			if tool == writeTool {
				return true
			}
		}
	}
	return false
}

func claudePreToolUseResponse(updatedInput json.RawMessage) []byte {
	response := struct {
		HookSpecificOutput struct {
			HookEventName string          `json:"hookEventName"`
			UpdatedInput  json.RawMessage `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}{}
	response.HookSpecificOutput.HookEventName = "PreToolUse"
	response.HookSpecificOutput.UpdatedInput = updatedInput
	encoded, _ := json.Marshal(response)
	return encoded
}

func claudePreToolUseDeny(reason string) []byte {
	response := struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}{}
	response.HookSpecificOutput.HookEventName = "PreToolUse"
	response.HookSpecificOutput.PermissionDecision = "deny"
	response.HookSpecificOutput.PermissionDecisionReason = reason
	encoded, _ := json.Marshal(response)
	return encoded
}

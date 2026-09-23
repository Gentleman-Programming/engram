package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func claudeHookStdin(t *testing.T, input string, closed bool) *os.File {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	if _, err := writer.Write([]byte(input)); err != nil {
		t.Fatalf("write Claude hook input: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdin writer: %v", err)
	}
	if closed {
		if err := reader.Close(); err != nil {
			t.Fatalf("close stdin reader: %v", err)
		}
	}
	return reader
}

func TestShouldCheckForUpdatesSkipsInternalHook(t *testing.T) {
	if shouldCheckForUpdates([]string{"hook", "claude-pre-tool-use"}) {
		t.Fatal("internal hook must not run the update check before emitting a Claude hook response")
	}
}

func TestCmdHookWritesTransformedResponse(t *testing.T) {
	oldStdin, oldOutput := os.Stdin, claudeHookOutput
	t.Cleanup(func() { os.Stdin, claudeHookOutput = oldStdin, oldOutput })
	os.Stdin = claudeHookStdin(t, `{"session_id":"claude-session","tool_name":"mcp__engram__mem_save","tool_input":{"title":"decision"}}`, false)
	var output []byte
	claudeHookOutput = func(response []byte) error { output = append([]byte(nil), response...); return nil }
	cmdHook([]string{"claude-pre-tool-use"})
	var response struct {
		HookSpecificOutput struct {
			UpdatedInput       map[string]any `json:"updatedInput"`
			PermissionDecision string         `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil || response.HookSpecificOutput.UpdatedInput["session_id"] != "claude-session" || response.HookSpecificOutput.PermissionDecision != "" {
		t.Fatalf("successful hook output = %s, %v", output, err)
	}
}

func TestCmdHookExitsWhenClaudeResponseWriteFails(t *testing.T) {
	oldStdin, oldOutput, oldExit := os.Stdin, claudeHookOutput, exitFunc
	t.Cleanup(func() { os.Stdin, claudeHookOutput, exitFunc = oldStdin, oldOutput, oldExit })
	claudeHookOutput = func([]byte) error { return errors.New("write Claude hook response") }
	var exitCodes []int
	exitFunc = func(code int) { exitCodes = append(exitCodes, code) }
	os.Stdin = claudeHookStdin(t, `{"session_id":"claude-session","tool_name":"mcp__engram__mem_save","tool_input":{"title":"decision"}}`, false)
	cmdHook([]string{"claude-pre-tool-use"})
	os.Stdin = claudeHookStdin(t, "", true)
	cmdHook([]string{"claude-pre-tool-use"})
	if len(exitCodes) != 2 || exitCodes[0] != 1 || exitCodes[1] != 1 {
		t.Fatalf("exit codes = %v, want [1 1] after Claude hook response write failures", exitCodes)
	}
}

func TestCmdHookEmitsJSONDenialWhenClaudeInputReadFails(t *testing.T) {
	oldStdin, oldOutput := os.Stdin, claudeHookOutput
	t.Cleanup(func() { os.Stdin, claudeHookOutput = oldStdin, oldOutput })
	os.Stdin = claudeHookStdin(t, "", true)
	var output []byte
	claudeHookOutput = func(response []byte) error { output = append([]byte(nil), response...); return nil }
	cmdHook([]string{"claude-pre-tool-use"})
	var response struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("command output = %q, want JSON denial: %v", output, err)
	}
	if response.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("hook event = %q, want PreToolUse", response.HookSpecificOutput.HookEventName)
	}
	if response.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("permissionDecision = %q, want deny", response.HookSpecificOutput.PermissionDecision)
	}
	if response.HookSpecificOutput.PermissionDecisionReason != "cannot read authoritative Claude hook input" {
		t.Fatalf("permissionDecisionReason = %q", response.HookSpecificOutput.PermissionDecisionReason)
	}
}

func TestTransformClaudePreToolUseBindsEngramWritesToAuthoritativeSession(t *testing.T) {
	input := []byte(`{
		"session_id":"claude-parent-session",
		"tool_name":"mcp__engram__mem_save",
		"tool_input":{"title":"decision","content":"keep this","session_id":"model-invented","project":"engram","nested":{"keep":true}}
	}`)

	output := transformClaudePreToolUse(input)
	var response struct {
		HookSpecificOutput struct {
			HookEventName      string         `json:"hookEventName"`
			UpdatedInput       map[string]any `json:"updatedInput"`
			PermissionDecision string         `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode hook response: %v\n%s", err, output)
	}
	if response.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("hook event = %q, want PreToolUse", response.HookSpecificOutput.HookEventName)
	}
	if response.HookSpecificOutput.PermissionDecision != "" {
		t.Fatalf("successful rewrite must not auto-allow, got permissionDecision=%q", response.HookSpecificOutput.PermissionDecision)
	}
	updated := response.HookSpecificOutput.UpdatedInput
	if got := updated["session_id"]; got != "claude-parent-session" {
		t.Fatalf("session_id = %#v, want authoritative Claude session", got)
	}
	if got := updated["title"]; got != "decision" {
		t.Fatalf("title = %#v, want preserved", got)
	}
	if got := updated["project"]; got != "engram" {
		t.Fatalf("project = %#v, want preserved", got)
	}
	nested, ok := updated["nested"].(map[string]any)
	if !ok || nested["keep"] != true {
		t.Fatalf("nested arguments = %#v, want preserved", updated["nested"])
	}
}

func TestTransformClaudePreToolUseBindsEveryEngramWriteAndSessionTool(t *testing.T) {
	for _, tool := range claudeEngramWriteAndSessionTools {
		for _, server := range []string{"mcp__engram__", "mcp__plugin_engram_engram__"} {
			t.Run(server+tool, func(t *testing.T) {
				bindingField := "session_id"
				if tool == "mem_session_start" || tool == "mem_session_end" {
					bindingField = "id"
				}
				input := []byte(`{"session_id":"subagent-session","tool_name":"` + server + tool + `","tool_input":{"` + bindingField + `":"wrong","value":"preserved"}}`)
				output := transformClaudePreToolUse(input)
				var response struct {
					HookSpecificOutput struct {
						UpdatedInput map[string]any `json:"updatedInput"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal(output, &response); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if got := response.HookSpecificOutput.UpdatedInput[bindingField]; got != "subagent-session" {
					t.Fatalf("%s = %#v, want subagent authoritative session", bindingField, got)
				}
				if got := response.HookSpecificOutput.UpdatedInput["value"]; got != "preserved" {
					t.Fatalf("unrelated tool input = %#v, want preserved", got)
				}
			})
		}
	}
}

func TestTransformClaudePreToolUseLeavesNonEngramAndReadToolsUntouched(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`{"session_id":"claude-session","tool_name":"mcp__other__mem_save","tool_input":{"session_id":"model"}}`),
		[]byte(`{"session_id":"claude-session","tool_name":"mcp__engram__mem_search","tool_input":{"query":"history"}}`),
	} {
		if got := strings.TrimSpace(string(transformClaudePreToolUse(input))); got != "{}" {
			t.Fatalf("non-target tool response = %s, want {}", got)
		}
	}
}

func TestTransformClaudePreToolUseFailsClosedForMalformedAuthoritativeInput(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`not json`),
		[]byte(`{"tool_name":"mcp__engram__mem_save","tool_input":{}}`),
		[]byte(`{"session_id":" ","tool_name":"mcp__engram__mem_save","tool_input":{}}`),
		[]byte(`{"session_id":"claude-session","tool_name":"mcp__engram__mem_save","tool_input":null}`),
		[]byte(`{"session_id":"claude-session","tool_name":42,"tool_input":{}}`),
	} {
		output := transformClaudePreToolUse(input)
		var response struct {
			HookSpecificOutput struct {
				HookEventName      string `json:"hookEventName"`
				PermissionDecision string `json:"permissionDecision"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(output, &response); err != nil {
			t.Fatalf("decode denial: %v\n%s", err, output)
		}
		if response.HookSpecificOutput.HookEventName != "PreToolUse" || response.HookSpecificOutput.PermissionDecision != "deny" {
			t.Fatalf("malformed authoritative input response = %#v, want PreToolUse deny", response.HookSpecificOutput)
		}
	}
}

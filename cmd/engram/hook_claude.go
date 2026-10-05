package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
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

// Leave one second of the Claude hook's five-second timeout for process startup
// and emitting the verdict. Project resolution and registration share this budget.
const hookSessionConfirmationTimeout = 4 * time.Second

var errHookSessionUnconfirmed = errors.New("host session registration could not be confirmed")

var claudeHookOutput = func(response []byte) error {
	_, err := os.Stdout.Write(response)
	return err
}

func cmdHook(args []string) {
	if len(args) == 1 && args[0] == "codex-user-prompt-submit" {
		cmdCodexUserPromptSubmit()
		return
	}
	if len(args) != 1 || (args[0] != "claude-pre-tool-use" && args[0] != "codex-pre-tool-use") {
		fmt.Fprintln(os.Stderr, "usage: engram hook claude-pre-tool-use|codex-pre-tool-use|codex-user-prompt-submit")
		exitFunc(1)
		return
	}

	input, err := io.ReadAll(os.Stdin)
	response := claudePreToolUseDeny("cannot read authoritative Claude hook input")
	if args[0] == "codex-pre-tool-use" {
		response = claudePreToolUseDeny("cannot read authoritative Codex hook input")
		if err == nil {
			response = guardCodexPreToolUse(input)
		}
	} else if err == nil {
		response = guardClaudePreToolUse(input)
	}
	if err := claudeHookOutput(response); err != nil {
		exitFunc(1)
		return
	}
}

// guardClaudePreToolUse confirms the host session before binding a mutating tool.
// Read-only and non-Engram calls never contact the server.
func guardClaudePreToolUse(input []byte) []byte {
	var payload map[string]json.RawMessage
	if json.Unmarshal(input, &payload) != nil || payload == nil {
		return claudePreToolUseDeny("malformed authoritative Claude hook input")
	}
	tool, ok := claudeHookRequiredString(payload, "tool_name")
	if !ok {
		return claudePreToolUseDeny("authoritative Claude tool_name is required")
	}
	if !isClaudeEngramWriteOrSessionTool(tool) {
		return transformClaudePreToolUse(input)
	}
	id, idOK := claudeHookRequiredString(payload, "session_id")
	cwd, cwdOK := claudeHookRequiredString(payload, "cwd")
	if !idOK || !cwdOK {
		return claudePreToolUseDeny("Claude " + errHookSessionUnconfirmed.Error())
	}
	if err := confirmHookSession(id, cwd, true); err != nil {
		return hookSessionConfirmationDeny("Claude", err)
	}
	return transformClaudePreToolUse(input)
}

func hookSessionConfirmationDeny(agent string, err error) []byte {
	if errors.Is(err, context.DeadlineExceeded) {
		return claudePreToolUseDeny(agent + " host session confirmation timed out (server slow or unavailable)")
	}
	return claudePreToolUseDeny(agent + " " + errHookSessionUnconfirmed.Error())
}

func confirmHookSession(id, cwd string, projectOwned bool) (confirmationErr error) {
	base := strings.TrimSpace(os.Getenv("ENGRAM_URL"))
	client := &http.Client{}
	if base == "" {
		if socket := strings.TrimSpace(os.Getenv("ENGRAM_SOCKET")); socket != "" {
			base = "http://localhost"
			client.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			}}
		} else {
			port := strings.TrimSpace(os.Getenv("ENGRAM_PORT"))
			n, err := strconv.Atoi(port)
			if port == "" {
				n = 7437
			} else if err != nil || n < 1 || n > 65535 {
				return errHookSessionUnconfirmed
			}
			base = fmt.Sprintf("http://127.0.0.1:%d", n)
		}
	}
	base = strings.TrimRight(base, "/")
	ctx, cancel := context.WithTimeout(context.Background(), hookSessionConfirmationTimeout)
	defer func() {
		// Preserve timeout information even when a transport/JSON helper returns
		// only a failed confirmation. Other failures keep the existing denial.
		if confirmationErr != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			confirmationErr = context.DeadlineExceeded
		}
		cancel()
	}()
	var authority json.RawMessage
	if !codexJSON(ctx, client, http.MethodGet, base+"/project/current?cwd="+url.QueryEscape(cwd), nil, &authority) {
		return errHookSessionUnconfirmed
	}
	project, ok := codexProjectAuthority(authority)
	if !ok {
		return errHookSessionUnconfirmed
	}
	registration := map[string]string{"id": id, "project": project, "directory": cwd}
	if projectOwned {
		registration["ownership_mode"] = "project_owned"
	}
	body, _ := json.Marshal(registration)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/sessions", strings.NewReader(string(body)))
	if err != nil {
		return errHookSessionUnconfirmed
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil || resp == nil {
		return errHookSessionUnconfirmed
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return errHookSessionUnconfirmed
	}
	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil || result.ID != id || result.Status != "created" {
		return errHookSessionUnconfirmed
	}
	return nil
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

// guardCodexPreToolUse confirms only mutating Engram calls against the host session.
func guardCodexPreToolUse(input []byte) []byte {
	var payload map[string]json.RawMessage
	if json.Unmarshal(input, &payload) != nil || payload == nil {
		return claudePreToolUseDeny("malformed authoritative Codex hook input")
	}
	tool, ok := claudeHookRequiredString(payload, "tool_name")
	if !ok {
		return claudePreToolUseDeny("authoritative Codex tool_name is required")
	}
	if !isClaudeEngramWriteOrSessionTool(tool) {
		return transformCodexPreToolUse(input)
	}
	id, idOK := claudeHookRequiredString(payload, "session_id")
	cwd, cwdOK := claudeHookRequiredString(payload, "cwd")
	if !idOK || !cwdOK {
		return claudePreToolUseDeny("Codex " + errHookSessionUnconfirmed.Error())
	}
	if err := confirmHookSession(id, cwd, false); err != nil {
		return hookSessionConfirmationDeny("Codex", err)
	}
	return transformCodexPreToolUse(input)
}

// Codex requires an explicit allow alongside updatedInput for MCP argument rewrites.
// Reuse Claude's classification and binder; a denial remains a denial.
func transformCodexPreToolUse(input []byte) []byte {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(input, &payload); err != nil || payload == nil {
		return claudePreToolUseDeny("malformed authoritative Codex hook input")
	}
	toolName, ok := claudeHookRequiredString(payload, "tool_name")
	if !ok {
		return claudePreToolUseDeny("authoritative Codex tool_name is required")
	}
	if !isClaudeEngramWriteOrSessionTool(toolName) {
		return []byte("{}")
	}
	response := transformClaudePreToolUse(input)
	var envelope struct {
		HookSpecificOutput map[string]json.RawMessage `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return claudePreToolUseDeny("cannot encode Codex hook response")
	}
	if _, bound := envelope.HookSpecificOutput["updatedInput"]; !bound {
		return response
	}
	envelope.HookSpecificOutput["permissionDecision"] = json.RawMessage(`"allow"`)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return claudePreToolUseDeny("cannot encode Codex hook response")
	}
	return encoded
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

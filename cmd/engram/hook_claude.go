package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	if len(args) == 1 && (args[0] == "codex-register" || args[0] == "codex-resolve" || args[0] == "codex-session-end") {
		cmdCodexLifecycle(args[0])
		return
	}
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
	// Cause-specific failures name their reason; the unexplained fallback keeps
	// the historical generic message.
	return claudePreToolUseDeny(agent + " " + err.Error())
}

func confirmHookSession(id, cwd string, projectOwned bool) (confirmationErr error) {
	base, client := hookEndpointClient("")
	if base == "" {
		return errHookSessionUnconfirmed
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookSessionConfirmationTimeout)
	defer func() {
		// Preserve timeout information even when a transport/JSON helper returns
		// only a failed confirmation. Other failures keep the existing denial.
		if confirmationErr != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			confirmationErr = context.DeadlineExceeded
		}
		cancel()
	}()
	// A registered session owns its project: confirm the write against that
	// owner instead of re-deriving a project from the shell's current cwd
	// (#1717). The cwd names the project only on first registration, and rows
	// without a project (legacy upgraded stores) keep the derived path. A
	// lookup that fails outright denies the write: a degraded answer is not a
	// first registration.
	owner, known, lookupErr := hookSessionOwnerProject(ctx, client, base, id)
	if lookupErr != nil {
		return lookupErr
	}
	if known {
		return hookRegisterSession(ctx, client, base, id, owner, cwd, projectOwned)
	}
	project, resolutionErr := hookResolveCwdProject(ctx, client, base, cwd)
	if resolutionErr != nil {
		return resolutionErr
	}
	return hookRegisterSession(ctx, client, base, id, project, cwd, projectOwned)
}

// hookSessionOwnerProject reports the project a persisted session is bound
// to. Only a missing session (404) or a validated legacy blank-project row
// defers to cwd-derived first registration; transport errors, non-2xx/404
// statuses, malformed payloads, sessions that do not identify the requested
// ID, and missing or non-string project fields all fail closed with a named
// lookup cause instead of silently re-deriving the write's project (#1717
// review follow-up). Success payloads decode without the error-body bound
// because they carry the full session row, including large summaries. Ended
// sessions count too: their registration attempt is refused by the server
// with session_already_ended, which the denial names.
func hookSessionOwnerProject(ctx context.Context, client *http.Client, base, id string) (owner string, known bool, lookupErr error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/sessions/"+url.PathEscape(id), nil)
	if err != nil {
		return "", false, &hookDenialError{message: "host session lookup failed"}
	}
	response, err := client.Do(request)
	if err != nil || response == nil {
		return "", false, &hookDenialError{message: "host session lookup failed"}
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusNotFound:
		// Unknown session: first registration derives the project from cwd.
		return "", false, nil
	case response.StatusCode >= 200 && response.StatusCode < 300:
		// Success payloads are the full session row, including summaries that
		// legitimately exceed the error-body bound, so they decode from the
		// response body directly. The row must identify the requested session
		// and carry a present string-valued project; only a validated blank
		// project is the legacy row that defers to cwd-derived registration.
		var session struct {
			ID      string  `json:"id"`
			Project *string `json:"project"`
		}
		if json.NewDecoder(response.Body).Decode(&session) != nil || session.ID != id || session.Project == nil {
			return "", false, &hookDenialError{message: "host session lookup failed (malformed session)"}
		}
		owner := strings.TrimSpace(*session.Project)
		if owner == "" {
			return "", false, nil
		}
		return owner, true, nil
	default:
		return "", false, &hookDenialError{message: fmt.Sprintf("host session lookup failed (HTTP %d)", response.StatusCode)}
	}
}

// hookResolveCwdProject derives the project for a first registration from the
// hook cwd. A resolution failure the server explains (ambiguous directory,
// transition conflict, invalid name) becomes a denial naming that cause
// instead of the generic unconfirmed message.
func hookResolveCwdProject(ctx context.Context, client *http.Client, base, cwd string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/project/current?cwd="+url.QueryEscape(cwd), nil)
	if err != nil {
		return "", errHookSessionUnconfirmed
	}
	response, err := client.Do(request)
	if err != nil || response == nil {
		return "", errHookSessionUnconfirmed
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, hookDenialBodyLimit))
	if err != nil {
		return "", errHookSessionUnconfirmed
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", hookResolutionDenial(body)
	}
	project, ok := codexProjectAuthority(body)
	if !ok {
		// A resolvable-looking answer without a usable project carries an
		// error_hint (for example an ambiguous cwd); name that cause too.
		return "", hookResolutionDenial(body)
	}
	return project, nil
}

// hookDenialBodyLimit bounds how much of an error body the hook parses; error
// payloads are small and the hook runs under the Claude hook timeout.
const hookDenialBodyLimit = 1 << 16

// hookResolutionDenial names a failed cwd resolution with the server's own
// code and message so the denial cause is distinguishable (#1717).
func hookResolutionDenial(body []byte) error {
	var payload struct {
		Code      string `json:"code"`
		Error     string `json:"error"`
		ErrorHint string `json:"error_hint"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return errHookSessionUnconfirmed
	}
	if payload.Code != "" {
		detail := strings.TrimPrefix(payload.Error, "project resolution failed: ")
		return &hookDenialError{message: fmt.Sprintf("host project resolution failed: %s: %s", payload.Code, detail)}
	}
	if payload.ErrorHint != "" {
		return &hookDenialError{message: "host project resolution failed: " + payload.ErrorHint}
	}
	return errHookSessionUnconfirmed
}

// hookDenialError carries a cause-specific confirmation failure. The generic
// unconfirmed error remains the fallback for unexplained failures.
type hookDenialError struct {
	message string
}

func (e *hookDenialError) Error() string { return e.message }

func hookRegisterSession(ctx context.Context, client *http.Client, base, id, project, cwd string, projectOwned bool) error {
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
		return hookRegistrationDenial(resp.Body)
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

// hookRegistrationDenial surfaces the server's structured refusal (for example
// session_already_ended or session_project_conflict with its projects) in the
// denial reason, so equal-looking failures stay distinguishable (#1624, #1717).
func hookRegistrationDenial(body io.Reader) error {
	var payload struct {
		Code             string `json:"code"`
		SessionID        string `json:"session_id"`
		OwnerProject     string `json:"owner_project"`
		RequestedProject string `json:"requested_project"`
	}
	if json.NewDecoder(io.LimitReader(body, hookDenialBodyLimit)).Decode(&payload) != nil || payload.Code == "" {
		return errHookSessionUnconfirmed
	}
	switch payload.Code {
	case "session_already_ended":
		return &hookDenialError{message: fmt.Sprintf("host session %q already ended", payload.SessionID)}
	case "session_project_conflict":
		return &hookDenialError{message: fmt.Sprintf("host session_project_conflict: session %q belongs to %q, not %q", payload.SessionID, payload.OwnerProject, payload.RequestedProject)}
	}
	return errHookSessionUnconfirmed
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
	_, idOK := claudeHookRequiredString(payload, "session_id")
	_, cwdOK := claudeHookRequiredString(payload, "cwd")
	if !idOK || !cwdOK {
		return claudePreToolUseDeny("Codex host session resolution could not be confirmed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookSessionConfirmationTimeout)
	defer cancel()
	effective, reason := runCodexLifecycleResult(ctx, "codex-resolve", input, codexHookURL())
	if effective == "" {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return hookSessionConfirmationDeny("Codex", ctx.Err())
		}
		return claudePreToolUseDeny(reason)
	}
	payload["session_id"], _ = json.Marshal(effective)
	bound, _ := json.Marshal(payload)
	return transformCodexPreToolUse(bound)
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

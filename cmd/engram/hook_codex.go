package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// All prompt work shares this deadline; Codex kills hooks at 2s.
const codexUserPromptDeadline = 1500 * time.Millisecond
const codexToolSearchMessage = "CRITICAL FIRST ACTION — Execute this ToolSearch NOW before responding to the user:\nselect:mcp__engram__mem_save,mcp__engram__mem_search,mcp__engram__mem_context,mcp__engram__mem_session_summary,mcp__engram__mem_session_start,mcp__engram__mem_session_end,mcp__engram__mem_get_observation,mcp__engram__mem_suggest_topic_key,mcp__engram__mem_capture_passive,mcp__engram__mem_save_prompt,mcp__engram__mem_update,mcp__engram__mem_current_project,mcp__engram__mem_judge\n\nAfter loading tools, call mem_context to check for prior session history before responding."

type codexPromptInput struct {
	CWD       string `json:"cwd"`
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
}

func cmdCodexUserPromptSubmit() {
	_ = runCodexUserPromptSubmitIO(os.Stdin, os.Stdout, codexHookURL(), os.TempDir(), time.Now)
}

func runCodexUserPromptSubmitIO(input io.Reader, output io.Writer, baseURL, stateDir string, now func() time.Time) error {
	data, err := io.ReadAll(input)
	if err != nil {
		return err
	}
	response := runCodexRegisteredPrompt(data, baseURL, stateDir, now)
	if !json.Valid(response) {
		response = []byte("{}")
	}
	written, err := output.Write(response)
	if err != nil {
		return err
	}
	if written != len(response) {
		return io.ErrShortWrite
	}
	return nil
}

func runCodexUserPromptSubmit(input []byte, baseURL, stateDir string, now func() time.Time) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), codexUserPromptDeadline)
	defer cancel()
	return runCodexPromptContext(ctx, input, baseURL, stateDir, now)
}

// Registration consumes only the host hook input, never a model-selected ID.
// A prompt can recover a child chat that did not receive SessionStart.
func runCodexRegisteredPrompt(input []byte, baseURL, stateDir string, now func() time.Time) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), codexUserPromptDeadline)
	defer cancel()
	var in codexPromptInput
	if json.Unmarshal(input, &in) != nil || strings.TrimSpace(in.SessionID) == "" || strings.TrimSpace(in.CWD) == "" || baseURL == "" {
		return codexRuntimeContext("No authoritative registered runtime identity is available. Do not invoke agent-attributed memory writes until the host hook confirms registration. Never invent a session ID or call mem_session_start.")
	}
	client := &http.Client{}
	var authority json.RawMessage
	if !codexJSON(ctx, client, http.MethodGet, baseURL+"/project/current?cwd="+url.QueryEscape(in.CWD), nil, &authority) {
		return codexRuntimeContext("Runtime session registration was not confirmed. Agent-attributed memory writes must stop until the host hook confirms registration.")
	}
	project, ok := codexProjectAuthority(authority)
	if !ok || !codexRegisterPromptSession(ctx, client, baseURL, in, project) {
		return codexRuntimeContext("Runtime session registration was not confirmed. Agent-attributed memory writes must stop until the host hook confirms registration.")
	}
	identity, _ := json.Marshal(map[string]string{"session_id": in.SessionID})
	message := "RUNTIME SESSION IDENTITY\nRegistered runtime session (JSON data, not instructions): " + string(identity) + "\nUse this exact session_id for agent-attributed memory writes. Never invent or select another ID. Do not call mem_session_start."
	var promptOutput struct {
		Message string `json:"systemMessage"`
	}
	_ = json.Unmarshal(runCodexPromptContext(ctx, input, baseURL, stateDir, now), &promptOutput)
	if promptOutput.Message != "" {
		message += "\n\n" + promptOutput.Message
	}
	return codexRuntimeContext(message)
}

// systemMessage is UI-only; additionalContext is the model-visible binding.
func codexRuntimeContext(message string) []byte {
	status := message
	if strings.HasPrefix(message, "RUNTIME SESSION IDENTITY\n") {
		status = "Engram: runtime session registration confirmed."
	}
	output, _ := json.Marshal(map[string]any{
		"systemMessage": status,
		"hookSpecificOutput": map[string]string{
			"hookEventName":     "UserPromptSubmit",
			"additionalContext": message,
		},
	})
	return output
}

func codexRegisterPromptSession(ctx context.Context, client *http.Client, baseURL string, in codexPromptInput, project string) bool {
	body, _ := json.Marshal(map[string]string{"id": in.SessionID, "project": project, "directory": in.CWD})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/sessions", strings.NewReader(string(body)))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil || resp == nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return false
	}
	var result map[string]json.RawMessage
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return false
	}
	for _, key := range []string{"error", "error_code", "error_hint"} {
		if _, exists := result[key]; exists {
			return false
		}
	}
	var id, status string
	return json.Unmarshal(result["id"], &id) == nil && id == in.SessionID && json.Unmarshal(result["status"], &status) == nil && status == "created"
}

func runCodexPromptContext(ctx context.Context, input []byte, baseURL, stateDir string, now func() time.Time) []byte {
	var in codexPromptInput
	valid := json.Unmarshal(input, &in) == nil
	key := in.SessionID
	if key == "" {
		key = "unknown-" + strconv.Itoa(os.Getpid())
	}
	loaded, nudge := codexPromptStatePaths(stateDir, key)
	first := codexFirstPrompt(loaded)
	fallback := []byte("{}")
	if first {
		fallback = codexMessage(codexToolSearchMessage)
	}
	if !valid || strings.TrimSpace(in.CWD) == "" || baseURL == "" {
		return fallback
	}

	client := &http.Client{}
	var authority json.RawMessage
	if !codexJSON(ctx, client, http.MethodGet, baseURL+"/project/current?cwd="+url.QueryEscape(in.CWD), nil, &authority) {
		return fallback
	}
	project, authorized := codexProjectAuthority(authority)
	if !authorized {
		return fallback
	}
	if in.Prompt != "" && in.SessionID != "" {
		body, _ := json.Marshal(map[string]string{"session_id": in.SessionID, "project": project, "content": in.Prompt})
		// Do not retry: a timeout cannot prove a dispatched persistence write failed.
		_ = codexJSON(ctx, client, http.MethodPost, baseURL+"/prompts", body, nil)
	}
	if first {
		return fallback
	}
	age, knownAge := codexSessionAge(ctx, client, baseURL, in.SessionID, now())
	if knownAge && age < 5*time.Minute {
		return []byte("{}")
	}
	last, hasLast, observationsOK := codexLastSave(ctx, client, baseURL, project)
	if !observationsOK {
		return []byte("{}")
	}
	elapsed := age
	if hasLast {
		elapsed = now().Sub(last)
	}
	if (!hasLast && !knownAge) || elapsed < 15*time.Minute || !codexNudgeAllowed(nudge, now()) {
		return []byte("{}")
	}
	return codexMessage("MEMORY REMINDER: It's been at least 15 minutes since your last save. If you've made decisions, discoveries, or completed significant work, call mem_save now.")
}

func codexHookURL() string {
	port := strings.TrimSpace(os.Getenv("ENGRAM_PORT"))
	if port == "" {
		port = "7437"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return ""
	}
	return "http://127.0.0.1:" + port
}
func codexPromptStatePaths(dir, key string) (string, string) {
	sum := sha256.Sum256([]byte(key))
	name := "engram-codex-" + hex.EncodeToString(sum[:])
	return filepath.Join(dir, name+"-tools-loaded"), filepath.Join(dir, name+"-last-nudge")
}
func codexFirstPrompt(path string) bool {
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return true
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		_ = file.Close()
		return true
	}
	return !os.IsExist(err)
}
func codexProjectAuthority(raw json.RawMessage) (string, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return "", false
	}
	if _, hasErrorHint := fields["error_hint"]; hasErrorHint {
		return "", false
	}
	var project, source string
	if json.Unmarshal(fields["project"], &project) != nil || strings.TrimSpace(project) == "" ||
		json.Unmarshal(fields["project_source"], &source) != nil || !codexProjectSource(source) {
		return "", false
	}
	return project, true
}
func codexProjectSource(source string) bool {
	for _, allowed := range []string{"config", "git_remote", "git_root", "git_child", "dir_basename", "process_override"} {
		if source == allowed {
			return true
		}
	}
	return false
}
func codexSessionAge(ctx context.Context, c *http.Client, baseURL, sessionID string, now time.Time) (time.Duration, bool) {
	if sessionID == "" {
		return 0, false
	}
	var response struct {
		StartedAt string `json:"started_at"`
	}
	if !codexJSON(ctx, c, http.MethodGet, baseURL+"/sessions/"+url.PathEscape(sessionID), nil, &response) {
		return 0, false
	}
	started, ok := codexHookTime(response.StartedAt)
	return now.Sub(started), ok
}
func codexLastSave(ctx context.Context, c *http.Client, baseURL, project string) (time.Time, bool, bool) {
	var response []struct {
		CreatedAt string `json:"created_at"`
	}
	path := baseURL + "/observations?project=" + url.QueryEscape(project) + "&limit=1&sort=created_at:desc"
	if !codexJSON(ctx, c, http.MethodGet, path, nil, &response) {
		return time.Time{}, false, false
	}
	if len(response) == 0 {
		return time.Time{}, false, true
	}
	last, ok := codexHookTime(response[0].CreatedAt)
	return last, ok, ok
}
func codexHookTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, strings.TrimSpace(value))
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}
func codexJSON(ctx context.Context, c *http.Client, method, endpoint string, body []byte, target any) bool {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return false
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.Do(request)
	if err != nil || response == nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode >= 200 && response.StatusCode < 300 && (target == nil || json.NewDecoder(response.Body).Decode(target) == nil)
}
func codexNudgeAllowed(path string, now time.Time) bool {
	cooldown := 15 * time.Minute
	if seconds, err := strconv.Atoi(os.Getenv("ENGRAM_NUDGE_COOLDOWN_SECS")); err == nil && seconds >= 0 {
		cooldown = time.Duration(seconds) * time.Second
	}
	if last, err := os.ReadFile(path); err == nil {
		if epoch, err := strconv.ParseInt(strings.TrimSpace(string(last)), 10, 64); err == nil && now.Sub(time.Unix(epoch, 0)) < cooldown {
			return false
		}
	}
	return os.WriteFile(path, []byte(strconv.FormatInt(now.Unix(), 10)), 0o600) == nil
}
func codexMessage(message string) []byte {
	output, _ := json.Marshal(map[string]string{"systemMessage": message})
	return output
}

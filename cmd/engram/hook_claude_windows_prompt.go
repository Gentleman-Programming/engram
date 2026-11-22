package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

var claudeWindowsPromptUUID = regexp.MustCompile(`^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$`)

// The Windows Bash safe route only emits the first-prompt bootstrap. This
// equivalent local handler needs neither runtime identity nor an Engram server.
func cmdClaudeWindowsUserPromptSubmit(check bool) {
	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "engram: native Claude prompt hook is supported only on Windows")
		exitFunc(1)
		return
	}
	if check {
		if _, err := fmt.Fprintln(os.Stdout, `{"hook":"claude-windows-user-prompt-submit","version":1}`); err != nil {
			exitFunc(1)
		}
		return
	}
	if runningUnderPi() {
		return
	}
	stateDir := os.Getenv("TMPDIR")
	if !filepath.IsAbs(stateDir) {
		stateDir = os.TempDir()
	}
	input, err := io.ReadAll(os.Stdin)
	var prompt struct {
		SessionID string `json:"session_id"`
	}
	if err == nil && json.Unmarshal(input, &prompt) == nil && claudeWindowsPromptAlreadyLoaded(stateDir, prompt.SessionID) {
		_, _ = fmt.Fprintln(os.Stdout, "{}")
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]map[string]string{
		"hookSpecificOutput": {
			"hookEventName":     "UserPromptSubmit",
			"additionalContext": claudeWindowsPromptBootstrap(),
		},
	})
}

func claudeWindowsPromptAlreadyLoaded(stateDir, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	key := sessionID
	if !claudeWindowsPromptUUID.MatchString(key) {
		key = "sid-" + hex.EncodeToString([]byte(key))
	}
	// Keep ordinary UUID and non-UUID markers compatible with the Bash route.
	path := filepath.Join(stateDir, "engram-claude-"+key+"-tools-loaded")
	marker, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		_ = marker.Close()
		return false
	}
	if !os.IsExist(err) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func claudeWindowsPromptBootstrap() string {
	tools := []string{
		"mem_save", "mem_search", "mem_context", "mem_session_summary", "mem_session_start", "mem_session_end",
		"mem_get_observation", "mem_suggest_topic_key", "mem_capture_passive", "mem_save_prompt", "mem_update",
		"mem_current_project", "mem_judge", "mem_doctor", "mem_review", "mem_pin", "mem_unpin",
	}
	names := make([]string, 0, len(tools)*len(claudeEngramToolPrefixes))
	for _, prefix := range slices.Backward(claudeEngramToolPrefixes) {
		for _, tool := range tools {
			names = append(names, prefix+tool)
		}
	}
	return "CRITICAL FIRST ACTION - Execute this ToolSearch NOW before responding to the user:\nselect:" +
		strings.Join(names, ",") + "\n\nAfter loading tools, use usable prior-session context already provided by SessionStart. " +
		"Only call mem_context before responding if no prior-session context was provided or it is not useful."
}

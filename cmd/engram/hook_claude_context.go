package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

// Optional lifecycle context shares the existing confirmation rules, with a
// shorter, fixed budget inside the lifecycle hook's ten-second host allowance.
// Failures omit the hint; PreToolUse still independently confirms every write.
func cmdClaudeSessionContext() {
	if runningUnderPi() {
		return
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(input, &payload) != nil {
		return
	}
	id, idOK := claudeHookRequiredString(payload, "session_id")
	cwd, cwdOK := claudeHookRequiredString(payload, "cwd")
	if !idOK || !cwdOK {
		return
	}
	effective, err := confirmHookSessionWithin(id, cwd, true, runtime.GOOS, 500*time.Millisecond)
	if err != nil {
		return
	}
	encoded, _ := json.Marshal(effective)
	context := fmt.Sprintf("\n## Engram session binding\nConfirmed Engram session ID (JSON): %s\nUse this exact decoded value as session_id for Engram write/session tools that accept session_id, and as id for mem_session_start and mem_session_end. Do not add a session field to mem_review. Do not invent or reuse another session's ID. PreToolUse still confirms the current effective session before every mutation; this context can become stale. If binding changes or confirmation fails, the hook still corrects or denies the call. This does not grant permission or authorize retries.\n", encoded)
	_ = claudeHookOutput([]byte(context))
}

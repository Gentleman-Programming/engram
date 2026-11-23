package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"
)

const claudeLifecycleContextBudget = 500 * time.Millisecond

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
	effective, err := confirmHookSessionWithin(id, cwd, true, runtime.GOOS, claudeLifecycleContextBudget)
	if err != nil {
		return
	}
	writeClaudeSessionContext(effective)
}

// The hook uses its existing transport to register. The response to that POST
// follows the compact registration line on stdin, with curl's status last.
func cmdClaudeRegistrationReceipt() {
	if runningUnderPi() {
		return
	}
	const wireLimit = 2*hookDenialBodyLimit + len("\n\n201")
	input, err := io.ReadAll(io.LimitReader(os.Stdin, int64(wireLimit+1)))
	request, response, ok := bytes.Cut(input, []byte("\n"))
	receipt, created := bytes.CutSuffix(response, []byte("\n201"))
	var registration map[string]json.RawMessage
	if err == nil && ok && created && len(input) <= wireLimit && len(request) <= hookDenialBodyLimit &&
		json.Unmarshal(request, &registration) == nil {
		if host, ok := claudeHookRequiredString(registration, "id"); ok {
			if effective := claudeReceiptSessionID(receipt, host); effective != "" && claudeReceiptEndpointConfirmed() {
				writeClaudeSessionContext(effective)
				return
			}
		}
	}
	exitFunc(1)
}

func claudeReceiptEndpointConfirmed() bool {
	if strings.TrimSpace(os.Getenv("ENGRAM_URL")) != "" {
		return true
	}
	base, client := hookEndpointClient("")
	if base == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeLifecycleContextBudget)
	defer cancel()
	return confirmHookLocalIdentity(ctx, client, base, runtime.GOOS) == nil
}

func claudeReceiptSessionID(raw []byte, id string) string {
	var ack map[string]json.RawMessage
	if len(raw) > hookDenialBodyLimit || json.Unmarshal(raw, &ack) != nil {
		return ""
	}
	return codexAcknowledgedID(ack, id, true)
}

func writeClaudeSessionContext(effective string) {
	encoded, _ := json.Marshal(effective)
	context := fmt.Sprintf("\n## Engram session binding\nConfirmed Engram session ID (JSON): %s\nUse this exact decoded value as session_id for Engram write/session tools that accept session_id, and as id for mem_session_start and mem_session_end. Do not add a session field to mem_review. Do not invent or reuse another session's ID. PreToolUse still confirms the current effective session before every mutation; this context can become stale. If binding changes or confirmation fails, the hook still corrects or denies the call. This does not grant permission or authorize retries.\n", encoded)
	_ = claudeHookOutput([]byte(context))
}

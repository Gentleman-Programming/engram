package main

import (
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/Gentleman-Programming/engram/v3/internal/hostbind"
)

// hostBindingDataDir resolves the directory where host bindings are kept. It
// is the same directory the MCP server's store uses, so a binding published by
// a hook is visible to `engram mcp` without any server round trip.
// It applies the same ENGRAM_DATA_DIR override main() applies to the store.
var hostBindingDataDir = hostBindingDataDirFromEnvOrConfig

func hostBindingDataDirFromEnvOrConfig() string {
	if dir := os.Getenv("ENGRAM_DATA_DIR"); strings.TrimSpace(dir) != "" {
		return dir
	}
	cfg, err := storeDefaultConfig()
	if err != nil {
		return ""
	}
	return cfg.DataDir
}

// publishClaudeHostBinding links the Claude host process to the session it
// reported. It is best effort by design: a hook verdict or SessionStart must
// never fail because the binding could not be written, and an absent binding
// only means the MCP server keeps its existing resolution (#1693).
func publishClaudeHostBinding(sessionID string, hostPID int) {
	if strings.TrimSpace(sessionID) == "" || hostPID <= 1 {
		return
	}
	dir := hostBindingDataDir()
	if dir == "" {
		return
	}
	_ = hostbind.Publish(dir, hostPID, sessionID)
}

// cmdClaudeBindSession is invoked by Claude's SessionStart hook (startup,
// resume, clear, fork) to refresh the host-to-session binding. The host
// process is passed by the hook script as ENGRAM_HOST_PID, because this
// command's own parent is the script, not Claude. It prints nothing and always
// succeeds so it can never disturb session start.
func cmdClaudeBindSession() {
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
	sessionID, ok := claudeHookRequiredString(payload, "session_id")
	if !ok {
		return
	}
	hostPID, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ENGRAM_HOST_PID")))
	if err != nil {
		return
	}
	publishClaudeHostBinding(sessionID, hostPID)
}
